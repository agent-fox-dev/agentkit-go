//go:build unix

package landlock

import (
	"context"
	"errors"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// It rescans until none remains, then names the survivors or the scan's error.
func TestSessionKillKillsTheWholeSession(t *testing.T) {
	ft := newFakeTable()
	ft.add(procEntry{PID: 101, Session: 100, State: 'S'}, procEntry{PID: 102, Session: 100, State: 'Z'},
		procEntry{PID: 103, Session: 200, State: 'S'}, procEntry{PID: 104, Session: 100, State: 'R'})
	if err := sessionKill(context.Background(), ft, 100, time.Second); err != nil || ft.scans < 2 {
		t.Fatalf("sessionKill = %v after %d scans", err, ft.scans)
	}
	if slices.Sort(ft.kills); !slices.Equal(ft.kills, []int{101, 104}) {
		t.Errorf("killed %v, want 101 and 104", ft.kills)
	}
	ft.add(procEntry{PID: 106, Session: 100, State: 'S'}, procEntry{PID: 105, Session: 100, State: 'S'})
	ft.immortal[105], ft.immortal[106] = true, true
	if err := sessionKill(context.Background(), ft, 100, 100*time.Millisecond); err == nil || err.Error() != "landlock: processes of session 100 survived SIGKILL: 105 106" {
		t.Errorf("survivors: %v", err)
	}
	ft.listErr = errors.New("no /proc")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sessionKill(ctx, ft, 100, time.Hour); err == nil || !strings.Contains(err.Error(), "killing session 100: no /proc") {
		t.Errorf("a failing scan: %v", err)
	}
}

func TestStopAgentKillsTheGroupOfAnAgentIgnoringEOF(t *testing.T) {
	h, stdin, _, err := startProcess("/bin/cat", nil, []string{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := stopAgent(h, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if code, signaled, _ := h.exit(); code != 0 || signaled || h.exitedOnItsOwn() {
		t.Errorf("cat exited %d, signaled %t", code, signaled)
	}
	h, stdin, _, err = startProcess("/bin/sh", []string{"-c", `trap "" TERM HUP; exec sleep 60`}, []string{"PATH=/usr/bin:/bin"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	if err := stopAgent(h, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, signaled, sig := h.exit(); !signaled || sig != syscall.SIGKILL || h.exitedOnItsOwn() {
		t.Errorf("sh: signaled %t by %v", signaled, sig)
	}
	if err := syscall.Kill(-h.pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Errorf("the process group %d still has members: %v", h.pid, err)
	}
	if _, _, _, err := startProcess("/nonexistent/agent", nil, nil, t.TempDir()); err == nil {
		t.Error("starting a missing binary succeeded")
	}
}
