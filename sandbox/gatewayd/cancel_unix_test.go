//go:build !windows

package gatewayd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-19-68: an execute call through the gateway cancelled from the host
// returns aborted and its process is gone. 19-REQ-10.2.
func TestCancel_TS19_68(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	sb := create(t, env.Dial(t, gwtest.Token), unrestrictedSpec)
	exec := toolOf(t, sb, "execute")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan string, 1)
	go func() {
		res <- exec.Execute(ctx, []byte(`{"command":"echo $$ > pid; exec sleep 300"}`)).Error
	}()
	var pid int
	eventually(t, "the pid file", func() bool {
		b, err := os.ReadFile(filepath.Join(sb.Spec().Workdir, "pid"))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	cancel()
	select {
	case code := <-res:
		if code != "aborted" {
			t.Errorf("the cancelled call returned %q, want aborted", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled call did not return")
	}
	eventually(t, "the process to exit", func() bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) })
}
