package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/podman"
)

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

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// listening matches the startup line and captures the bound address.
var listening = regexp.MustCompile(`(?m)^agentkit-gateway: listening on (\S+) \((tls|plaintext)\), backend (\S+), version (\S+)$`)

// waitListening waits for the startup line in out and returns its address.
func waitListening(t *testing.T, out interface{ String() string }) string {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if m := listening.FindStringSubmatch(out.String()); m != nil {
			return m[1]
		}
	}
	t.Fatalf("no listening line in:\n%s", out.String())
	return ""
}

// TS-19-56: a stop signal runs Shutdown and the command exits 0, or 1 when
// destroying a sandbox failed. 19-REQ-8.4.
func TestStopSignal_TS19_56(t *testing.T) {
	tokens := writeFile(t, t.TempDir(), "tokens", tok40+"\n", 0o600)
	for _, c := range []struct {
		name     string
		sig      os.Signal
		closeErr error
		want     int
	}{
		{"SIGTERM", syscall.SIGTERM, nil, 0},
		{"SIGINT", syscall.SIGINT, nil, 0},
		{"failing close", syscall.SIGINT, errors.New("close failed"), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			be := gwtest.NewCounting()
			be.SetCloseErr(c.closeErr)
			sigs := make(chan os.Signal, 1)
			var stderr syncBuffer
			done := make(chan int, 1)
			go func() {
				done <- run([]string{"--token-file", tokens, "--plaintext", "--listen", "127.0.0.1:0", "--network", "unrestricted"},
					&bytes.Buffer{}, &stderr, deps{newBackend: func(podman.Options) (sandbox.Provider, error) { return be, nil }, signals: sigs})
			}()
			addr := waitListening(t, &stderr)
			p, err := gateway.Dial(context.Background(), "http://"+addr, gateway.Options{Token: tok40})
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			if _, err := p.Create(context.Background(), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}); err != nil {
				t.Fatal(err)
			}
			sigs <- c.sig
			var code int
			select {
			case code = <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("run did not return after the signal")
			}
			if code != c.want {
				t.Errorf("exit %d, want %d; stderr:\n%s", code, c.want, stderr.String())
			}
			if be.Closes() == 0 {
				t.Error("the sandbox's backend Close was not called")
			}
			if c.closeErr != nil {
				var failures []string
				for _, l := range strings.Split(stderr.String(), "\n") {
					if strings.HasPrefix(l, "agentkit-gateway: ") && strings.Contains(l, "close failed") {
						failures = append(failures, l)
					}
				}
				if len(failures) != 1 {
					t.Errorf("stderr names the failure in %d agentkit-gateway: lines, want 1:\n%s", len(failures), stderr.String())
				}
			}
		})
	}
}
