package outline

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-01-8: Outline reads the file itself when Src is nil and uses Src when supplied.
func TestOutline_SrcNilVsProvided_TS_01_8(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "a.go")

	// Write a file with func OnDisk.
	diskContent := "package a\n\nfunc OnDisk() {}\n"
	if err := os.WriteFile(goFile, []byte(diskContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Call with nil Src: should read from disk.
	f1, err := Outline(ctx, goFile, nil, Options{})
	if err != nil {
		t.Fatalf("nil Src: unexpected error: %v", err)
	}
	names1 := declNames(f1.Decls)
	if !sliceContains(names1, "OnDisk") {
		t.Fatalf("nil Src: expected OnDisk in decls, got %v", names1)
	}
	if sliceContains(names1, "InMemory") {
		t.Fatalf("nil Src: should not contain InMemory, got %v", names1)
	}

	// Call with non-nil Src containing func InMemory.
	memContent := []byte("package a\n\nfunc InMemory() {}\n")
	f2, err := Outline(ctx, goFile, memContent, Options{})
	if err != nil {
		t.Fatalf("Src provided: unexpected error: %v", err)
	}
	names2 := declNames(f2.Decls)
	if !sliceContains(names2, "InMemory") {
		t.Fatalf("Src provided: expected InMemory in decls, got %v", names2)
	}
	if sliceContains(names2, "OnDisk") {
		t.Fatalf("Src provided: should not contain OnDisk, got %v", names2)
	}
}

// TS-01-10: A file over MaxFileBytes is returned as none with no error,
// while exactly MaxFileBytes is outlined; zero means 1 MiB.
func TestOutline_MaxFileBytes_TS_01_10(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Helper to create a Go file of a given size with a declaration.
	makeGoFile := func(name string, size int) string {
		header := "package a\n\nfunc F() {}\n"
		padding := ""
		if size > len(header) {
			padding = "//" + strings.Repeat("x", size-len(header)-2)
		}
		content := header + padding
		// Adjust to exact size.
		if len(content) > size {
			content = content[:size]
		}
		for len(content) < size {
			content += " "
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	const limit = 500

	// Exactly MaxFileBytes: should be outlined.
	exactFile := makeGoFile("exact.go", limit)
	f1, err := Outline(ctx, exactFile, nil, Options{MaxFileBytes: limit})
	if err != nil {
		t.Fatalf("exact size: unexpected error: %v", err)
	}
	if f1.Backend != BackendGoAST {
		t.Fatalf("exact size: Backend = %q, want %q", f1.Backend, BackendGoAST)
	}
	if len(f1.Decls) == 0 {
		t.Fatal("exact size: expected declarations, got none")
	}

	// One byte over MaxFileBytes: should be none.
	overFile := makeGoFile("over.go", limit+1)
	f2, err := Outline(ctx, overFile, nil, Options{MaxFileBytes: limit})
	if err != nil {
		t.Fatalf("over size: unexpected error: %v", err)
	}
	if f2.Backend != BackendNone {
		t.Fatalf("over size: Backend = %q, want %q", f2.Backend, BackendNone)
	}
	if len(f2.Decls) != 0 {
		t.Fatalf("over size: expected 0 decls, got %d", len(f2.Decls))
	}

	// MaxFileBytes 0 means 1 MiB. A file just over 1 MiB should be none.
	overMiB := makeGoFile("over_mib.go", (1<<20)+1)
	f3, err := Outline(ctx, overMiB, nil, Options{MaxFileBytes: 0})
	if err != nil {
		t.Fatalf("over 1MiB: unexpected error: %v", err)
	}
	if f3.Backend != BackendNone {
		t.Fatalf("over 1MiB: Backend = %q, want %q", f3.Backend, BackendNone)
	}
	if len(f3.Decls) != 0 {
		t.Fatalf("over 1MiB: expected 0 decls, got %d", len(f3.Decls))
	}
}

// TS-01-16: A .go file is parsed in-process with Backend go/ast and the Runner is never called.
func TestOutline_GoBackendNoRunner_TS_01_16(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "a.go")
	if err := os.WriteFile(goFile, []byte("package a\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runnerCalls := 0
	ctx := context.Background()
	f, err := Outline(ctx, goFile, nil, Options{
		Runner: func(_ context.Context, _ []string) ([]byte, error) {
			runnerCalls++
			t.Fatal("Runner should not be called for .go files")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Backend != BackendGoAST {
		t.Fatalf("Backend = %q, want %q", f.Backend, BackendGoAST)
	}
	if f.Lang != LangGo {
		t.Fatalf("Lang = %q, want %q", f.Lang, LangGo)
	}
	if runnerCalls != 0 {
		t.Fatalf("Runner was called %d times, want 0", runnerCalls)
	}
}

// TS-01-17: Functions are kind func and methods kind method with the receiver base type as Container.
func TestOutline_GoFuncAndMethod_TS_01_17(t *testing.T) {
	src := []byte(`package a

func F() {}

type T struct{}
func (t *T) M() {}
func (t T) N() {}

type G[K comparable, V any] struct{}
func (g *G[K, V]) P() {}
`)

	ctx := context.Background()
	f, err := Outline(ctx, "/fake/a.go", src, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dm := declMap(f.Decls)

	// F is a function.
	assertDecl(t, dm, "F", KindFunc, "")
	// M is a method with Container T.
	assertDecl(t, dm, "M", KindMethod, "T")
	// N is a method with Container T.
	assertDecl(t, dm, "N", KindMethod, "T")
	// P is a method with Container G (no * and no type parameters).
	assertDecl(t, dm, "P", KindMethod, "G")
}

// TS-01-18: Type, const and var specs map to interface, type, const and var,
// one per name, skipping blank.
func TestOutline_GoTypeConstVar_TS_01_18(t *testing.T) {
	src := []byte(`package a

type I interface{ M() }
type S struct{ X int }
type D int
type A = []byte

const (
	A1 = 1
	B1 = 2
	_  = 3
)

var x, y int
`)

	ctx := context.Background()
	f, err := Outline(ctx, "/fake/a.go", src, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dm := declMap(f.Decls)

	// Interface
	assertDecl(t, dm, "I", KindInterface, "")
	// Struct, defined type, alias → all kind type
	assertDecl(t, dm, "S", KindType, "")
	assertDecl(t, dm, "D", KindType, "")
	assertDecl(t, dm, "A", KindType, "")
	// Const
	assertDecl(t, dm, "A1", KindConst, "")
	assertDecl(t, dm, "B1", KindConst, "")
	// Var
	assertDecl(t, dm, "x", KindVar, "")
	assertDecl(t, dm, "y", KindVar, "")
	// No blank identifier
	for _, d := range f.Decls {
		if d.Name == "_" {
			t.Fatal("blank identifier _ should be skipped")
		}
	}
}

// TS-01-19: Go Decl ranges equal the go/ast node positions and exclude doc comments.
func TestOutline_GoRanges_TS_01_19(t *testing.T) {
	sources := []struct {
		name string
		src  string
	}{
		{
			name: "basic",
			src: `package a

// Doc comment for F.
func F() {
	x := 1
	_ = x
}

// Doc comment for T.
type T struct {
	X int
}

const C = 42

var V int
`,
		},
		{
			name: "grouped",
			src: `package a

const (
	// Doc for A.
	A = 1
	B = 2
)

var (
	// Doc for X.
	X int
	Y string
)

type (
	// Doc for I.
	I interface{ M() }
	S struct{ F int }
)
`,
		},
		{
			name: "method_with_doc",
			src: `package a

type R struct{}

// Doc for M.
func (r *R) M() int {
	return 0
}
`,
		},
	}

	for _, tc := range sources {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f, err := Outline(ctx, "/fake/"+tc.name+".go", []byte(tc.src), Options{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Parse independently with go/ast.
			want := referenceDecls(t, tc.name+".go", tc.src)

			// Compare.
			if len(f.Decls) != len(want) {
				t.Fatalf("got %d decls, want %d\ngot:  %v\nwant: %v", len(f.Decls), len(want), f.Decls, want)
			}
			for i, d := range f.Decls {
				w := want[i]
				if d.Name != w.Name {
					t.Errorf("decl %d: Name = %q, want %q", i, d.Name, w.Name)
				}
				if d.StartLine != w.StartLine {
					t.Errorf("decl %q: StartLine = %d, want %d", d.Name, d.StartLine, w.StartLine)
				}
				if d.EndLine != w.EndLine {
					t.Errorf("decl %q: EndLine = %d, want %d", d.Name, d.EndLine, w.EndLine)
				}
			}

			// Verify doc comments are excluded: no decl starts at a doc comment line.
			lines := strings.Split(tc.src, "\n")
			for _, d := range f.Decls {
				if d.StartLine > 0 && d.StartLine <= len(lines) {
					line := lines[d.StartLine-1]
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") {
						t.Errorf("decl %q StartLine %d is a doc comment line: %q", d.Name, d.StartLine, line)
					}
				}
			}
		})
	}
}

// TS-01-20: Exported equals ast.IsExported(Name) for every Go Decl.
func TestOutline_GoExported_TS_01_20(t *testing.T) {
	src := []byte(`package a

func Exported() {}
func unexported() {}
type PublicType struct{}
type privateType struct{}
const PublicConst = 1
const privateConst = 2
var PublicVar int
var privateVar int

// Unicode upper-case.
func Ωmega() {}
// Unicode lower-case.
func αlpha() {}
`)

	ctx := context.Background()
	f, err := Outline(ctx, "/fake/a.go", src, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, d := range f.Decls {
		want := ast.IsExported(d.Name)
		if d.Exported != want {
			t.Errorf("decl %q: Exported = %v, want %v (ast.IsExported)", d.Name, d.Exported, want)
		}
	}
}

// TS-01-21: Go signatures follow the specified one-line forms.
func TestOutline_GoSignatures_TS_01_21(t *testing.T) {
	src := []byte(`package a

func Simple() {}

func MultiLine(
	a int,
	b string,
) error {
	return nil
}

type S struct {
	X int
}

type I interface {
	M()
}

type M map[string]int

type A = []byte

const C = 42

var V int

var W interface{}
`)

	ctx := context.Background()
	f, err := Outline(ctx, "/fake/a.go", src, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dm := declMap(f.Decls)

	assertSig(t, dm, "Simple", "func Simple()")
	// MultiLine should be on one line, without the body.
	sig := dm["MultiLine"].Signature
	if strings.Contains(sig, "\n") {
		t.Errorf("MultiLine signature contains newline: %q", sig)
	}
	if strings.Contains(sig, "{") {
		t.Errorf("MultiLine signature contains body: %q", sig)
	}
	if !strings.HasPrefix(sig, "func MultiLine(") {
		t.Errorf("MultiLine signature = %q, want prefix 'func MultiLine('", sig)
	}

	assertSig(t, dm, "S", "type S struct")
	assertSig(t, dm, "I", "type I interface")
	assertSig(t, dm, "M", "type M map[string]int")
	assertSig(t, dm, "A", "type A = []byte")
	assertSig(t, dm, "C", "const C")
	assertSig(t, dm, "V", "var V int")
	assertSig(t, dm, "W", "var W interface{}")
}

// TS-01-22: A syntax-broken Go file still yields recovered declarations with Backend go/ast and no error.
func TestOutline_GoSyntaxError_TS_01_22(t *testing.T) {
	src := []byte(`package a

func A() {}

func broken( {
	// syntax error
}

func B() {}
`)

	ctx := context.Background()
	f, err := Outline(ctx, "/fake/broken.go", src, Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Backend != BackendGoAST {
		t.Fatalf("Backend = %q, want %q", f.Backend, BackendGoAST)
	}

	names := declNames(f.Decls)
	if !sliceContains(names, "A") {
		t.Fatalf("expected A in decls, got %v", names)
	}
	// B may or may not be recovered depending on the parser, but A must be there.
}

// --- helpers ---

func declNames(decls []Decl) []string {
	names := make([]string, len(decls))
	for i, d := range decls {
		names[i] = d.Name
	}
	return names
}

func declMap(decls []Decl) map[string]Decl {
	m := make(map[string]Decl, len(decls))
	for _, d := range decls {
		m[d.Name] = d
	}
	return m
}

func sliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

func assertDecl(t *testing.T, dm map[string]Decl, name string, kind Kind, container string) {
	t.Helper()
	d, ok := dm[name]
	if !ok {
		t.Fatalf("decl %q not found", name)
	}
	if d.Kind != kind {
		t.Errorf("decl %q: Kind = %q, want %q", name, d.Kind, kind)
	}
	if d.Container != container {
		t.Errorf("decl %q: Container = %q, want %q", name, d.Container, container)
	}
}

func assertSig(t *testing.T, dm map[string]Decl, name, wantSig string) {
	t.Helper()
	d, ok := dm[name]
	if !ok {
		t.Fatalf("decl %q not found", name)
	}
	if d.Signature != wantSig {
		t.Errorf("decl %q: Signature = %q, want %q", name, d.Signature, wantSig)
	}
}

// referenceDecls parses Go source independently and returns the expected
// decl names with their line ranges, for comparison with the outline output.
func referenceDecls(t *testing.T, filename, src string) []Decl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil && f == nil {
		t.Fatalf("reference parse failed: %v", err)
	}

	var decls []Decl
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if name == "_" {
				continue
			}
			kind := KindFunc
			container := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				kind = KindMethod
				container = receiverBaseName(d.Recv.List[0].Type)
			}
			startLine := fset.Position(d.Pos()).Line
			endLine := fset.Position(d.End()).Line
			decls = append(decls, Decl{
				Kind:      kind,
				Name:      name,
				Container: container,
				StartLine: startLine,
				EndLine:   endLine,
			})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					name := spec.Name.Name
					if name == "_" {
						continue
					}
					kind := KindType
					if _, ok := spec.Type.(*ast.InterfaceType); ok {
						kind = KindInterface
					}
					var startLine, endLine int
					if d.Lparen.IsValid() {
						// Grouped: use spec range.
						startLine = fset.Position(spec.Pos()).Line
						endLine = fset.Position(spec.End()).Line
					} else {
						// Ungrouped: use GenDecl range.
						startLine = fset.Position(d.Pos()).Line
						endLine = fset.Position(d.End()).Line
					}
					decls = append(decls, Decl{
						Kind:      kind,
						Name:      name,
						StartLine: startLine,
						EndLine:   endLine,
					})
				case *ast.ValueSpec:
					tok := KindConst
					if d.Tok == token.VAR {
						tok = KindVar
					}
					for _, ident := range spec.Names {
						if ident.Name == "_" {
							continue
						}
						var startLine, endLine int
						if d.Lparen.IsValid() {
							startLine = fset.Position(spec.Pos()).Line
							endLine = fset.Position(spec.End()).Line
						} else {
							startLine = fset.Position(d.Pos()).Line
							endLine = fset.Position(d.End()).Line
						}
						decls = append(decls, Decl{
							Kind:      tok,
							Name:      ident.Name,
							StartLine: startLine,
							EndLine:   endLine,
						})
					}
				}
			}
		}
	}
	return decls
}

// receiverBaseName extracts the base type name from a receiver expression,
// unwrapping *, (), IndexExpr and IndexListExpr.
func receiverBaseName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
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
