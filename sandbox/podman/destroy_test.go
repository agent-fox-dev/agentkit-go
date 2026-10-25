package podman

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

func TestCloseRunsTheDestroyStepOnce(t *testing.T) {
	f := newFake(t)
	s := create(t, f, sandbox.Spec{})
	n := len(f.recorded())
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.lastProc().Done():
	default:
		t.Fatal("Close returned before the engine process exited")
	}
	var got []string
	for _, c := range f.recorded()[n:] {
		got = append(got, strings.Join(c.argv[1:], " "))
	}
	if want := "rm -f agentkit-" + s.ID() + "|rm -f agentkit-" + s.ID() + "-init|volume rm agentkit-" + s.ID(); strings.Join(got, "|") != want {
		t.Fatalf("Close ran %q, want %q", got, want)
	}
	if err := s.Close(context.Background()); err != nil || len(f.recorded()) != n+3 {
		t.Errorf("second Close = %v and ran %d commands", err, len(f.recorded())-n-3)
	}
}

// The group is killed 5 s after Close; every removal still runs.
func TestDestroyKillsALingeringProcessAndJoinsFailures(t *testing.T) {
	t.Parallel()
	f := newFake(t)
	f.startFn = func() (process, error) { return f.serveAgent(true, sandbox.LandlockInfo{}) }
	f.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
		switch {
		case hasArgs(argv, "rm", "-f") && !strings.HasSuffix(argv[len(argv)-1], "-init"):
			return exitResult(1, "Error: permission denied"), nil
		case hasArgs(argv, "volume", "rm"):
			return exitResult(1, "Error: driver error"), nil
		}
		return exitResult(0, ""), nil
	}
	s := create(t, f, sandbox.Spec{})
	start := time.Now()
	err := s.Close(context.Background())
	if at := f.lastProc().killAt.Load(); at == 0 || time.Unix(0, at).Sub(start) < 5*time.Second {
		t.Errorf("the group was killed at %v, want at least 5s after Close began at %v", time.Unix(0, at), start)
	}
	if !f.ran("rm", "-f", "agentkit-"+s.ID()+"-init") || err == nil || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "driver error") {
		t.Errorf("Close = %v, want every removal run and both failures joined", err)
	}
}

// A volume in use is retried for up to 10 s, then reported by name.
func TestDestroyToleratesMissingEntriesAndRetriesAVolumeInUse(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		busy     int // volume rm answers "in use" this many times; -1 forever
		out      string
		attempts int
	}{{0, "Error: No such container or volume", 1}, {2, "", 3}, {-1, "", 0}} {
		f := newFake(t)
		busy := c.busy
		f.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
			switch {
			case hasArgs(argv, "volume", "rm") && busy != 0:
				busy--
				return exitResult(2, "Error: volume is being used by the following container(s)"), nil
			case c.out != "" && !hasArgs(argv, "volume", "create"):
				return exitResult(1, c.out), nil
			}
			return exitResult(0, ""), nil
		}
		s := create(t, f, sandbox.Spec{})
		start := time.Now()
		err := s.Close(context.Background())
		if c.busy >= 0 && (err != nil || f.count("volume", "rm") != c.attempts) {
			t.Errorf("busy %d: Close = %v after %d attempts, want nil after %d", c.busy, err, f.count("volume", "rm"), c.attempts)
		}
		if d := time.Since(start); c.busy < 0 && (err == nil || !strings.Contains(err.Error(), "agentkit-"+s.ID()+" is still in use") || d < 10*time.Second) {
			t.Errorf("in use forever: Close = %v after %v, want the volume named after 10s", err, d)
		}
	}
}
