package tools

import (
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
)

// TS-05-13 (unit): Go type resolver parses workspace packages and type-checks with workspace-local importer without external processes
// Verifies: 05-REQ-3.1
func TestGoTypeResolver_WorkspaceImporter_TS_05_13(t *testing.T) {
	dir := t.TempDir()

	// Root go.mod defining module 'example.com/mod'
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/mod\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Subpackage pkg/a
	pkgADir := filepath.Join(dir, "pkg", "a")
	if err := os.MkdirAll(pkgADir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcA := `package a

type Value struct {
	Count int
}

func NewValue() Value {
	return Value{Count: 42}
}
`
	if err := os.WriteFile(filepath.Join(pkgADir, "a.go"), []byte(srcA), 0o600); err != nil {
		t.Fatal(err)
	}

	// Subpackage pkg/b importing example.com/mod/pkg/a
	pkgBDir := filepath.Join(dir, "pkg", "b")
	if err := os.MkdirAll(pkgBDir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcB := `package b

import "example.com/mod/pkg/a"

func Compute() int {
	v := a.NewValue()
	return v.Count
}
`
	if err := os.WriteFile(filepath.Join(pkgBDir, "b.go"), []byte(srcB), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	imp := newWorkspaceImporter(ws, "example.com/mod")
	pkg, err := checkPackage(imp, "pkg/b")
	if err != nil {
		t.Fatalf("checkPackage failed: %v", err)
	}
	if pkg == nil {
		t.Fatal("expected non-nil package for pkg/b")
	}

	if len(pkg.Imports()) == 0 {
		t.Fatal("expected at least 1 import in pkg/b")
	}
	if pkg.Imports()[0].Path() != "example.com/mod/pkg/a" {
		t.Fatalf("expected import path 'example.com/mod/pkg/a', got %q", pkg.Imports()[0].Path())
	}

	// Check that types from pkg/a are fully resolved in pkg/b
	pkgA := pkg.Imports()[0]
	if pkgA.Scope().Lookup("Value") == nil {
		t.Fatal("expected Value type to be resolved in pkg/a")
	}
	if pkgA.Scope().Lookup("NewValue") == nil {
		t.Fatal("expected NewValue func to be resolved in pkg/a")
	}
}

// TS-05-14 (unit): Go type resolver stubs external imports with empty synthetic packages and FakeImportC without aborting
// Verifies: 05-REQ-3.2
func TestGoTypeResolver_SyntheticExternalStubs_TS_05_14(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	src := `package main

import (
	"C"
	"fmt"
	"os"
	"github.com/stretchr/testify/assert"
)

func main() {
	fmt.Println("hello")
	_ = os.Args
	_ = assert.True
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	imp := newWorkspaceImporter(ws, "example.com/test")
	if !makeTypesConfig(imp).FakeImportC {
		t.Fatal("expected cfg.FakeImportC to be true")
	}

	pkg, err := checkPackage(imp, "")
	if err != nil || pkg == nil {
		t.Fatalf("expected a package despite errors, got %v, %v", pkg, err)
	}

	// Check synthetic empty packages
	if imp.imported["fmt"] == nil || imp.imported["fmt"].Scope().Len() != 0 {
		t.Fatalf("expected synthetic empty package for 'fmt', got scope len=%d", imp.imported["fmt"].Scope().Len())
	}
	if imp.imported["os"] == nil || imp.imported["os"].Scope().Len() != 0 {
		t.Fatalf("expected synthetic empty package for 'os', got scope len=%d", imp.imported["os"].Scope().Len())
	}
	if imp.imported["github.com/stretchr/testify/assert"] == nil || imp.imported["github.com/stretchr/testify/assert"].Scope().Len() != 0 {
		t.Fatalf("expected synthetic empty package for assert, got scope len=%d", imp.imported["github.com/stretchr/testify/assert"].Scope().Len())
	}

	// Type checking completed without panicking and collected type errors
	if len(imp.errors) == 0 {
		t.Fatal("expected collected type errors for undefined identifiers on synthetic packages")
	}
}

// TS-05-15 (unit): Go type resolver attributes resolved confidence to receiver method calls, interfaces, renamed imports, and promoted fields
// Verifies: 05-REQ-3.3
func TestGoTypeResolver_ResolvedConfidence_TS_05_15(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/resolved\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// pkg/a defining Do
	pkgADir := filepath.Join(dir, "pkg", "a")
	if err := os.MkdirAll(pkgADir, 0o750); err != nil {
		t.Fatal(err)
	}
	srcA := `package a

func Do() {}
`
	if err := os.WriteFile(filepath.Join(pkgADir, "a.go"), []byte(srcA), 0o600); err != nil {
		t.Fatal(err)
	}

	// main package with Reader interface, Buffer struct, Base struct, and renamed import
	srcMain := `package main

import alias "example.com/resolved/pkg/a"

type Reader interface {
	Read(p []byte) (n int, err error)
}

type Base struct{}

func (Base) Ping() {}

type Buffer struct {
	Base
}

func (Buffer) Read(p []byte) (n int, err error) {
	return 0, nil
}

func Run() {
	var buf Buffer
	buf.Ping()

	var r Reader = buf
	_, _ = r.Read(nil)

	alias.Do()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(srcMain), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	// 1. Target Ping: receiver method call through embedded Base
	targetPing := outline.Decl{
		Kind:      outline.KindMethod,
		Name:      "Ping",
		Container: "Base",
	}
	sitesPing := resolveGoReferences(loadGoWorkspace(ws), targetPing)
	if len(sitesPing) == 0 {
		t.Fatal("expected at least 1 reference site for Ping")
	}
	for _, s := range sitesPing {
		if s.Confidence != "resolved" {
			t.Fatalf("expected confidence 'resolved' for Ping, got %q", s.Confidence)
		}
	}

	// 2. Target Read: interface method invocation r.Read()
	targetRead := outline.Decl{
		Kind:      outline.KindMethod,
		Name:      "Read",
		Container: "Reader",
	}
	sitesRead := resolveGoReferences(loadGoWorkspace(ws), targetRead)
	if len(sitesRead) == 0 {
		t.Fatal("expected at least 1 reference site for Read")
	}
	for _, s := range sitesRead {
		if s.Confidence != "resolved" {
			t.Fatalf("expected confidence 'resolved' for Read, got %q", s.Confidence)
		}
	}

	// 3. Target Do: renamed import alias.Do()
	targetDo := outline.Decl{
		Kind: outline.KindFunc,
		Name: "Do",
	}
	sitesDo := resolveGoReferences(loadGoWorkspace(ws), targetDo)
	if len(sitesDo) == 0 {
		t.Fatal("expected at least 1 reference site for Do")
	}
	for _, s := range sitesDo {
		if s.Confidence != "resolved" {
			t.Fatalf("expected confidence 'resolved' for Do, got %q", s.Confidence)
		}
	}
}

// TS-05-16 (unit): Go type resolver excludes same-named methods on unrelated types or distinct packages
// Verifies: 05-REQ-3.4
func TestGoTypeResolver_ExcludeUnrelatedTypes_TS_05_16(t *testing.T) {
	dir := t.TempDir()

	srcAlpha := `package main

type Alpha struct{}

func (Alpha) Close() {}
`
	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte(srcAlpha), 0o600); err != nil {
		t.Fatal(err)
	}

	srcBeta := `package main

type Beta struct{}

func (Beta) Close() {}
`
	if err := os.WriteFile(filepath.Join(dir, "beta.go"), []byte(srcBeta), 0o600); err != nil {
		t.Fatal(err)
	}

	srcCaller := `package main

func Use() {
	var a Alpha
	a.Close()

	var b Beta
	b.Close()
}
`
	if err := os.WriteFile(filepath.Join(dir, "alpha_caller.go"), []byte(srcCaller), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	alphaClose := outline.Decl{
		Kind:      outline.KindMethod,
		Name:      "Close",
		Container: "Alpha",
	}
	sites := resolveGoReferences(loadGoWorkspace(ws), alphaClose)
	if len(sites) == 0 {
		t.Fatal("expected at least 1 reference for Alpha.Close")
	}

	for _, s := range sites {
		if s.Path != "alpha_caller.go" {
			t.Fatalf("expected reference site path 'alpha_caller.go', got %q", s.Path)
		}
		if strings.Contains(s.Source, "beta.Close") || strings.Contains(s.Source, "b.Close") {
			t.Fatalf("unexpected Beta.Close reference found in sites: %s", s.Source)
		}
	}
}

// TS-05-17 (unit): Go type resolver demotes incompletely typed identifiers to lexical confidence instead of dropping them
// Verifies: 05-REQ-3.5
func TestGoTypeResolver_DemoteIncompleteTypes_TS_05_17(t *testing.T) {
	dir := t.TempDir()

	srcTarget := `package main

type Handler struct{}

func (Handler) Do() {}
`
	if err := os.WriteFile(filepath.Join(dir, "handler.go"), []byte(srcTarget), 0o600); err != nil {
		t.Fatal(err)
	}

	srcCaller := `package main

import "external"

func Call(client external.Client) {
	client.Do()
}
`
	if err := os.WriteFile(filepath.Join(dir, "caller.go"), []byte(srcCaller), 0o600); err != nil {
		t.Fatal(err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	targetDo := outline.Decl{
		Kind:      outline.KindMethod,
		Name:      "Do",
		Container: "Handler",
	}
	sites := resolveGoReferences(loadGoWorkspace(ws), targetDo)
	var hit *ReferenceSite
	for i := range sites {
		if strings.Contains(sites[i].Source, "client.Do()") {
			hit = &sites[i]
		}
	}
	if hit == nil {
		t.Fatal("expected call site 'client.Do()' to be found")
	}
	if hit.Confidence != "lexical" {
		t.Fatalf("expected confidence 'lexical' for client.Do(), got %q", hit.Confidence)
	}
}

// TS-05-18 (property): Go source parsing preserves comments and records 1-based line and column offsets via token.FileSet
// Verifies: 05-REQ-3.6
func TestGoTypeResolver_CommentsAndPositions_TS_05_18(t *testing.T) {
	// Generate multiple Go packages with comments and expressions
	genGoPackages := func() []string {
		var pkgDirs []string
		snippets := []string{
			`package sample1
// Leading comment on type
type Item struct {
	// Field comment
	ID int
}
// Function comment
func Do(x int) int {
	/* inline comment */
	return x + 1
}
`,
			`package sample2
// Another package comment
const (
	// Const A
	Alpha = 1
	// Const B
	Beta = 2
)
func Compute() {
	// inside compute
	println("done")
}
`,
		}

		for i, snip := range snippets {
			dir := t.TempDir()
			filePath := filepath.Join(dir, fmt.Sprintf("sample_%d.go", i))
			if err := os.WriteFile(filePath, []byte(snip), 0o600); err != nil {
				t.Fatal(err)
			}
			pkgDirs = append(pkgDirs, dir)
		}
		return pkgDirs
	}

	for _, pkgDir := range genGoPackages() {
		ws, err := NewWorkspace(pkgDir)
		if err != nil {
			t.Fatal(err)
		}
		imp := newWorkspaceImporter(ws, "")
		fset := imp.fset
		files, err := imp.parseDirFiles(pkgDir, false)
		if err != nil || len(imp.errors) > 0 {
			t.Fatalf("parseDirFiles failed: %v %v", err, imp.errors)
		}
		if len(files) == 0 {
			t.Fatal("expected at least 1 parsed file")
		}

		for _, f := range files {
			if len(f.Comments) == 0 {
				t.Fatal("expected comments to be preserved in parsed ast.File")
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					p := fset.Position(id.Pos())
					if p.Line < 1 || p.Column < 1 {
						t.Errorf("identifier %s position line=%d column=%d not 1-based", id.Name, p.Line, p.Column)
					}
				}
				return true
			})
		}
	}
}
