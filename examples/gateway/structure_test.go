package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// selectorIs reports whether sel is pkg.name.
func selectorIs(sel *ast.SelectorExpr, pkg, name string) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

// TS-22-30: main.go opens its sandbox with one sandbox.Open, picks its proxies
// with one core.Named and guards the shell with one guard.Allow("ls"), so it
// holds none of the wiring they replace: no Create, Sync or Tools method call,
// no faux.Turn composite literal, no guard.Restricted call and no switch over a
// tool's Name. Its core.Named call picks read_file, search_files and execute,
// in that order. 22-REQ-7.3.
func TestExampleUsesTheHelpers_TS22_30(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	var namedArgs []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if sel, ok := n.Type.(*ast.SelectorExpr); ok && selectorIs(sel, "faux", "Turn") {
				t.Error("main.go has a faux.Turn composite literal")
			}
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch {
			case selectorIs(sel, "sandbox", "Open"):
				calls["sandbox.Open"]++
			case selectorIs(sel, "core", "Named"):
				calls["core.Named"]++
				if len(n.Args) > 0 {
					namedArgs = stringLiterals(n.Args[1:])
				}
			case selectorIs(sel, "guard", "Allow"):
				if len(n.Args) == 1 {
					if lit, ok := n.Args[0].(*ast.BasicLit); ok {
						if s, err := strconv.Unquote(lit.Value); err == nil && s == "ls" {
							calls[`guard.Allow("ls")`]++
						}
					}
				}
			case selectorIs(sel, "guard", "Restricted"):
				t.Error("main.go calls guard.Restricted")
			case sel.Sel.Name == "Create" || sel.Sel.Name == "Sync" || sel.Sel.Name == "Tools":
				t.Errorf("main.go calls the %s method that sandbox.Open replaces", sel.Sel.Name)
			}
		case *ast.SwitchStmt:
			if sel, ok := n.Tag.(*ast.SelectorExpr); ok && sel.Sel.Name == "Name" {
				t.Error("main.go switches on a tool's .Name")
			}
		}
		return true
	})
	for _, c := range []string{"sandbox.Open", "core.Named", `guard.Allow("ls")`} {
		if calls[c] != 1 {
			t.Errorf("main.go has %d %s calls, want 1", calls[c], c)
		}
	}
	if want := []string{"read_file", "search_files", "execute"}; !slices.Equal(namedArgs, want) {
		t.Errorf("core.Named picks %q, want %q", namedArgs, want)
	}
}

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
