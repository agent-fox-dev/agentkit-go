package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// binary is agentkit-sandboxd built once for the package's tests, with a
// version, commit and build time stamped at link time.
var binary string

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "agentkit-sandboxd-test-")
	binary = filepath.Join(dir, "agentkit-sandboxd")
	ldflags := fmt.Sprintf("-X %[1]s.stamp=v9.8.7 -X %[1]s.commit=abc1234 -X %[1]s.buildTime=2026-10-14T00:00:00Z",
		"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver")
	if out, err := exec.Command("go", "build", "-ldflags", ldflags, "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building agentkit-sandboxd: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// agentProc is a running agentkit-sandboxd and the sandbox on its stdio.
type agentProc struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	sb     sandbox.Sandbox
}

// start runs the binary with env and args and attaches to it.
func start(t *testing.T, env []string, args ...string) *agentProc {
	t.Helper()
	p := &agentProc{cmd: exec.Command(binary, args...), stderr: new(bytes.Buffer)}
	p.cmd.Env, p.cmd.Stderr = env, p.stderr
	stdin, _ := p.cmd.StdinPipe()
	stdout, _ := p.cmd.StdoutPipe()
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.cmd.Process.Kill(); _ = p.cmd.Wait() })
	var err error
	if p.sb, err = attach.Dial(context.Background(), stdioconn.New(stdout, stdin), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}}); err != nil {
		t.Fatalf("attach: %v (stderr %q)", err, p.stderr)
	}
	return p
}

// tool runs the named proxy with input.
func (p *agentProc) tool(t *testing.T, name, input string) core.ToolResult {
	t.Helper()
	tl, err := p.sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tl {
		if tool.Name == name {
			return tool.Execute(context.Background(), []byte(input))
		}
	}
	t.Fatalf("no proxy %s", name)
	return core.ToolResult{}
}

// sh runs command with the execute tool and returns its exit code and output.
func (p *agentProc) sh(t *testing.T, command string) (string, string) {
	t.Helper()
	r := p.tool(t, "execute", fmt.Sprintf(`{"command":%q}`, command))
	return fmt.Sprint(r.Data["exit_code"]), r.Text
}

// runIn runs the command in this process, capturing stdout and stderr.
func runIn(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	orig, dir := [2]*os.File{os.Stdout, os.Stderr}, t.TempDir()
	os.Stdout, _ = os.Create(filepath.Join(dir, "stdout"))
	os.Stderr, _ = os.Create(filepath.Join(dir, "stderr"))
	code = run(args)
	os.Stdout, os.Stderr = orig[0], orig[1]
	so, _ := os.ReadFile(filepath.Join(dir, "stdout"))
	se, _ := os.ReadFile(filepath.Join(dir, "stderr"))
	return string(so), string(se), code
}

// oneLine reports whether s is the command's one-line error message.
func oneLine(s string) bool {
	return strings.HasPrefix(s, "agentkit-sandboxd: ") && strings.Count(s, "\n") == 1 && strings.HasSuffix(s, "\n")
}
