//go:build unix

package landlock

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// TS-18-43: stopping the agent waits for an exit on EOF and SIGKILLs the
// process group of an agent that ignores EOF after 5 seconds. 18-REQ-7.2.
func TestStopAgent_TS18_43(t *testing.T) {
	h, stdin, _, err := startProcess("/bin/cat", nil, []string{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	start := time.Now()
	if err := stopAgent(h, stopWait); err != nil {
		t.Fatal(err)
	}
	if code, signaled, _ := h.exit(); time.Since(start) >= 5*time.Second || code != 0 || signaled {
		t.Errorf("cat: stopped after %v with code %d, signaled %t", time.Since(start), code, signaled)
	}

	h, stdin, _, err = startProcess("/bin/sh", []string{"-c", `trap "" TERM HUP; exec sleep 60`}, []string{"PATH=/usr/bin:/bin"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	start = time.Now()
	_ = stopAgent(h, stopWait)
	if d := time.Since(start); d < 5*time.Second || d >= 7*time.Second {
		t.Errorf("sleep: stopped after %v, want about 5s", d)
	}
	if _, signaled, sig := h.exit(); !signaled || sig != syscall.SIGKILL {
		t.Errorf("sleep: signaled %t by %v, want SIGKILL", signaled, sig)
	}
	if err := syscall.Kill(-h.pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the process group %d still has members: %v", h.pid, err)
	}
}
