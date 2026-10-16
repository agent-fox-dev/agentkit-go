package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// stringLiterals returns the string literals among args, and "?" for any
// argument that is not one.
func stringLiterals(args []ast.Expr) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = "?"
		if lit, ok := a.(*ast.BasicLit); ok {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out[i] = s
			}
		}
	}
	return out
}

// isSelector reports whether e is the selector pkg.name.
func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

// TS-22-27: main.go picks its tools with core.Named and scripts its model with
// the faux helpers, so it holds none of the wiring they replace: no faux.Turn
// literal, no guard.Restricted call, no switch over a tool's Name, and no turn
// function, and its one core.Named call names list_files, find_files and
// read_file, in that order. 22-REQ-6.2.
func TestExampleUsesTheHelpers_TS22_27(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	var namedArgs []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if isSelector(n.Type, "faux", "Turn") {
				t.Error("main.go has a faux.Turn composite literal")
			}
		case *ast.CallExpr:
			if isSelector(n.Fun, "guard", "Restricted") {
				t.Error("main.go calls guard.Restricted")
			}
			if isSelector(n.Fun, "core", "Named") {
				named++
				if len(n.Args) > 0 {
					namedArgs = stringLiterals(n.Args[1:])
				}
			}
		case *ast.SwitchStmt:
			if sel, ok := n.Tag.(*ast.SelectorExpr); ok && sel.Sel.Name == "Name" {
				t.Error("main.go switches on a tool's .Name")
			}
		case *ast.FuncDecl:
			if n.Name.Name == "turn" {
				t.Error("main.go declares a turn function")
			}
		}
		return true
	})
	if named != 1 {
		t.Errorf("main.go calls core.Named %d times, want 1", named)
	}
	if want := []string{"list_files", "find_files", "read_file"}; !slices.Equal(namedArgs, want) {
		t.Errorf("core.Named picks %q, want %q", namedArgs, want)
	}
}
