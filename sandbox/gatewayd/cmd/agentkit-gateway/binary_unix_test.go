//go:build !windows

package main

import (
	"bytes"
	"net"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// started is a running agentkit-gateway binary.
type started struct {
	cmd    *exec.Cmd
	stderr *syncBuffer
}

// startBinary runs the built binary with args.
func startBinary(t *testing.T, args ...string) *started {
	t.Helper()
	s := &started{cmd: exec.Command(binary, args...), stderr: &syncBuffer{}}
	s.cmd.Stderr = s.stderr
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.cmd.Process.Kill(); _ = s.cmd.Wait() })
	return s
}

// stop sends SIGTERM and returns the exit code.
func (s *started) stop(t *testing.T) int {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the command did not exit after SIGTERM")
	}
	return s.cmd.ProcessState.ExitCode()
}

// runBinary runs the built binary to completion.
func runBinary(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(binary, args...)
	cmd.Stderr = &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("%v did not exit", args)
	}
	return cmd.ProcessState.ExitCode(), stderr.String()
}

// TS-19-54: serving plaintext on a non-loopback address logs a warning
// line in addition to the listening line. 19-REQ-8.2.
func TestPlaintextWarning_TS19_54(t *testing.T) {
	fakeEngine(t)
	tokens := writeFile(t, t.TempDir(), "tokens", tok40+"\n", 0o600)
	warn := regexp.MustCompile(`(?i)warn.*plaintext.*0\.0\.0\.0`)
	s := startBinary(t, "--token-file", tokens, "--plaintext", "--listen", "0.0.0.0:0")
	waitListening(t, s.stderr)
	s.stop(t)
	if !warn.MatchString(s.stderr.String()) {
		t.Errorf("no plaintext warning naming 0.0.0.0:\n%s", s.stderr)
	}
	s = startBinary(t, "--token-file", tokens, "--plaintext", "--listen", "127.0.0.1:0")
	waitListening(t, s.stderr)
	s.stop(t)
	if regexp.MustCompile(`(?i)warn`).MatchString(s.stderr.String()) {
		t.Errorf("a loopback plaintext run warned:\n%s", s.stderr)
	}
}

// TS-19-58: startup failures exit 1 with one stderr line that never
// contains a token. 19-REQ-8.6.
func TestStartupFailures_TS19_58(t *testing.T) {
	fakeEngine(t)
	dir := t.TempDir()
	tokens := writeFile(t, dir, "tokens", tok40+"\n", 0o600)
	short := "short-token-20-chars"
	shortFile := writeFile(t, dir, "short", short+"\n", 0o600)
	notPEM := writeFile(t, dir, "not.pem", "not a certificate", 0o600)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	for _, args := range [][]string{
		{"--token-file", filepath.Join(dir, "missing"), "--plaintext"},
		{"--token-file", shortFile, "--plaintext"},
		{"--token-file", tokens, "--tls-cert", notPEM, "--tls-key", notPEM},
		{"--token-file", tokens, "--engine-binary", "/nonexistent/podman", "--plaintext"},
		{"--token-file", tokens, "--plaintext", "--listen", busy.Addr().String()},
	} {
		code, stderr := runBinary(t, args...)
		lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
		if code != 1 || len(lines) != 1 || !strings.HasPrefix(stderr, "agentkit-gateway: ") {
			t.Errorf("%v: exit %d, stderr %q; want 1 and one agentkit-gateway: line", args, code, stderr)
		}
		if strings.Contains(stderr, tok40) || strings.Contains(stderr, short) {
			t.Errorf("%v: stderr contains a token: %q", args, stderr)
		}
	}
}
