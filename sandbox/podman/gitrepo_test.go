package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

var testRepo = sandbox.GitRepo{URL: "https://github.com/agent-fox-dev/agentkit-go", Depth: 1}

func isInit(argv []string) bool {
	i := slices.Index(argv, "--entrypoint")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == "git"
}

// initIndex is the position of the first init step f recorded, or -1.
func initIndex(f *fakeRunner) int {
	return slices.IndexFunc(f.recorded(), func(c cmd) bool { return !c.start && isInit(c.argv) })
}

// onInit answers the init step with fn and anything else with exit 0.
func onInit(fn func(ctx context.Context) (tools.ExecResult, error)) answerFunc {
	return func(ctx context.Context, argv []string) (tools.ExecResult, error) {
		if isInit(argv) {
			return fn(ctx)
		}
		return exitResult(0, ""), nil
	}
}

// gitSandbox is a sandbox whose agent has git and whose init step is fn (a clone when nil).
func gitSandbox(t *testing.T, spec sandbox.Spec, fn func(ctx context.Context) (tools.ExecResult, error)) (*fakeRunner, sandbox.Sandbox) {
	f := withGit(t, newFake(t))
	if fn == nil {
		fn = func(context.Context) (tools.ExecResult, error) { return exitResult(0, ""), nil }
	}
	f.answer = onInit(fn)
	return f, create(t, f, spec)
}

// The step is exactly InitArgs, bounded by the expiry; the clone reaches the tools.
func TestGitRepoSyncRunsTheInitStep(t *testing.T) {
	var f *fakeRunner
	spec := sandbox.Spec{TTL: 10 * time.Minute}
	f, s := gitSandbox(t, spec, func(context.Context) (tools.ExecResult, error) {
		return exitResult(0, ""), os.WriteFile(filepath.Join(f.workdir, "README.md"), []byte("# cloned\n"), 0o644)
	})
	if r, err := s.Sync(context.Background(), testRepo); err != nil || r != (sandbox.SyncResult{}) {
		t.Fatalf("GitRepo Sync = %+v, %v; want zeros and nil", r, err)
	}
	c := f.recorded()[initIndex(f)]
	created := createdAt(t, c.argv, spec.TTL)
	spec.Image = "img:1"
	if want := InitArgs(Options{Engine: EnginePodman}, s.ID(), created, effective(t, Options{}, spec), testRepo); !slices.Equal(c.argv[1:], want) {
		t.Errorf("init %q,\nwant %q", c.argv[1:], want)
	}
	limit := created.Add(spec.TTL + time.Second) // the label is truncated to whole seconds
	if c.deadline.IsZero() || c.deadline.After(limit) || c.timeout <= 0 || time.Now().Add(c.timeout).After(limit.Add(time.Second)) {
		t.Errorf("init deadline %v, timeout %v; want both within created+TTL (%v)", c.deadline, c.timeout, limit)
	}
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, ts, "read_file", map[string]any{"path": "README.md"}); !strings.Contains(fmt.Sprint(res.Data["content"]), "cloned") {
		t.Errorf("read_file README.md: %+v", res)
	}
}

// A failing first Tools call takes the slot too; a failed Sync does not.
func TestGitRepoIsAcceptedOnlyAsTheFirstSyncBeforeTools(t *testing.T) {
	ctx := context.Background()
	for first, step := range map[string]func(sandbox.Sandbox) error{
		"GitRepo":  func(s sandbox.Sandbox) error { _, err := s.Sync(ctx, testRepo); return err },
		"LocalDir": func(s sandbox.Sandbox) error { _, err := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); return err },
		"failed Sync": func(s sandbox.Sandbox) error {
			_, err := s.Sync(ctx, sandbox.LocalDir{Path: "/nonexistent/src"})
			return err
		},
		"failed Tools": func(s sandbox.Sandbox) error { _, err := s.Tools(ctx, sandbox.ToolOptions{}); return err },
	} {
		f, s := gitSandbox(t, sandbox.Spec{}, nil)
		if first == "failed Tools" {
			f.startFn = func() (process, error) {
				ag := fakeagent.New()
				ag.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: sandbox.ProtocolMajor, ProtocolMinor: sandbox.ProtocolMinor, Workdir: "/workspace", Git: true})
				ag.SetTools(&sandboxpb.ToolsResponse{Tools: []*sandboxpb.ToolInfo{{Name: "read_file", Digest: []byte("mismatch")}}}, nil)
				return f.serveFakeAgent(ag)
			}
			s = create(t, f, sandbox.Spec{})
		}
		if err := step(s); (err != nil) != strings.HasPrefix(first, "failed") {
			t.Fatalf("%s: %v", first, err)
		}
		n := len(f.recorded())
		_, err := s.Sync(ctx, testRepo)
		if refused := err != nil && strings.Contains(err.Error(), "GitRepo must be the first Sync and come before Tools"); refused != (first != "failed Sync") ||
			refused && len(f.recorded()) != n {
			t.Errorf("after %s: GitRepo Sync = %v", first, err)
		}
	}
}

