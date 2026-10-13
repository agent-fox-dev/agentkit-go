package landlock

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-47: Close kills the agent and a background sleep left by execute
// and removes a sandbox containing a read-only tree. 18-REQ-7.6.
func TestCloseKillsSession_TS18_47(t *testing.T) {
	needABI4(t)
	p := newRealProvider(t, Options{})
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimSuffix(s.Spec().Workdir, "/workspace")
	ts := realTools(t, s)
	pid := agentPID(t, dir)
	code, out := execute(t, ts, "sleep 300 >/dev/null 2>&1 & echo $!")
	sleepPID, err := strconv.Atoi(strings.TrimSpace(out))
	if code != 0 || err != nil {
		t.Fatalf("starting sleep: %d %q", code, out)
	}
	if code, out := execute(t, ts, "mkdir -p ro/a/b && touch ro/a/b/f && chmod -R a-w ro"); code != 0 {
		t.Fatalf("chmod: %d %q", code, out)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if !eventually(time.Second, func() bool { return !alive(pid) && !alive(sleepPID) }) {
		t.Errorf("agent %d alive %t, sleep %d alive %t", pid, alive(pid), sleepPID, alive(sleepPID))
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the sandbox directory remains: %v", err)
	}
}
