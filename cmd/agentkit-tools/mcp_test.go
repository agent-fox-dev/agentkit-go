package main

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

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
