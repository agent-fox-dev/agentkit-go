package outline_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-01-72 (smoke): An embedder outlines a Go file with no subprocess and gets
// a root-relative go/ast File.
func TestOutlineGoFileNoSubprocess_TS_01_72(t *testing.T) {
	root := t.TempDir()
	goSrc := `package pkg

type MyStruct struct {
	Field int
}

func (m *MyStruct) Method() string {
	return ""
}

func FreeFunc(x int) int {
	return x + 1
}

const Version = "1.0"
`
	goFile := filepath.Join(root, "pkg", "a.go")
	if err := os.MkdirAll(filepath.Dir(goFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, []byte(goSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set PATH to empty so any spawn would fail (proving no subprocess).
	if runtime.GOOS != "windows" {
		t.Setenv("PATH", "")
	}

	ctx := context.Background()
	f, err := outline.Outline(ctx, goFile, nil, outline.Options{Root: root})
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}

	// File.Path is root-relative, slash-separated.
	if f.Path != "pkg/a.go" {
		t.Fatalf("File.Path = %q, want %q", f.Path, "pkg/a.go")
	}

	// Backend is go/ast.
	if f.Backend != outline.BackendGoAST {
		t.Fatalf("Backend = %q, want %q", f.Backend, outline.BackendGoAST)
	}

	// Decls are sorted by StartLine.
	for i := 1; i < len(f.Decls); i++ {
		if f.Decls[i].StartLine < f.Decls[i-1].StartLine {
			t.Fatalf("Decls not sorted by StartLine: %v at %d before %v at %d",
				f.Decls[i-1].Name, f.Decls[i-1].StartLine,
				f.Decls[i].Name, f.Decls[i].StartLine)
		}
	}

	// Build a map for easy lookup.
	declMap := map[string]outline.Decl{}
	for _, d := range f.Decls {
		declMap[d.Name] = d
	}

	// Check expected declarations.
	if d, ok := declMap["MyStruct"]; !ok {
		t.Error("missing MyStruct")
	} else {
		if d.Kind != outline.KindType {
			t.Errorf("MyStruct.Kind = %q, want %q", d.Kind, outline.KindType)
		}
		if !d.Exported {
			t.Error("MyStruct should be exported")
		}
	}

	if d, ok := declMap["Method"]; !ok {
		t.Error("missing Method")
	} else {
		if d.Kind != outline.KindMethod {
			t.Errorf("Method.Kind = %q, want %q", d.Kind, outline.KindMethod)
		}
		if d.Container != "MyStruct" {
			t.Errorf("Method.Container = %q, want %q", d.Container, "MyStruct")
		}
	}

	if d, ok := declMap["FreeFunc"]; !ok {
		t.Error("missing FreeFunc")
	} else {
		if d.Kind != outline.KindFunc {
			t.Errorf("FreeFunc.Kind = %q, want %q", d.Kind, outline.KindFunc)
		}
	}

	if d, ok := declMap["Version"]; !ok {
		t.Error("missing Version")
	} else {
		if d.Kind != outline.KindConst {
			t.Errorf("Version.Kind = %q, want %q", d.Kind, outline.KindConst)
		}
	}

	// Verify ranges match go/ast by parsing the same source independently.
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, goFile, goSrc, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("go/parser: %v", err)
	}
	astDecls := map[string][2]int{} // name -> [startLine, endLine]
	for _, d := range astFile.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			astDecls[name] = [2]int{
				fset.Position(d.Pos()).Line,
				fset.Position(d.End()).Line,
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					grouped := d.Lparen.IsValid()
					var sl, el int
					if grouped {
						sl = fset.Position(spec.Pos()).Line
						el = fset.Position(spec.End()).Line
					} else {
						sl = fset.Position(d.Pos()).Line
						el = fset.Position(d.End()).Line
					}
					astDecls[spec.Name.Name] = [2]int{sl, el}
				case *ast.ValueSpec:
					for _, ident := range spec.Names {
						grouped := d.Lparen.IsValid()
						var sl, el int
						if grouped {
							sl = fset.Position(spec.Pos()).Line
							el = fset.Position(spec.End()).Line
						} else {
							sl = fset.Position(d.Pos()).Line
							el = fset.Position(d.End()).Line
						}
						astDecls[ident.Name] = [2]int{sl, el}
					}
				}
			}
		}
	}

	for _, d := range f.Decls {
		expected, ok := astDecls[d.Name]
		if !ok {
			continue // e.g. _ or something we didn't track
		}
		if d.StartLine != expected[0] {
			t.Errorf("%s.StartLine = %d, want %d", d.Name, d.StartLine, expected[0])
		}
		if d.EndLine != expected[1] {
			t.Errorf("%s.EndLine = %d, want %d", d.Name, d.EndLine, expected[1])
		}
	}

	// Verify signatures are sanitised (no newlines, no control chars, <= 200 bytes).
	for _, d := range f.Decls {
		if strings.ContainsAny(d.Signature, "\n\r") {
			t.Errorf("%s signature contains newline: %q", d.Name, d.Signature)
		}
		if len(d.Signature) > 200 {
			t.Errorf("%s signature is %d bytes, want <= 200", d.Name, len(d.Signature))
		}
	}
}

