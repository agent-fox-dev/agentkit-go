package policy

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const modulePath = "github.com/agent-fox-dev/agentkit-go"

// apiPackages are the packages whose exported surface TS-13-67 records in
// testdata/exported-api.golden, relative to the repository root: the original
// contract (the root, core, tools, codemode) and the packages PRD 12 changed or
// added. The golden is a change detector over this list, not a freeze: a change
// to one of these surfaces is deliberate, made with AGENTKIT_UPDATE_API_GOLDEN=1
// and reviewed in the diff of the golden. New entries go at the end, so the
// existing sections do not move.
var apiPackages = []string{".", "core", "tools", "codemode", "workspace", "guard", "provider/faux", "sandbox", "codesearch", "mcpserve"}

// moduleGoFiles returns every Go file of the root module, relative to root:
// nested modules, testdata, build output and dot-directories are skipped.
func moduleGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if n := d.Name(); n == "testdata" || n == "build" || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			rel, _ := filepath.Rel(root, path)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TS-13-67: only sandbox/, examples/sandbox and internal test helpers import
// sandbox packages, only they import grpc, and the exported API of every
// package in apiPackages equals the golden. 13-REQ-10.7, which froze the
// original four packages; the golden is now the change detector over
// apiPackages (see its comment). A directory in apiPackages that does not
// exist fails the test, naming it, and a difference is listed as lines gained
// and lost.
func TestSandboxLayoutAndAPI_TS13_67(t *testing.T) {
	root := repoRoot(t)
	files := moduleGoFiles(t, root)
	fset := token.NewFileSet()
	imports := map[string][]string{}
	importedByCode := map[string]bool{}
	for _, f := range files {
		pf, err := parser.ParseFile(fset, filepath.Join(root, f), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range pf.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			imports[f] = append(imports[f], p)
			if !strings.HasSuffix(f, "_test.go") {
				importedByCode[p] = true
			}
		}
	}
	under := func(f string, dirs ...string) bool {
		return slices.ContainsFunc(dirs, func(d string) bool { return strings.HasPrefix(f, d) })
	}
	// testHelper: a file under internal/ that is a test, or belongs to a
	// package no non-test file imports.
	testHelper := func(f string) bool {
		if !under(f, "internal/") {
			return false
		}
		return strings.HasSuffix(f, "_test.go") || !importedByCode[modulePath+"/"+filepath.ToSlash(filepath.Dir(f))]
	}
	for f, imps := range imports {
		for _, imp := range imps {
			if (imp == modulePath+"/sandbox" || strings.HasPrefix(imp, modulePath+"/sandbox/")) &&
				!under(f, "sandbox/", "examples/sandbox/") && !testHelper(f) {
				t.Errorf("%s imports %s from outside sandbox/ and examples/sandbox/", f, imp)
			}
			if (imp == "google.golang.org/grpc" || strings.HasPrefix(imp, "google.golang.org/grpc/")) &&
				!strings.HasSuffix(f, "_test.go") && !under(f, "sandbox/", "examples/sandbox/") {
				t.Errorf("%s imports %s outside sandbox/ and examples/sandbox/", f, imp)
			}
		}
	}

	var got bytes.Buffer
	for _, pkg := range apiPackages {
		got.WriteString("== " + pkg + "\n")
		for _, line := range exportedAPI(t, filepath.Join(root, pkg)) {
			got.WriteString(line + "\n")
		}
	}
	golden := filepath.Join(root, "internal", "policy", "testdata", "exported-api.golden")
	if os.Getenv("AGENTKIT_UPDATE_API_GOLDEN") != "" {
		if err := os.WriteFile(golden, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Skip("wrote " + golden)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		gl, wl := strings.Split(got.String(), "\n"), strings.Split(string(want), "\n")
		for _, l := range gl {
			if !slices.Contains(wl, l) {
				t.Errorf("exported API gained: %s", l)
			}
		}
		for _, l := range wl {
			if !slices.Contains(gl, l) {
				t.Errorf("exported API lost: %s", l)
			}
		}
	}
}

// exportedAPI lists the exported declarations of the package in dir, one
// sorted line each: functions and methods with their signatures, types with
// their exported fields and methods, constants and variables with their
// types.
func exportedAPI(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	print := func(n any) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, n); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() || (d.Recv != nil && !ast.IsExported(recvName(d.Recv))) {
					continue
				}
				seen[print(&ast.FuncDecl{Recv: d.Recv, Name: d.Name, Type: d.Type})] = true
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							seen["type "+print(&ast.TypeSpec{Name: s.Name, TypeParams: s.TypeParams, Assign: s.Assign, Type: exportedOnly(s.Type)})] = true
						}
					case *ast.ValueSpec:
						for _, n := range s.Names {
							if n.IsExported() {
								typ := ""
								if s.Type != nil {
									typ = " " + print(s.Type)
								}
								seen[d.Tok.String()+" "+n.Name+typ] = true
							}
						}
					}
				}
			}
		}
	}
	lines := make([]string, 0, len(seen))
	for l := range seen {
		lines = append(lines, l)
	}
	slices.Sort(lines)
	return lines
}

// recvName is the base type name of a method receiver.
func recvName(fl *ast.FieldList) string {
	expr := fl.List[0].Type
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

// exportedOnly drops the unexported fields of a struct and the unexported
// methods of an interface, so only the exported surface is compared.
func exportedOnly(expr ast.Expr) ast.Expr {
	keep := func(fl *ast.FieldList) *ast.FieldList {
		if fl == nil {
			return nil
		}
		out := &ast.FieldList{}
		for _, f := range fl.List {
			var names []*ast.Ident
			for _, n := range f.Names {
				if n.IsExported() {
					names = append(names, n)
				}
			}
			embedded := len(f.Names) == 0
			if len(names) > 0 || (embedded && ast.IsExported(typeName(f.Type))) {
				out.List = append(out.List, &ast.Field{Names: names, Type: f.Type})
			}
		}
		return out
	}
	switch e := expr.(type) {
	case *ast.StructType:
		return &ast.StructType{Fields: keep(e.Fields)}
	case *ast.InterfaceType:
		return &ast.InterfaceType{Methods: keep(e.Methods)}
	}
	return expr
}

// typeName is the name an embedded field is known by.
func typeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return typeName(e.X)
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	}
	return ""
}