// Liveness, the repository and the agent's git are checked in order, none taking the slot.
func TestGitRepoSyncChecksBeforeAnyCommand(t *testing.T) {
	ctx := context.Background()
	bad := sandbox.GitRepo{URL: "ssh://h/r"}
	for _, c := range []struct {
		name  string
		repo  sandbox.GitRepo
		setup func(*fakeRunner, sandbox.Sandbox)
		check func(error) bool
	}{
		{"closed", bad, func(_ *fakeRunner, s sandbox.Sandbox) { _ = s.Close(ctx) }, func(err error) bool { return err == sandbox.ErrClosed }},
		{"bad repository", bad, func(*fakeRunner, sandbox.Sandbox) {}, func(err error) bool { return err.Error() == gitclone.Check(bad).Error() }},
		{"no git", testRepo, func(f *fakeRunner, _ sandbox.Sandbox) {}, func(err error) bool {
			return strings.Contains(err.Error(), "needs git on its PATH for a GitRepo")
		}},
	} {
		f, s := gitSandbox(t, sandbox.Spec{}, nil)
		if c.name == "no git" {
			f = newFake(t)
			s = create(t, f, sandbox.Spec{})
		}
		c.setup(f, s)
		n := len(f.recorded())
		if _, err := s.Sync(ctx, c.repo); err == nil || !c.check(err) || len(f.recorded()) != n {
			t.Errorf("%s: Sync = %v after %d commands", c.name, err, len(f.recorded())-n)
		}
		if _, err := s.Sync(ctx, testRepo); c.name == "bad repository" && err != nil {
			t.Errorf("a valid repository after the refusal: %v, want the slot free", err)
		}
	}
}

func TestGitRepoInitFailuresLeaveTheSlotFree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		ttl  time.Duration
		res  tools.ExecResult // after the step's context is done, when rm
		err  error
		want string
		rm   bool // whether the step did not finish, so its container goes
	}{
		{"git missing", 0, exitResult(127, "exec: git: not found"), nil, "cannot run git for a GitRepo (exit status 127)", false},
		{"clone fails", 0, exitResult(128, "fatal: repository not found"), nil, testRepo.URL + " failed (exit, exit status 128): fatal: repository not found", false},
		{"cancelled", 0, tools.ExecResult{Outcome: tools.OutcomeAbort, ExitCode: -1}, nil, context.Canceled.Error(), true},
		{"expired", 2 * time.Second, tools.ExecResult{Outcome: tools.OutcomeTimeout, ExitCode: -1}, nil, "did not finish before the sandbox expired", true},
		{"unstartable", 0, tools.ExecResult{}, errors.New(`"podman" not found on PATH`), "not found on PATH", true},
	} {
		var failed atomic.Bool
		ctx, cancel := context.WithCancel(context.Background())
		f, s := gitSandbox(t, sandbox.Spec{TTL: c.ttl}, func(ictx context.Context) (tools.ExecResult, error) {
			if failed.Swap(true) {
				return exitResult(0, ""), nil
			}
			if c.res.Outcome == tools.OutcomeAbort {
				cancel()
			}
			if c.rm && c.err == nil {
				<-ictx.Done()
			}
			return c.res, c.err
		})
		if _, err := s.Sync(ctx, testRepo); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Sync = %v, want %q", c.name, err, c.want)
		}
		cancel()
		if rm := f.index(initIndex(f)+1, "rm", "-f", "agentkit-"+s.ID()+"-init") >= 0; rm != c.rm {
			t.Errorf("%s: rm -f of the init container ran %t, want %t", c.name, rm, c.rm)
		}
		if _, err := s.Sync(context.Background(), testRepo); c.ttl == 0 && err != nil {
			t.Errorf("%s: a second GitRepo = %v, want the slot free", c.name, err)
		}
	}
}

func TestGitRepoSyncHoldsOtherCalls(t *testing.T) {
	release, started := make(chan struct{}), make(chan struct{})
	_, s := gitSandbox(t, sandbox.Spec{}, func(context.Context) (tools.ExecResult, error) {
		close(started)
		<-release
		return exitResult(0, ""), nil
	})
	ctx := context.Background()
	done := make(chan error, 3)
	go func() { _, err := s.Sync(ctx, testRepo); done <- err }()
	<-started
	go func() { _, err := s.Tools(ctx, sandbox.ToolOptions{}); done <- err }()
	go func() { _, err := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); done <- err }()
	if testutil.Within(200*time.Millisecond, func() bool { return len(done) > 0 }) {
		t.Fatal("a call returned while the init step was running")
	}
	close(release)
	for range 3 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// The init container's mount may have re-created the volume.
func TestGitRepoRacingCloseRemovesTheVolumeAgain(t *testing.T) {
	var s sandbox.Sandbox
	f, s := gitSandbox(t, sandbox.Spec{}, func(context.Context) (tools.ExecResult, error) {
		_ = s.Close(context.Background())
		return exitResult(0, ""), nil
	})
	if _, err := s.Sync(context.Background(), testRepo); !errors.Is(err, sandbox.ErrClosed) {
		t.Errorf("Sync = %v, want ErrClosed", err)
	}
	if f.index(initIndex(f)+1, "volume", "rm", "agentkit-"+s.ID()) < 0 {
		t.Errorf("no volume rm after the init step: %v", f.recorded())
	}
}
