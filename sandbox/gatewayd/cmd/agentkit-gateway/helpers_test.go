package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

const tok40 = "0123456789abcdefghijklmnopqrstuvwxyzABCD"

// syncBuffer is a bytes.Buffer safe for one writer and concurrent readers.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

var listening = regexp.MustCompile(`(?m)^agentkit-gateway: listening on (\S+) \((tls|plaintext)\), backend (\S+), version (\S+)$`)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// waitListening waits for the startup line in out and returns its address.
func waitListening(t *testing.T, out interface{ String() string }) string {
	t.Helper()
	var m []string
	testutil.Eventually(t, "the listening line", func() bool { m = listening.FindStringSubmatch(out.String()); return m != nil })
	return m[1]
}

// file writes body to a file in a fresh directory and returns its path.
func file(t *testing.T, body string) string {
	return filepath.Join(testutil.Tree(t, map[string]string{"f": body}), "f")
}

// backend is a newBackend that answers be and records the options it got.
func backend(be sandbox.Provider, got *podman.Options) func(podman.Options) (sandbox.Provider, error) {
	return func(o podman.Options) (sandbox.Provider, error) { *got = o; return be, nil }
}

// serving is the command running in-process.
type serving struct {
	stderr *syncBuffer
	sigs   chan os.Signal
	code   chan int
	addr   string
}

// serve runs the command with newBackend, a token file holding tok40, a free
// loopback port and the unrestricted network, then args.
func serve(t *testing.T, newBackend func(podman.Options) (sandbox.Provider, error), args ...string) *serving {
	t.Helper()
	s := &serving{stderr: &syncBuffer{}, sigs: make(chan os.Signal, 1), code: make(chan int, 1)}
	args = append([]string{"--token-file", file(t, tok40+"\n"), "--listen", "127.0.0.1:0", "--network", "unrestricted"}, args...)
	go func() { s.code <- run(args, io.Discard, s.stderr, deps{newBackend: newBackend, signals: s.sigs}) }()
	s.addr = waitListening(t, s.stderr)
	return s
}

// stop sends sig and returns the exit code.
func (s *serving) stop(t *testing.T, sig os.Signal) int {
	t.Helper()
	s.sigs <- sig
	select {
	case code := <-s.code:
		return code
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after the signal")
		return 0
	}
}
