package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// binary is agentkit-sandboxd built once for this package's tests.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentkit-sandboxd-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "agentkit-sandboxd")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building agentkit-sandboxd: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// started is a running agentkit-sandboxd and a sandbox attached to it.
type started struct {
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	sb     sandbox.Sandbox
}

// start runs the binary with args and env and attaches to its stdio.
func start(t *testing.T, env []string, args ...string) *started {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sb, err := attach.Dial(context.Background(), stdioconn.New(stdout, stdin), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatalf("attach: %v (stderr %q)", err, stderr.String())
	}
	return &started{cmd: cmd, stderr: &stderr, sb: sb}
}

// tool runs the named proxy with input.
func tool(t *testing.T, sb sandbox.Sandbox, name, input string) core.ToolResult {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tl {
		if p.Name == name {
			return p.Execute(context.Background(), []byte(input))
		}
	}
	t.Fatalf("no proxy %s", name)
	return core.ToolResult{}
}

// runBinary runs the binary to completion and returns stdout, stderr and the exit
// code.
func runBinary(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

func oneLine(s string) bool {
	s = strings.TrimSpace(s)
	return s != "" && !strings.Contains(s, "\n")
}

// TS-13-23: agentkit-sandboxd defaults to /workspace and 1h and hands its
// own environment to the tools verbatim. 13-REQ-4.1.
func TestDefaultsAndEnvironment_TS13_23(t *testing.T) {
	cfg, err := parseFlags(nil)
	if err != nil || cfg.workdir != "/workspace" || cfg.ttl != time.Hour {
		t.Fatalf("parseFlags() = %+v, %v", cfg, err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX env command")
	}
	t.Setenv("HOST_ONLY_VAR", "1")
	p := start(t, []string{"PATH=" + os.Getenv("PATH"), "MARKER=sandboxd-env"}, "--workdir", t.TempDir())
	out := tool(t, p.sb, "execute", `{"command":"env"}`).Text
	if !strings.Contains(out, "MARKER=sandboxd-env") || strings.Contains(out, "HOST_ONLY_VAR") {
		t.Errorf("execute env:\n%s", out)
	}
}

// expectedVersion is the module version recorded in the built binary, or
// (devel): what the binary itself reports.
func expectedVersion(t *testing.T) string {
	t.Helper()
	bi, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	if bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// TS-13-24: --version prints the versions and exits 0, and usage errors
// exit non-zero with a one-line stderr message without serving.
// 13-REQ-4.2, 13-REQ-4.3.
func TestVersionAndUsageErrors_TS13_24(t *testing.T) {
	so, _, code := runBinary(t, "--version")
	if code != 0 || !strings.Contains(so, expectedVersion(t)) || !strings.Contains(so, "1.1") {
		t.Errorf("--version: exit %d, stdout %q", code, so)
	}
	for _, args := range [][]string{{"--ttl", "0"}, {"--ttl", "-1s"}, {"--ttl", "bogus"}, {"--nosuchflag"}} {
		so, se, code := runBinary(t, args...)
		if code == 0 || !oneLine(se) || so != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q; want non-zero, nothing, one line", args, code, so, se)
		}
	}
}

// TS-13-25: the command points os.Stdout at stderr before serving and opens
// no listening socket. 13-REQ-4.4.
func TestStdoutRedirectAndNoSocket_TS13_25(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stderr = w
	got := redirectStdout()
	fmt.Println("stray")
	os.Stdout, os.Stderr = origOut, origErr
	w.Close()
	captured, _ := io.ReadAll(r)
	if !strings.Contains(string(captured), "stray") {
		t.Errorf("a stray Println reached %q, not stderr", captured)
	}
	if got != origOut {
		t.Error("redirectStdout did not return the original stdout")
	}

	p := start(t, []string{"PATH=" + os.Getenv("PATH")}, "--workdir", t.TempDir())
	if p.sb.Agent().ProtocolMajor != 1 {
		t.Errorf("Hello over stdio: %+v", p.sb.Agent())
	}
	if runtime.GOOS != "linux" {
		t.Skip("the socket check reads /proc")
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", p.cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	for _, fd := range fds {
		link, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", p.cmd.Process.Pid, fd.Name()))
		if strings.HasPrefix(link, "socket:") {
			t.Errorf("fd %s is a socket (%s)", fd.Name(), link)
		}
	}
}

// TS-13-26: a startup failure or TTL expiry exits non-zero with one stderr
// line naming the cause. 13-REQ-4.6.
func TestStartupFailureAndTTL_TS13_26(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	_, se, code := runBinary(t, "--workdir", missing)
	if code == 0 || !oneLine(se) || !strings.Contains(se, missing) {
		t.Errorf("missing workdir: exit %d, stderr %q", code, se)
	}

	cmd := exec.Command(binary, "--workdir", t.TempDir(), "--ttl", "300ms")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var se2 bytes.Buffer
	cmd.Stderr = &se2
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil || !oneLine(se2.String()) || !strings.Contains(se2.String(), "300ms") {
			t.Errorf("TTL: %v, stderr %q", err, se2.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the agent outlived its 300ms TTL by 5s")
	}
}

// TS-13-27: agentkit-sandboxd builds on the four cross targets with cgo off
// and on the host with cgo on. 13-REQ-4.7.
func TestCrossTargetBuilds_TS13_27(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-target builds skipped under -short")
	}
	for _, tgt := range [][3]string{{"linux", "amd64", "0"}, {"linux", "arm64", "0"}, {"darwin", "arm64", "0"},
		{"windows", "amd64", "0"}, {runtime.GOOS, runtime.GOARCH, "1"}} {
		cmd := exec.Command("go", "build", "-o", os.DevNull, ".")
		cmd.Env = append(os.Environ(), "GOOS="+tgt[0], "GOARCH="+tgt[1], "CGO_ENABLED="+tgt[2])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%v: %v\n%s", tgt, err, out)
		}
	}
}

// TS-13-74 (smoke, 13-PATH-6): the built binary serves the protocol on its
// stdio to the attach package and exits 0 when the host closes.
// 13-REQ-4.4, 13-REQ-4.5.
func TestSmokeBinaryServesStdio_TS13_74(t *testing.T) {
	wd := t.TempDir()
	if err := os.WriteFile(filepath.Join(wd, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := start(t, []string{"PATH=" + os.Getenv("PATH")}, "--workdir", wd, "--ttl", "1m")
	want, _ := filepath.EvalSymlinks(wd)
	if p.sb.Agent().ProtocolMajor != 1 || p.sb.Agent().Workdir != want {
		t.Errorf("Agent() = %+v", p.sb.Agent())
	}
	tl, err := p.sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil || len(tl) != 11 {
		t.Fatalf("Tools: %d, %v", len(tl), err)
	}
	if r := tool(t, p.sb, "list_files", `{"path":"."}`); !r.OK || !strings.Contains(r.Text, "seed.txt") {
		t.Errorf("list_files: %+v", r)
	}
	if err := p.sb.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after the host closed: %v (stderr %q)", err, p.stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent did not exit within 5s of the host closing")
	}
}
