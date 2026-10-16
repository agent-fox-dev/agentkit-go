package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TS-20-6: Workspace has no exported fields and Options has exactly one field,
// Ignore.
func TestWorkspaceAndOptionsShape_TS20_6(t *testing.T) {
	wt := reflect.TypeFor[Workspace]()
	for i := 0; i < wt.NumField(); i++ {
		if wt.Field(i).IsExported() {
			t.Errorf("Workspace has the exported field %s", wt.Field(i).Name)
		}
	}
	ot := reflect.TypeFor[Options]()
	if ot.NumField() != 1 || ot.Field(0).Name != "Ignore" || ot.Field(0).Type != reflect.TypeFor[IgnoreOptions]() {
		t.Errorf("Options = %v, want exactly Ignore IgnoreOptions", ot)
	}

	ws, err := Open(t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Open with the zero Options: %v", err)
	}
	if ws == nil {
		t.Fatal("Open with the zero Options returned no workspace")
	}
}

const doublestarPath = "github.com/bmatcuk/doublestar/v4"

// TS-20-49: workspace's non-test files import only the standard library and
// doublestar, and carry private copies of the glob wrapper and the hidden-name
// test.
func TestImportsOnlyTheStandardLibraryAndDoublestar_TS20_49(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var funcs []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			// A standard-library path has no dot in its first element.
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") && p != doublestarPath {
				t.Errorf("%s imports %s: workspace may import only the standard library and %s", name, p, doublestarPath)
			}
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				funcs = append(funcs, fd.Name.Name)
			}
		}
	}
	for _, want := range []string{"globMatch", "isHidden"} {
		if !slices.Contains(funcs, want) {
			t.Errorf("workspace declares no private %s", want)
		}
	}
}
