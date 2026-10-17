package policy

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// listenAPIs are the socket-listening functions, by import path.
var listenAPIs = map[string]func(name string) bool{
	"net": func(n string) bool {
		switch n {
		case "Listen", "ListenTCP", "ListenUnix", "ListenPacket", "ListenConfig":
			return true
		}
		return false
	},
	"crypto/tls": func(n string) bool { return n == "Listen" },
	"net/http":   func(n string) bool { return strings.HasPrefix(n, "ListenAndServe") },
}

// moduleFiles is every non-test Go file of the module rooted at dir:
// nested modules, testdata, build output and dot-directories are skipped.
func moduleFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == dir {
				return nil
			}
			if n := d.Name(); n == "testdata" || n == "build" || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// noListen fails for every non-test Go file of the root module and the
// sandbox module under root that refers to a socket-listening API. The nested
// modules that may listen, sandbox/gatewayd and mcpserve, are exempt: the walk
// skips every directory that has its own go.mod.
func noListen(root string) error {
	var errs []error
	for _, mod := range []string{root, filepath.Join(root, "sandbox")} {
		if _, err := os.Stat(filepath.Join(mod, "go.mod")); err != nil {
			continue
		}
		files, err := moduleFiles(mod)
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := listenUses(f); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// listenUses reports every selector in file that names a listening API
// through the file's own import names, aliases included.
func listenUses(file string) error {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	names := map[string]string{} // local name -> import path
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if _, ok := listenAPIs[p]; !ok {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		names[name] = p
	}
	var errs []error
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if p, ok := names[id.Name]; ok && listenAPIs[p](sel.Sel.Name) {
			errs = append(errs, fmt.Errorf("%s: %s.%s listens on a socket; only the nested modules (sandbox/gatewayd, mcpserve) may", fset.Position(sel.Pos()), p, sel.Sel.Name))
		}
		return true
	})
	return errors.Join(errs...)
}

// TS-19-63: the no-listen check fails on any socket-listening API in
// non-test files of the root or sandbox module and exempts only the nested
// modules (sandbox/gatewayd, mcpserve). 19-REQ-9.4, 23-REQ-9.3 (TS-23-51).
func TestNoListen_TS19_63(t *testing.T) {
	if err := noListen(repoRoot(t)); err != nil {
		t.Fatalf("the delivered tree listens:\n%v", err)
	}
	module := func(t *testing.T, files map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		files["go.mod"] = "module example.test/m\n\ngo 1.27\n"
		files["sandbox/go.mod"] = "module example.test/m/sandbox\n\ngo 1.27\n"
		files["sandbox/gatewayd/go.mod"] = "module example.test/m/sandbox/gatewayd\n\ngo 1.27\n"
		files["mcpserve/go.mod"] = "module example.test/m/mcpserve\n\ngo 1.27\n"
		for name, body := range files {
			p := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	use := func(imp, call string) string {
		return "package x\n\nimport " + imp + "\n\nvar _ = " + call + "\n"
	}
	for _, c := range []struct{ file, imp, call, api string }{
		{"x.go", `"net"`, "net.Listen", "net.Listen"},
		{"x.go", `"net"`, "net.ListenTCP", "net.ListenTCP"},
		{"x.go", `"net"`, "net.ListenUnix", "net.ListenUnix"},
		{"x.go", `"net"`, "net.ListenPacket", "net.ListenPacket"},
		{"x.go", `"net"`, "net.ListenConfig{}", "net.ListenConfig"},
		{"sandbox/x.go", `"crypto/tls"`, "tls.Listen", "crypto/tls.Listen"},
		{"sandbox/x.go", `"net/http"`, "http.ListenAndServe", "net/http.ListenAndServe"},
		{"x.go", `"net/http"`, "http.ListenAndServeTLS", "net/http.ListenAndServeTLS"},
		{"x.go", `n "net"`, "n.Listen", "net.Listen"},
	} {
		err := noListen(module(t, map[string]string{c.file: use(c.imp, c.call)}))
		if err == nil || !strings.Contains(err.Error(), "x.go") || !strings.Contains(err.Error(), c.api) {
			t.Errorf("%s in %s: %v, want a failure naming the file and %s", c.call, c.file, err, c.api)
		}
	}
	if err := noListen(module(t, map[string]string{"x_test.go": use(`"net"`, "net.Listen")})); err != nil {
		t.Errorf("a _test.go use failed the check: %v", err)
	}
	if err := noListen(module(t, map[string]string{"sandbox/gatewayd/x.go": use(`"net"`, "net.Listen")})); err != nil {
		t.Errorf("a gatewayd use failed the check: %v", err)
	}
	if err := noListen(module(t, map[string]string{"mcpserve/x.go": use(`"net"`, "net.Listen")})); err != nil {
		t.Errorf("an mcpserve use failed the check: %v", err)
	}
	err := noListen(module(t, map[string]string{"x.go": use(`"net"`, "net.Listen")}))
	if err == nil || !strings.Contains(err.Error(), "sandbox/gatewayd") || !strings.Contains(err.Error(), "mcpserve") {
		t.Errorf("a listener in the root: %v, want a failure naming both nested modules that may listen", err)
	}
}
