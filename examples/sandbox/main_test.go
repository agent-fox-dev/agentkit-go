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
	"strings"
	"testing"
)

// TS-13-68: the example runs offline against local and provider/faux and
// exits 0, both as a program and in process. 13-REQ-10.8.
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
	for _, want := range []string{"synced 3 files", "read_file src/main.go in the sandbox: ok=true",
		"README: Says hello from inside the sandbox.", "TODOs: 2", "src: main.go, util.go",
		"rm: blocked_by_policy", "exported src: main.go, util.go", "Sandbox example completed successfully"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("output lacks %q:\n%s", want, b.String())
		}
	}
}

// TS-14-45: the example syncs and exports with LocalDir (no Tar or TarOut
// literal in main.go), runs offline, and leaves the exported files in its
// host output directory. 14-REQ-9.6.
func TestExampleUsesLocalDir_TS14_45(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// literal names the sandbox type of a composite literal, or "".
	literal := func(e ast.Expr) string {
		if cl, ok := e.(*ast.CompositeLit); ok {
			if sel, ok := cl.Type.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "sandbox" {
					return sel.Sel.Name
				}
			}
		}
		return ""
	}
	argTypes := map[string][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		if k := literal(asExpr(n)); k == "Tar" || k == "TarOut" {
			t.Errorf("main.go has a sandbox.%s literal", k)
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Sync" || sel.Sel.Name == "Export") {
				for _, a := range call.Args {
					if k := literal(a); k != "" {
						argTypes[sel.Sel.Name] = append(argTypes[sel.Sel.Name], k)
					}
				}
			}
		}
		return true
	})
	for _, m := range []string{"Sync", "Export"} {
		if got := argTypes[m]; len(got) != 1 || got[0] != "LocalDir" {
			t.Errorf("%s takes %v, want one sandbox.LocalDir", m, got)
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

func asExpr(n ast.Node) ast.Expr {
	e, _ := n.(ast.Expr)
	return e
}
