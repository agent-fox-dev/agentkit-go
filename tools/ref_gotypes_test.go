package tools

import (
	"context"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

func TestTheWorkspaceImporterChecksWorkspacePackagesAndStubsTheRest(t *testing.T) {
	ws := newWS(t, map[string]string{
		"go.mod":     "module example.com/mod\n\ngo 1.22\n",
		"pkg/a/a.go": "package a\n\ntype Value struct{ Count int }\n\nfunc NewValue() Value { return Value{Count: 42} }\n",
		"pkg/b/b.go": "package b\n\nimport (\n\t\"C\"\n\t\"fmt\"\n\t\"example.com/mod/pkg/a\"\n\t\"github.com/x/assert\"\n)\n\n" +
			"func Compute() int {\n\tfmt.Println(assert.True)\n\treturn a.NewValue().Count\n}\n",
		"pkg/b/b_test.go": "package b\n\nfunc TestCompute() { _ = Compute() }\n",
		"pkg/b/broken.go": "package b\n\nfunc Broken( {\n",
	})
	imp := newWorkspaceImporter(ws, "")
	if imp.modulePath != "example.com/mod" {
		t.Fatalf("module path %q, read from go.mod", imp.modulePath)
	}
	pkg, err := checkPackage(imp, "pkg/b")
	if err != nil || pkg == nil {
		t.Fatalf("checkPackage = %v, %v", pkg, err)
	}
	a := imp.imported["example.com/mod/pkg/a"]
	if a == nil || a.Scope().Lookup("Value") == nil || a.Scope().Lookup("NewValue") == nil {
		t.Fatal("the workspace import was not type-checked")
	}
	for _, ext := range []string{"fmt", "github.com/x/assert"} {
		if p := imp.imported[ext]; p == nil || p.Scope().Len() != 0 {
			t.Errorf("%s is not an empty stub", ext)
		}
	}
	// The parse error and the uses of stubbed names are collected, not fatal.
	if len(imp.errors) < 2 {
		t.Errorf("errors = %v", imp.errors)
	}
	if again, _ := checkPackage(imp, "example.com/mod/pkg/b"); again != pkg {
		t.Error("a checked package is checked once")
	}
	if _, err := checkPackage(imp, "missing"); err == nil {
		t.Error("a directory that is not there")
	}
	if _, err := checkPackage(newWorkspaceImporter(newWS(t, map[string]string{"x/readme.md": ""}), "m"), "x"); err == nil {
		t.Error("a directory with no Go files")
	}
	if !makeTypesConfig(imp).FakeImportC {
		t.Error("cgo's C import must not stop a check")
	}
	if p, _ := imp.Import("C"); p == nil {
		t.Error("an unresolvable import is a stub")
	}
}

func TestResolvedReferencesAreTheTargetsAndNoOthers(t *testing.T) {
	ws := newWS(t, map[string]string{
		"go.mod": "module example.com/x\n\ngo 1.22\n",
		"main.go": "package main\n\nimport alias \"example.com/x/a\"\n\ntype Reader interface{ Read(p []byte) (int, error) }\n\n" +
			"type Base struct{}\n\nfunc (Base) Ping() {}\n\ntype Buffer struct{ Base }\n\nfunc (Buffer) Read(p []byte) (int, error) { return 0, nil }\n\n" +
			"func Run() {\n\tvar buf Buffer\n\tbuf.Ping()\n\tvar r Reader = buf\n\t_, _ = r.Read(nil)\n\talias.Do()\n}\n",
		"a/a.go": "package a\n\ntype Alpha struct{}\n\nfunc (Alpha) Close() {}\n\nfunc (*Alpha) Open() {}\n\nfunc Do() {}\n\nfunc Run() {}\n",
		"b/b.go": "package b\n\nimport (\n\t\"example.com/x/a\"\n\t\"external\"\n)\n\ntype Beta struct{}\n\nfunc (Beta) Close() {}\n\n" +
			"func Run() {}\n\nfunc use(c external.Client) {\n\ta.Run()\n\tRun()\n\tClose := 3\n\t_ = Close\n\ta.Alpha{}.Close()\n\tBeta{}.Close()\n\tc.Close()\n\t(&a.Alpha{}).Open()\n}\n",
	})
	imp := loadGoWorkspace(context.Background(), ws, nil)
	for _, c := range []struct {
		name   string
		target outline.Decl
		path   string
		want   []string
	}{
		{"an embedded method", outline.Decl{Name: "Ping", Container: "Base"}, "", []string{"main.go:17:6 resolved"}},
		{"an interface method", outline.Decl{Name: "Read", Container: "Reader"}, "", []string{"main.go:19:11 resolved"}},
		{"through an import alias", outline.Decl{Name: "Do"}, "a/a.go", []string{"main.go:20:8 resolved"}},
		// Another package's Run, a local variable called Close and Beta's
		// Close are other objects; a call on a stubbed type cannot be
		// confirmed, so it is kept as lexical rather than dropped.
		{"only this package's Run", outline.Decl{Name: "Run", StartLine: 11}, "a/a.go", []string{"b/b.go:15:4 resolved"}},
		{"only Alpha's Close", outline.Decl{Name: "Close", Container: "Alpha", StartLine: 5}, "a/a.go",
			[]string{"b/b.go:19:12 resolved", "b/b.go:21:4 lexical"}},
		// A target whose line moved is found by name in its package scope.
		{"a pointer-receiver method by scope", outline.Decl{Name: "Open", Container: "Alpha", StartLine: 99}, "", []string{"b/b.go:22:15 resolved"}},
		{"an unknown target confirms nothing", outline.Decl{Name: "Close", Container: "Nobody"}, "", []string{
			"b/b.go:18:6 lexical", "b/b.go:19:12 lexical", "b/b.go:20:9 lexical", "b/b.go:21:4 lexical"}},
	} {
		sites := resolveGoReferences(imp, c.target, c.path)
		sortReferenceSites(sites)
		if got := siteList(ReferenceResult{Sites: sites}); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if packages, errs := imp.stats(); packages < 3 || errs == 0 {
		t.Errorf("stats = %d packages, %d errors", packages, errs)
	}
	if !imp.definesName("Ping") || imp.definesName("Nope") {
		t.Error("definesName")
	}
}
