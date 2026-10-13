//go:build unix

package sandbox_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-13-71 (smoke, 13-PATH-3): cancelling a proxy execute returns aborted at
// once, kills the sandbox process, and the sandbox keeps serving.
// 13-REQ-7.4.
func TestSmokeCancelKillsProcess_TS13_71(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ex, ls := tool(t, sb, "execute"), tool(t, sb, "list_files")
	pidFile := filepath.Join(sb.Agent().Workdir, "child.pid")
	c, cancel := context.WithCancel(context.Background())
	got := make(chan core.ToolResult, 1)
	go func() { got <- ex.Execute(c, []byte(`{"command":"sleep 60 & echo $! > child.pid; wait"}`)) }()
	var pid int
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.HasSuffix(string(b), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child.pid did not appear")
		}
	}
	cancel()
	select {
	case r := <-got:
		if want := core.ErrResult("aborted", "Operation aborted"); !reflect.DeepEqual(r, want) {
			t.Errorf("cancelled execute: %+v, want %+v", r, want)
		}
	case <-time.After(time.Second):
		t.Fatal("the cancelled execute did not return within 1s")
	}
	for deadline := time.Now().Add(5 * time.Second); !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("process %d survived the cancel", pid)
		}
	}
	if r := ls.Execute(context.Background(), []byte(`{"path":"."}`)); !r.OK {
		t.Fatalf("list_files afterwards: %+v", r)
	}
}
