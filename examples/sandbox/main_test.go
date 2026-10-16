package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TS-13-68, as TS-22-28 changes it: the example runs offline against local
// and provider/faux and exits 0, both as a program and in process, and prints
// no "synced" line, because sandbox.Open discards the SyncResult. 13-REQ-10.8,
// 22-REQ-7.1.
func TestExampleRunsOffline_TS13_68(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX ls")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TMPDIR", t.TempDir())
	out, err := exec.Command("go", "run", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Sandbox example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}
	var b strings.Builder
	if err := run(context.Background(), &b, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"read_file src/main.go in the sandbox: ok=true",
		"README: Says hello from inside the sandbox.", "TODOs: 2", "src: main.go, util.go",
		"rm: blocked_by_policy", "exported src: main.go, util.go", "Sandbox example completed successfully"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, b.String())
		}
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, "synced ") {
			t.Errorf("the output has the line %q: the example no longer holds a SyncResult", line)
		}
	}
}

// sandboxLiteral names the sandbox type of a composite literal, or "".
func sandboxLiteral(e ast.Expr) string {
	if cl, ok := e.(*ast.CompositeLit); ok {
		if sel, ok := cl.Type.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "sandbox" {
				return sel.Sel.Name
			}
		}
	}
	return ""
}

// TS-14-45, as TS-22-31 changes it: the example fills the sandbox and exports
// from it with LocalDir (no Tar or TarOut literal in main.go), runs offline,
// and leaves the exported files in its host output directory. The
// sandbox.Open call and the Export call each take exactly one
// sandbox.LocalDir literal. 14-REQ-9.6, 22-REQ-7.4.
func TestExampleUsesLocalDir_TS14_45(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	localDirs := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		if k := sandboxLiteral(asExpr(n)); k == "Tar" || k == "TarOut" {
			t.Errorf("main.go has a sandbox.%s literal", k)
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selectorIs(sel, "sandbox", "Open") || sel.Sel.Name == "Export" {
			for _, a := range call.Args {
				if sandboxLiteral(a) == "LocalDir" {
					localDirs[sel.Sel.Name]++
				}
			}
		}
		return true
	})
	for _, m := range []string{"Open", "Export"} {
		if got := localDirs[m]; got != 1 {
			t.Errorf("%s takes %d sandbox.LocalDir literals, want one", m, got)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX ls")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("TMPDIR", t.TempDir())
	dir := t.TempDir()
	var b strings.Builder
	if err := run(context.Background(), &b, dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"main.go", "util.go"} {
		if _, err := os.Stat(filepath.Join(dir, "out", name)); err != nil {
			t.Errorf("the exported %s is not in the output directory: %v", name, err)
		}
	}
	if !strings.Contains(b.String(), "Sandbox example completed successfully") {
		t.Errorf("output:\n%s", b.String())
	}
}

// TS-22-30: main.go opens its sandbox with one sandbox.Open, picks its proxies
// with one core.Named and guards the shell with one guard.Allow("ls"), so it
// holds none of the wiring they replace: no Create, Sync or Tools method call,
// no faux.Turn literal, no guard.Restricted call and no switch over a tool's
// Name. 22-REQ-7.3.
func TestExampleUsesTheHelpers_TS22_30(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
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
}

// selectorIs reports whether sel is pkg.name.
func selectorIs(sel *ast.SelectorExpr, pkg, name string) bool {
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg && sel.Sel.Name == name
}

func asExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}
