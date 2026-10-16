//go:build linux

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// waitExit waits for the agent to exit after Close and returns its status.
func waitExit(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the agent did not exit within 10s of Close")
		return nil
	}
}

// TS-15-43: a confined agent started with --network none lets execute write
// under W, H and T and denies the sibling write, the outside read and the
// loopback connect. 15-PATH-1, 15-REQ-5.4, 15-REQ-1.3.
func TestSmokeConfinedNone_TS15_43(t *testing.T) {
	needABI(t, 4)
	w, h, tmp, s := confinedDirs(t)
	dial := buildDialer(t, w)
	addr, accepted := listenLoopback(t)
	cmd := exec.Command(binary, "--network", "none", "--workdir", w)
	cmd.Env = []string{"HOME=" + h, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sb, err := attach.Dial(context.Background(), stdioconn.New(stdout, stdin), attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(w, "a"), filepath.Join(h, "b"), filepath.Join(tmp, "c")} {
		if code, out := execIn(t, sb, "touch "+f); code != "0" {
			t.Errorf("touch %s: exit %s: %s", f, code, out)
		}
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s is not on the host: %v", f, err)
		}
	}
	for _, c := range []string{"touch " + filepath.Join(s, "x"), "cat " + filepath.Join(s, "secret")} {
		if code, out := execIn(t, sb, c); code == "0" || !strings.Contains(out, "Permission denied") {
			t.Errorf("%s: exit %s: %s", c, code, out)
		}
	}
	if _, err := os.Lstat(filepath.Join(s, "x")); err == nil {
		t.Error("S/x exists")
	}
	if code, out := execIn(t, sb, dial+" "+addr); code == "0" || accepted.Load() != 0 {
		t.Errorf("dialtcp: exit %s (%s), %d accepted", code, out, accepted.Load())
	}
	l := sb.Agent().Landlock
	if l.ABI != 4 || !l.TCP {
		t.Errorf("Agent().Landlock = %+v", l)
	}
	for _, d := range []string{w, h, tmp} {
		if !slices.Contains(l.Writable, resolvedPath(t, d)) {
			t.Errorf("Writable %v lacks %s", l.Writable, d)
		}
	}
	if err := sb.Close(context.Background()); err != nil {
		t.Error(err)
	}
	if err := waitExit(t, cmd); err != nil {
		t.Errorf("the agent exited with %v, want 0", err)
	}
}

// TS-15-46: an embedder requiring Landlock starts the confined agent with
// AgentArgs, syncs a project over Rsync, runs a shell tool and exports the
// result. 15-PATH-4, 15-REQ-3.6, 15-REQ-6.6, 15-REQ-5.1.
func TestSmokeRequireLandlock_TS15_46(t *testing.T) {
	needABI(t, 4)
	w, h, tmp, _ := confinedDirs(t)
	project, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	caps := sandbox.Capabilities{Provider: "test", Network: []sandbox.NetworkMode{sandbox.NetworkNone}, Workdir: true, Landlock: true}
	eff, err := sandbox.CheckSpec(caps, sandbox.Spec{RequireLandlock: true, Network: sandbox.Network{Mode: sandbox.NetworkNone}, Workdir: w})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, sandbox.AgentArgs(eff)...)
	cmd.Env = []string{"HOME=" + h, "TMPDIR=" + tmp, "PATH=/usr/bin:/bin"}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	destroyed := 0
	var exitErr error
	destroy := func(context.Context) error { destroyed++; exitErr = waitExit(t, cmd); return nil }
	ctx := context.Background()
	sb, err := attach.Dial(ctx, stdioconn.New(stdout, stdin), attach.Config{Provider: "test", Spec: eff, Destroy: destroy})
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if a := sb.Agent(); a.ProtocolMinor != 2 || a.Landlock.ABI != 4 || !a.Landlock.TCP {
		t.Errorf("Agent() = %+v", a)
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: project, Ignore: workspace.NoGlobalExcludes()}); err != nil {
		t.Errorf("Sync: %v", err)
	} else if _, err := os.Stat(filepath.Join(w, "main.go")); err != nil {
		t.Errorf("main.go is not in W: %v", err)
	}
	if code, out := execIn(t, sb, "/bin/sh -c 'mkdir -p out && echo hello > out/result.txt'"); code != "0" {
		t.Errorf("execute: exit %s: %s", code, out)
	}
	if err := sb.Export(ctx, "out", sandbox.LocalDir{Path: dst, Ignore: workspace.NoGlobalExcludes()}); err != nil {
		t.Errorf("Export: %v", err)
	} else if b, err := os.ReadFile(filepath.Join(dst, "result.txt")); err != nil || string(b) != "hello\n" {
		t.Errorf("D/result.txt = %q, %v", b, err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Error(err)
	}
	if destroyed != 1 || exitErr != nil {
		t.Errorf("Close ran the destroy step %d times; the agent exited with %v", destroyed, exitErr)
	}
}