// TS-01-75 (smoke): An embedder walks a workspace and outlines exactly the
// files search_files would select.
func TestWalkAndOutlineMatchesSearchFiles_TS_01_75(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".gitignore", "ignored/\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("lib.py", "def helper():\n    pass\n")
	write(".hidden.go", "package hidden\n")
	write("ignored/skip.go", "package skip\n")

	// Create a symlink.
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(filepath.Join(root, "main.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot := ws.Root

	ctx := context.Background()

	// Walk with IncludeHidden=false, collecting regular files.
	var walkPaths []string
	err = tools.Walk(ctx, ws, resolvedRoot, tools.WalkOptions{
		Ignore:        tools.NoGlobalExcludes(),
		IncludeHidden: false,
	}, func(rel string, d os.DirEntry) error {
		if d.Type().IsRegular() {
			walkPaths = append(walkPaths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	// Search with a match-everything pattern.
	res, err := tools.SearchIn(ctx, resolvedRoot, tools.SearchParams{Pattern: "."}, tools.NoGlobalExcludes())
	if err != nil {
		t.Fatalf("SearchIn: %v", err)
	}
	searchSet := map[string]bool{}
	for _, m := range res.Matches {
		searchSet[m.File] = true
	}

	// The walk set should equal the search set.
	walkSet := map[string]bool{}
	for _, f := range walkPaths {
		walkSet[f] = true
	}
	for f := range searchSet {
		if !walkSet[f] {
			t.Errorf("search_files found %q but Walk did not visit it", f)
		}
	}
	for f := range walkSet {
		if !searchSet[f] {
			t.Errorf("Walk visited %q but search_files did not search it", f)
		}
	}

	// Hidden and ignored files are absent.
	if walkSet[".hidden.go"] {
		t.Error("hidden file .hidden.go should not be in walk set")
	}
	if walkSet["ignored/skip.go"] {
		t.Error("ignored file should not be in walk set")
	}

	// Outline the walked files.
	srcs := make([]outline.Source, len(walkPaths))
	for i, rel := range walkPaths {
		srcs[i] = outline.Source{Abs: filepath.Join(resolvedRoot, filepath.FromSlash(rel))}
	}
	files, err := outline.OutlineMany(ctx, srcs, outline.Options{Root: resolvedRoot})
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}

	// One File per collected path.
	if len(files) != len(walkPaths) {
		t.Fatalf("OutlineMany returned %d files, want %d", len(files), len(walkPaths))
	}

	// Go files have Backend go/ast and Python files tree-sitter (none
	// without cgo).
	for _, f := range files {
		switch {
		case strings.HasSuffix(f.Path, ".go"):
			if f.Backend != outline.BackendGoAST {
				t.Errorf("%s Backend = %q, want %q", f.Path, f.Backend, outline.BackendGoAST)
			}
		case strings.HasSuffix(f.Path, ".py"):
			if f.Backend != nonGoBackend {
				t.Errorf("%s Backend = %q, want %q", f.Path, f.Backend, nonGoBackend)
			}
		}
	}
}

// --- helpers ---

func declNames(decls []outline.Decl) []string {
	names := make([]string, len(decls))
	for i, d := range decls {
		names[i] = d.Name
	}
	return names
}

func sliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
