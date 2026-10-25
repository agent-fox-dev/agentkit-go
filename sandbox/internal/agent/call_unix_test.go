//go:build unix

package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// backgroundChild is an execute call whose command leaves a child running
// and writes its pid to child.pid.
var backgroundChild = &sandboxpb.CallRequest{Name: "execute", Input: []byte(`{"command":"sleep 60 & echo $! > child.pid; wait"}`)}

// assertChildGone waits for the process whose pid ws/child.pid holds to be gone.
func assertChildGone(t *testing.T, ws string) {
	t.Helper()
	pid, err := strconv.Atoi(waitLine(t, filepath.Join(ws, "child.pid")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	testutil.Eventually(t, "the call's child process to be gone", func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	})
}

func TestCallCancelledKillsTheCommandsProcesses(t *testing.T) {
	ws := t.TempDir()
	c := configured(t, ws)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := c.Call(ctx, backgroundChild); errc <- err }()
	waitLine(t, filepath.Join(ws, "child.pid"))
	cancel()
	if err := <-errc; status.Code(err) != codes.Canceled {
		t.Errorf("cancelled call returned %v, want Canceled", err)
	}
	assertChildGone(t, ws)
}
