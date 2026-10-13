package confine

import (
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// fileConstraint is the //go:build expression of the Go file at path, or
// nil when it has none.
func fileConstraint(t *testing.T, path string) constraint.Expr {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				e, err := constraint.Parse(c.Text)
				if err != nil {
					t.Fatal(err)
				}
				return e
			}
		}
	}
	return nil
}

func satisfiedFor(e constraint.Expr, goos string) bool {
	return e == nil || e.Eval(func(tag string) bool { return tag == goos || (tag == "unix" && goos != "windows") })
}

// TS-15-13 (constraint half): the stub that applies nothing off Linux is
// built for darwin and windows and not linux, and imports no go-landlock
// package. 15-REQ-2.7. Its behaviour is checked in setup_other_test.go.
func TestStubConstraint_TS15_13(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		landlock := false
		for _, imp := range f.Imports {
			if strings.HasPrefix(strings.Trim(imp.Path.Value, `"`), "github.com/landlock-lsm/go-landlock") {
				landlock = true
			}
		}
		c := fileConstraint(t, name)
		if landlock && (satisfiedFor(c, "darwin") || satisfiedFor(c, "windows") || !satisfiedFor(c, "linux")) {
			t.Errorf("%s imports go-landlock but is not Linux-only", name)
		}
		if name == "setup_other.go" {
			found = true
			if landlock || satisfiedFor(c, "linux") || !satisfiedFor(c, "darwin") || !satisfiedFor(c, "windows") {
				t.Errorf("setup_other.go: constraint %v, go-landlock %v", c, landlock)
			}
		}
	}
	if !found {
		t.Fatal("no setup_other.go stub")
	}
}
