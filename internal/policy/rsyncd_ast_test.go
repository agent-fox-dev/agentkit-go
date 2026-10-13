package policy

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// rsyncConstructors are the gokrazy/rsync constructors that confine the whole
// process unless given their package's DontRestrict option: the server
// always (14-REQ-7.2), the client as a receiver (D-4).
var rsyncConstructors = []struct{ pkg, fn string }{
	{"github.com/gokrazy/rsync/rsyncd", "NewServer"},
	{"github.com/gokrazy/rsync/rsyncclient", "New"},
}

// rsyncxDir is the one package allowed to construct them.
const rsyncxDir = "sandbox/internal/rsyncx"

// rsyncCall is one constructor call found by checkRsyncCalls.
type rsyncCall struct {
	fn, encl, at string
}

// checkRsyncCalls finds every call of a constructor in f (named rel, a
// slash-separated repository path) and returns them with a violation for
// each one outside rsyncx or without its package's DontRestrict() among
// its arguments. Aliased imports are resolved.
func checkRsyncCalls(fset *token.FileSet, f *ast.File, rel string) (calls []rsyncCall, violations []string) {
	alias := map[string]string{} // local name -> constructor package
	for _, imp := range f.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		for _, c := range rsyncConstructors {
			if c.pkg != p {
				continue
			}
			name := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			alias[name] = p
		}
	}
	if len(alias) == 0 {
		return nil, nil
	}
	isCall := func(e ast.Expr, local, fn string) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != fn {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == local
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for local, pkg := range alias {
				for _, c := range rsyncConstructors {
					if c.pkg != pkg || !isCall(call, local, c.fn) {
						continue
					}
					at := fmt.Sprintf("%s:%d", rel, fset.Position(call.Pos()).Line)
					calls = append(calls, rsyncCall{pkg + "." + c.fn, rel + ":" + fd.Name.Name, at})
					if filepath.ToSlash(filepath.Dir(rel)) != rsyncxDir {
						violations = append(violations, at+": "+c.fn+" outside "+rsyncxDir)
					}
					if !slices.ContainsFunc(call.Args, func(a ast.Expr) bool { return isCall(a, local, "DontRestrict") }) {
						violations = append(violations, at+": "+c.fn+" without "+local+".DontRestrict()")
					}
				}
			}
			return true
		})
	}
	return calls, violations
}

// repoGoFiles returns every non-test Go file of every module in the
// repository, relative and slash-separated; testdata, build output and
// dot-directories are skipped.
func repoGoFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); path != root && (n == "testdata" || n == "build" || strings.HasPrefix(n, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
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

// TS-14-36: rsyncd.NewServer, and rsyncclient.New (D-4), are each called in
// exactly one function, in sandbox/internal/rsyncx, always with DontRestrict().
// 14-REQ-7.2. The tree is walked across modules: rsyncx lives in the nested
// sandbox module (D-2).
func TestRsyncConstructorsDontRestrict_TS14_36(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	encl := map[string]map[string]bool{}
	for _, rel := range repoGoFiles(t, root) {
		f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		calls, violations := checkRsyncCalls(fset, f, rel)
		for _, v := range violations {
			t.Error(v)
		}
		for _, c := range calls {
			if encl[c.fn] == nil {
				encl[c.fn] = map[string]bool{}
			}
			encl[c.fn][c.encl] = true
		}
	}
	for _, c := range rsyncConstructors {
		if fns := encl[c.pkg+"."+c.fn]; len(fns) != 1 {
			t.Errorf("%s.%s is called in %d functions %v, want exactly one", c.pkg, c.fn, len(fns), fns)
		}
	}

	fixtures := []struct{ name, src string }{
		{rsyncxDir + "/nodr.go", "package rsyncx\nimport \"github.com/gokrazy/rsync/rsyncd\"\nfunc f() {\n\trsyncd.NewServer(nil)\n}\n"},
		{"sandbox/other/outside.go", "package other\nimport \"github.com/gokrazy/rsync/rsyncd\"\nfunc f() {\n\trsyncd.NewServer(nil, rsyncd.DontRestrict())\n}\n"},
		{rsyncxDir + "/alias.go", "package rsyncx\nimport rd \"github.com/gokrazy/rsync/rsyncd\"\nfunc f() {\n\trd.NewServer(nil)\n}\n"},
		{rsyncxDir + "/client.go", "package rsyncx\nimport \"github.com/gokrazy/rsync/rsyncclient\"\nfunc f() {\n\trsyncclient.New(nil)\n}\n"},
	}
	for _, fx := range fixtures {
		f, err := parser.ParseFile(fset, fx.name, fx.src, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, v := checkRsyncCalls(fset, f, fx.name)
		if len(v) != 1 || !strings.Contains(v[0], fx.name+":4") {
			t.Errorf("fixture %s: violations %q, want one naming %s:4", fx.name, v, fx.name)
		}
	}
}
