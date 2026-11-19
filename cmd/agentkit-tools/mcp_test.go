package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

const (
	initFrame  = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`
	readyFrame = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
)

// session is the command serving mcp on pipes, with AgentKit's MCP client
// connected to it.
type session struct {
	cmd    *exec.Cmd
	conn   *agentmcp.ServerConnection
	stderr *bytes.Buffer
}

// startSession starts `agentkit-tools mcp` with args and connects to it.
func startSession(t *testing.T, args ...string) *session {
	t.Helper()
	cmd := exec.Command(binary(t), append([]string{"mcp"}, args...)...)
	cmd.Env = testEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	s := &session{cmd: cmd, stderr: &bytes.Buffer{}}
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	s.conn, err = agentmcp.Connect(context.Background(), agentmcp.ServerConfig{Name: "agentkit-tools", Timeout: time.Minute},
		agentmcp.NewPipeTransport(stdout, stdin, wire.Defaults()), agentmcp.ConnectionOptions{})
	if err != nil {
		t.Fatalf("connecting: %v; stderr %s", err, s.stderr)
	}
	return s
}

// text is the text of a tools/call result.
func (s *session) call(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	res, err := s.conn.Call(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*agentmcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if res.IsError {
		t.Fatalf("%s failed: %s", name, b.String())
	}
	return b.String()
}

func TestAHarnessSearchesEditsAndSearchesAgainInOneSession(t *testing.T) {
	dir := sampleTree(t)
	s := startSession(t, "--dir", dir)
	list, err := s.conn.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range list {
		names = append(names, tl.Name)
	}
	// The MCP SDK lists a server's tools by name; the set is the served one.
	want := parseListing(t, run(t, "tools", "--dir", dir).stdout).names()
	if !slices.Equal(slices.Sorted(slices.Values(names)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("tools/list = %v, want the tools %v", names, want)
	}
	if slices.Contains(names, "code_search") {
		if got := s.call(t, "code_search", map[string]any{"query": "needle"}); !strings.Contains(got, "a.go") {
			t.Fatalf("the first search did not find a.go:\n%s", got)
		}
	}
	s.call(t, "edit_file", map[string]any{"path": "b.go", "edits": []any{map[string]any{
		"old_string": "func Other() int { return 2 }", "new_string": "func Other() int { return 2 } // fresh_marker"}}})
	if slices.Contains(names, "code_search") {
		if got := s.call(t, "code_search", map[string]any{"query": "fresh_marker"}); !strings.Contains(got, "b.go") {
			t.Fatalf("the search after the edit did not find b.go:\n%s", got)
		}
	}
	if err := s.conn.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the command did not exit after the client closed")
	}
	if code := s.cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("exit %d after the client closed; stderr %s", code, s.stderr)
	}
}

func TestAFrameThatBreaksTheWireLimitsEndsTheSessionWithoutQuotingIt(t *testing.T) {
	cmd := exec.Command(binary(t), "mcp", "--dir", sampleTree(t))
	cmd.Env = testEnv()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	bad := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"secret_marker":"a","secret_marker":"b"}}}`
	go func() {
		for _, f := range []string{initFrame, readyFrame, bad} {
			if _, err := io.WriteString(stdin, f+"\n"); err != nil {
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the session did not end on the rejected frame")
	}
	if code := cmd.ProcessState.ExitCode(); code != 1 {
		t.Fatalf("exit %d, want 1; stderr %s", code, stderr.String())
	}
	var own []string
	for _, l := range lines(stderr.String()) {
		if strings.HasPrefix(l, "agentkit-tools:") {
			own = append(own, l)
		}
	}
	if len(own) != 1 || !strings.HasPrefix(own[0], "agentkit-tools: mcp session ended: frame rejected: ") {
		t.Fatalf("the command's stderr lines = %q", own)
	}
	if strings.Contains(stderr.String(), "secret_marker") {
		t.Fatalf("stderr quotes the frame:\n%s", stderr.String())
	}
}

func TestASignalEndsTheCommandWithItsStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals")
	}
	dir := sampleTree(t)
	req := writeRequest(t, `{"name":"execute","input":{"command":"sleep 60","timeout_s":120}}`)
	for _, c := range []struct {
		sig  os.Signal
		code int
	}{{syscall.SIGINT, 130}, {syscall.SIGTERM, 143}} {
		t.Run("mcp/"+c.sig.String(), func(t *testing.T) {
			s := startSession(t, "--dir", dir)
			go func() {
				_, _ = s.conn.Call(context.Background(), "execute", map[string]any{"command": "sleep 60", "timeout_s": 120})
			}()
			time.Sleep(1500 * time.Millisecond) // the call has started its sleep
			if err := s.cmd.Process.Signal(c.sig); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				_ = s.cmd.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("the session outlived the signal")
			}
			if code := s.cmd.ProcessState.ExitCode(); code != c.code {
				t.Fatalf("exit %d, want %d; stderr %s", code, c.code, s.stderr)
			}
		})
		t.Run("call/"+c.sig.String(), func(t *testing.T) {
			p := start(t, testEnv(), "call", "--dir", dir, "--request", req)
			time.Sleep(1500 * time.Millisecond) // the call has started its sleep
			if err := p.cmd.Process.Signal(c.sig); err != nil {
				t.Fatal(err)
			}
			if code := p.wait(20 * time.Second); code != c.code {
				t.Fatalf("exit %d, want %d; stderr %s", code, c.code, p.stderr)
			}
			if p.stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want nothing", p.stdout)
			}
		})
	}
}
