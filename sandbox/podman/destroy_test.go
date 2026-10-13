package podman

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// create is a sandbox over f closed at the end of the test.
func create(t *testing.T, f *fakeRunner, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	if spec.Image == "" {
		spec.Image = "img:1"
	}
	s, err := testProvider(f, Options{}).Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// TS-17-37: Close runs the destroy step's commands in order, and a second
// Close runs nothing. 17-REQ-6.1, 17-REQ-6.4.
func TestCloseDestroys_TS17_37(t *testing.T) {
	f := newFake(t)
	s := create(t, f, sandbox.Spec{})
	id := s.ID()
	n := len(f.recorded())
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.lastProc().Done():
	default:
		t.Fatal("Close returned before the engine process exited")
	}
	got := f.recorded()[n:]
	want := [][]string{{"rm", "-f", "agentkit-" + id}, {"rm", "-f", "agentkit-" + id + "-init"}, {"volume", "rm", "agentkit-" + id}}
	if len(got) != len(want) {
		t.Fatalf("Close ran %d commands, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if strings.Join(got[i].argv[1:], " ") != strings.Join(w, " ") {
			t.Errorf("command %d is %q, want %q", i, got[i].argv[1:], w)
		}
	}
	m := len(f.recorded())
	if err := s.Close(context.Background()); err != nil || len(f.recorded()) != m {
		t.Errorf("second Close = %v and ran %d commands", err, len(f.recorded())-m)
	}
}

// TS-17-38: the destroy step kills a lingering engine process after 5
// seconds, runs every action, and joins their errors. 17-REQ-6.1.
func TestDestroyKillsAndJoins_TS17_38(t *testing.T) {
	f := newFake(t)
	f.startFn = func([]string) (process, error) { return f.serveAgent(true) }
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
	elapsed := time.Since(start)
	p := f.lastProc()
	if !p.killed.Load() || time.Unix(0, p.killAt.Load()).Sub(start) < 5*time.Second {
		t.Errorf("the group was killed %t, %v after Close began; want after at least 5s", p.killed.Load(), time.Unix(0, p.killAt.Load()).Sub(start))
	}
	if !f.ran("rm", "-f", "agentkit-"+s.ID()+"-init") || !f.ran("volume", "rm") {
		t.Error("an action after the failing rm did not run")
	}
	if err == nil || !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "driver error") {
		t.Errorf("Close = %v, want both failures joined", err)
	}
	if elapsed >= 30*time.Second {
		t.Errorf("Close took %v", elapsed)
	}
}

// TS-17-39: missing containers and volumes are not errors, and an in-use
// volume is retried for up to 10 seconds. 17-REQ-6.2, 17-REQ-6.3.
func TestDestroyMissingAndInUse_TS17_39(t *testing.T) {
	f := newFake(t)
	f.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
		if hasArgs(argv, "rm") {
			return exitResult(1, "Error: no such container"), nil
		}
		if hasArgs(argv, "volume", "rm") {
			return exitResult(1, "Error: no such volume"), nil
		}
		return exitResult(0, ""), nil
	}
	if err := create(t, f, sandbox.Spec{}).Close(context.Background()); err != nil {
		t.Errorf("missing container and volume: Close = %v, want nil", err)
	}

	f2 := newFake(t)
	inUse := 2
	f2.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
		if hasArgs(argv, "volume", "rm") && inUse > 0 {
			inUse--
			return exitResult(2, "Error: volume is being used by the following container(s)"), nil
		}
		return exitResult(0, ""), nil
	}
	if err := create(t, f2, sandbox.Spec{}).Close(context.Background()); err != nil || f2.count("volume", "rm") != 3 {
		t.Errorf("in use twice: Close = %v after %d volume rm attempts, want nil after 3", err, f2.count("volume", "rm"))
	}

	f3 := newFake(t)
	f3.answer = func(_ context.Context, argv []string) (tools.ExecResult, error) {
		if hasArgs(argv, "volume", "rm") {
			return exitResult(2, "Error: volume is being used"), nil
		}
		return exitResult(0, ""), nil
	}
	s3 := create(t, f3, sandbox.Spec{})
	t0 := time.Now()
	err := s3.Close(context.Background())
	if d := time.Since(t0); err == nil || !strings.Contains(err.Error(), "agentkit-"+s3.ID()) || d < 10*time.Second || d >= 30*time.Second {
		t.Errorf("in use forever: Close = %v after %v, want an error naming agentkit-%s after 10-30s", err, d, s3.ID())
	}
}
