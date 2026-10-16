package agentkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// receiverOf is the type name of a method's receiver, or "" for a function.
func receiverOf(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// calleeName is the name a call expression calls: "core.PrepareArguments" for
// a package function, "callBefore" for a local one, "" for anything else.
func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok {
			return x.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// TS-21-47: the pipeline exists once. *Agent has no executeBatch, and the
// calls that run a tool — core.PrepareArguments and the guard, handler and
// after-interceptor wrappers — are made only by the runner's implementation:
// the batch engine on *Runner and the nested caller.
func TestThePipelineExistsOnce_TS21_47(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	pipeline := map[string]bool{
		"core.PrepareArguments": true, "callBefore": true, "invokeHandler": true, "callAfter": true,
	}
	owners := map[string]bool{"Runner": true, "nestedCaller": true}
	found := map[string]int{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if fn.Name.Name == "executeBatch" && receiverOf(fn) == "Agent" {
				t.Errorf("%s declares (*Agent).executeBatch", path)
			}
			if fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calleeName(call)
				if !pipeline[name] {
					return true
				}
				found[name]++
				if recv := receiverOf(fn); !owners[recv] {
					t.Errorf("%s: %s calls %s from %q; only the runner's implementation may", path, fn.Name.Name, name, recv)
				}
				return true
			})
		}
	}
	for name := range pipeline {
		if found[name] == 0 {
			t.Errorf("no call to %s found: the check would pass vacuously", name)
		}
	}
}
