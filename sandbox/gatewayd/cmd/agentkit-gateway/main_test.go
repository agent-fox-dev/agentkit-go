//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// End to end: the built command, over a fake podman on PATH, serves a client
// as gateway/podman and exits 0 on SIGTERM.
func TestTheBuiltCommandServesUntilSIGTERM(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "agentkit-gateway")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	fake := "#!/bin/sh\ncase \"$1\" in --version) echo 'podman version 5.0.0';; esac\n"
	must(t, os.WriteFile(filepath.Join(dir, "podman"), []byte(fake), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	cmd, stderr := exec.Command(bin, "--token-file", file(t, tok40+"\n"), "--plaintext", "--listen", "127.0.0.1:0"), &syncBuffer{}
	cmd.Stderr = stderr
	must(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	p, err := gateway.Dial(context.Background(), "http://"+waitListening(t, stderr), gateway.Options{Token: tok40})
	must(t, err)
	if got := p.Capabilities().Provider; got != "gateway/podman" || listening.FindStringSubmatch(stderr.String())[3] != "podman" {
		t.Errorf("Capabilities().Provider = %q, want gateway/podman:\n%s", got, stderr)
	}
	_ = p.Close()
	must(t, cmd.Process.Signal(syscall.SIGTERM))
	select {
	case <-done: //nolint:errcheck // the exit code is read from ProcessState
	case <-time.After(30 * time.Second):
		t.Fatal("the command did not exit after SIGTERM")
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Errorf("exit %d after SIGTERM, want 0:\n%s", code, stderr)
	}
}
