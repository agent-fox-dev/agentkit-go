package buildver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// TS-16-2: buildver gains only an unexported, uninitialised string stamp
// that no source assigns, and its exported API is unchanged. 16-REQ-4.1.
// Build, which reports the link-time commit and build time, is the one
// addition (docs/adr/05-keep-the-sandbox-in-its-own-module-and-sync-through-a-filtered-view.md).
func TestStampAPI_TS16_2(t *testing.T) {
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var exported []string
	stamps := 0
	fset := token.NewFileSet()
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && d.Name.IsExported() {
					exported = append(exported, d.Name.Name)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.ValueSpec:
						for i, n := range s.Names {
							if n.IsExported() {
								exported = append(exported, n.Name)
							}
							if n.Name == "stamp" && d.Tok == token.VAR {
								stamps++
								id, ok := s.Type.(*ast.Ident)
								if !ok || id.Name != "string" || len(s.Values) > i {
									t.Errorf("stamp is not declared as `var stamp string` with no value")
								}
							}
						}
					case *ast.TypeSpec:
						if s.Name.IsExported() {
							exported = append(exported, s.Name.Name)
						}
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			var lhs []ast.Expr
			switch x := n.(type) {
			case *ast.AssignStmt:
				lhs = x.Lhs
			case *ast.IncDecStmt:
				lhs = []ast.Expr{x.X}
			}
			for _, l := range lhs {
				if id, ok := l.(*ast.Ident); ok && id.Name == "stamp" {
					t.Errorf("%s assigns stamp; it is set only through -ldflags -X", fset.Position(id.Pos()))
				}
			}
			return true
		})
	}
	if !slices.Equal(exported, []string{"Version", "Build"}) {
		t.Errorf("exported identifiers %v, want exactly [Version Build]", exported)
	}
	if stamps != 1 {
		t.Errorf("%d package-level vars named stamp, want 1", stamps)
	}
}
