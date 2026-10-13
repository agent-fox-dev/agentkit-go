//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/confine"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// kernelABI probes Landlock independently of the command:
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION).
func kernelABI() (int, error) {
	r, _, errno := syscall.Syscall(444, 0, 0, 1)
	if errno != 0 {
		return 0, errno
	}
	return int(r), nil
}

// needABI skips, naming what it found, below Landlock ABI min.
func needABI(t *testing.T, min int) int {
	t.Helper()
	k, err := kernelABI()
	if err != nil {
		t.Skipf("Landlock unavailable: %v", err)
	}
	if k < min {
		t.Skipf("kernel Landlock ABI %d, the test needs %d", k, min)
	}
	return k
}

func resolvedPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TS-15-19: the command hands its confinement outcome to the agent, Hello
// reflects it, and processes started by tools inherit the ruleset.
// 15-REQ-3.5.
func TestHandOff_TS15_19(t *testing.T) {
	base := t.TempDir()
	w, h, tmp, s := filepath.Join(base, "w"), filepath.Join(base, "h"), filepath.Join(base, "t"), filepath.Join(base, "s")
	for _, d := range []string{w, h, tmp, s} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := start(t, []string{"HOME=" + h, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}, "--workdir", w)
	l := p.sb.Agent().Landlock
	k, kerr := kernelABI()
	switch {
	case kerr == nil && k >= 2:
		if l.ABI != min(k, 4) || l.KernelABI != k || !slices.Contains(l.Writable, resolvedPath(t, w)) {
			t.Errorf("Agent().Landlock = %+v, want ABI %d, KernelABI %d and %s writable", l, min(k, 4), k, w)
		}
	default:
		if want := confine.Plan(k, kerr, sandbox.NetworkNone).Note; l.ABI != 0 || l.Note != want {
			t.Errorf("Agent().Landlock = %+v, want ABI 0 and Plan's note %q", l, want)
		}
	}
	if l.ABI > 0 {
		f := filepath.Join(s, "f")
		r := tool(t, p.sb, "execute", `{"command":"/bin/sh -c '/bin/sh -c \"echo x > `+f+`\"'"}`)
		if fmt.Sprint(r.Data["exit_code"]) == "0" {
			t.Errorf("a grandchild wrote outside the roots: %+v", r)
		}
		if _, err := os.Lstat(f); err == nil {
			t.Error("S/f exists on the host")
		}
	}

	// The agent.Config literal sets Landlock from the confinement outcome.
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	outcome, landlock := "", ""
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if c, ok := x.Rhs[0].(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Setup" {
					if id, ok := x.Lhs[0].(*ast.Ident); ok {
						outcome = id.Name
					}
				}
			}
		case *ast.CompositeLit:
			if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Config" {
				for _, e := range x.Elts {
					if kv, ok := e.(*ast.KeyValueExpr); ok && kv.Key.(*ast.Ident).Name == "Landlock" {
						if id, ok := kv.Value.(*ast.Ident); ok {
							landlock = id.Name
						}
					}
				}
			}
		}
		return true
	})
	if outcome == "" || landlock != outcome {
		t.Errorf("agent.Config.Landlock is %q, confine.Setup's outcome %q", landlock, outcome)
	}
}

// TS-15-12 (binary half): a ruleset that cannot be built after a good probe
// ends the command with one landlock line on stderr and exit 1, serving
// nothing. 15-REQ-2.6.
func TestApplyFailureExits_TS15_12(t *testing.T) {
	needABI(t, 2)
	if os.Geteuid() == 0 {
		t.Skip("root resolves through a mode-000 directory")
	}
	base := t.TempDir()
	pdir := filepath.Join(base, "p")
	if err := os.MkdirAll(filepath.Join(pdir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(pdir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pdir, 0o755) })
	cmd := exec.Command(binary, "--workdir", t.TempDir(), "--landlock-ro", filepath.Join(pdir, "sub"))
	cmd.Env = []string{"HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	var se bytes.Buffer
	cmd.Stderr = &se
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, aerr := attach.Dial(ctx, stdioconn.New(stdout, stdin), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	err := cmd.Wait()
	code := -1
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	}
	line := se.String()
	if code != 1 || !oneLine(line) || !strings.HasPrefix(line, "agentkit-sandboxd: landlock: ") || !strings.Contains(line, filepath.Join(pdir, "sub")) {
		t.Errorf("exit %d, stderr %q; want 1 and one landlock line naming P/sub", code, line)
	}
	if aerr == nil {
		t.Error("the attach got a Hello")
	}
}

// TS-15-5 (binary case): an agent started with HOME=/ and a missing TMPDIR
// still answers Hello, with both roots skipped and noted. 15-REQ-1.6.
func TestSkippedRootsServe_TS15_5(t *testing.T) {
	needABI(t, 2)
	x := filepath.Join(t.TempDir(), "missing")
	p := start(t, []string{"HOME=/", "TMPDIR=" + x, "PATH=/usr/bin:/bin"}, "--workdir", t.TempDir())
	l := p.sb.Agent().Landlock
	if l.ABI == 0 || slices.Contains(l.Writable, "/") || slices.Contains(l.Writable, x) ||
		!strings.Contains(l.Note, "HOME") || !strings.Contains(l.Note, "TMPDIR") {
		t.Errorf("Agent().Landlock = %+v", l)
	}
}
