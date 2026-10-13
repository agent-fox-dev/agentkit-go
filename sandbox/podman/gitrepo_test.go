package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

var testRepo = sandbox.GitRepo{URL: "https://github.com/agent-fox-dev/agentkit-go", Depth: 1}

// withGit gives f's agent a PATH holding a stub git, so Hello reports Git.
func withGit(t *testing.T, f *fakeRunner) *fakeRunner {
	f.agentEnv = []string{"PATH=" + stubBinary(t, "git")}
	return f
}

// isInit reports whether argv is the init step.
func isInit(argv []string) bool {
	i := slices.Index(argv, "--entrypoint")
	return i >= 0 && i+1 < len(argv) && argv[i+1] == "git"
}

// initCmd is the recorded init step, if any, and its position.
func initCmd(f *fakeRunner) (cmd, int) {
	for i, c := range f.recorded() {
		if !c.start && isInit(c.argv) {
			return c, i
		}
	}
	return cmd{}, -1
}

// initAnswer answers the init step with fn and every other command with
// exit 0.
func initAnswer(fn func(ctx context.Context) (tools.ExecResult, error)) func(context.Context, []string) (tools.ExecResult, error) {
	return func(ctx context.Context, argv []string) (tools.ExecResult, error) {
		if isInit(argv) {
			return fn(ctx)
		}
		return exitResult(0, ""), nil
	}
}

// callTool runs the named proxy tool with input.
func callTool(t *testing.T, ts []core.Tool, name string, input map[string]any) core.ToolResult {
	t.Helper()
	b, _ := json.Marshal(input)
	for _, tl := range ts {
		if tl.Name == name {
			return tl.Execute(context.Background(), b)
		}
	}
	t.Fatalf("no tool %s", name)
	return core.ToolResult{}
}

// TS-17-42: the podman sandbox forwards every method to the attached
// sandbox. 17-REQ-7.1.
func TestForwards_TS17_42(t *testing.T) {
	f := withGit(t, newFake(t))
	s := create(t, f, sandbox.Spec{})
	ctx := context.Background()
	if s.ID() != volumeID(t, f) || !s.Agent().Git || s.Spec().Image != "img:1" {
		t.Errorf("ID %q, Agent %+v, Spec().Image %q", s.ID(), s.Agent(), s.Spec().Image)
	}
	src := t.TempDir()
	for _, n := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(src, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.Sync(ctx, sandbox.LocalDir{Path: src})
	if err != nil || r.Files != 2 {
		t.Fatalf("Sync = %+v, %v; want 2 files", r, err)
	}
	if _, err := os.Stat(filepath.Join(f.workdir, "a.txt")); err != nil {
		t.Errorf("the synced file is not in the agent's workdir: %v", err)
	}
	ts, err := s.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, ts, "write_file", map[string]any{"path": "new.txt", "content": "x"}); !res.OK {
		t.Fatalf("write_file: %+v", res)
	}
	out := t.TempDir()
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "new.txt")); err != nil {
		t.Errorf("Export did not bring new.txt back: %v", err)
	}
	if err := s.Close(ctx); err != nil || !f.ran("volume", "rm", "agentkit-"+s.ID()) {
		t.Errorf("Close = %v, destroy step ran %t", err, f.ran("volume", "rm"))
	}
}

// TS-17-43: the GitRepo slot is taken by a successful Sync of any source
// and by the first Tools call, even a failing one, but not by a failed
// Sync. 17-REQ-7.1, 17-REQ-7.3.
func TestGitRepoSlot_TS17_43(t *testing.T) {
	ctx := context.Background()
	const rule = "GitRepo must be the first Sync and come before Tools"
	ok := initAnswer(func(context.Context) (tools.ExecResult, error) { return exitResult(0, ""), nil })

	fa := withGit(t, newFake(t))
	fa.answer = ok
	a := create(t, fa, sandbox.Spec{})
	if _, err := a.Sync(ctx, testRepo); err != nil {
		t.Fatal(err)
	}

	fb := withGit(t, newFake(t))
	b := create(t, fb, sandbox.Spec{})
	if _, err := b.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); err != nil {
		t.Fatal(err)
	}

	fc := newFake(t)
	fc.startFn = func([]string) (process, error) {
		ag := fakeagent.New()
		ag.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: sandbox.ProtocolMajor, ProtocolMinor: sandbox.ProtocolMinor, Workdir: "/workspace", Git: true})
		ag.SetTools(&sandboxpb.ToolsResponse{Tools: []*sandboxpb.ToolInfo{{Name: "read_file", Digest: []byte("mismatch")}}}, nil)
		return fc.serveFakeAgent(ag)
	}
	c := create(t, fc, sandbox.Spec{})
	if _, err := c.Tools(ctx, sandbox.ToolOptions{}); err == nil {
		t.Fatal("Tools with mismatched digests succeeded")
	}

	for name, x := range map[string]struct {
		s sandbox.Sandbox
		f *fakeRunner
	}{"A": {a, fa}, "B": {b, fb}, "C": {c, fc}} {
		n := len(x.f.recorded())
		if _, err := x.s.Sync(ctx, testRepo); err == nil || !strings.Contains(err.Error(), rule) {
			t.Errorf("%s: GitRepo Sync = %v, want the slot rule", name, err)
		}
		if len(x.f.recorded()) != n {
			t.Errorf("%s: the refused Sync ran an engine command", name)
		}
	}

	fd := withGit(t, newFake(t))
	fd.answer = ok
	d := create(t, fd, sandbox.Spec{})
	if _, err := d.Sync(ctx, sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("LocalDir Sync of a missing directory succeeded")
	}
	if _, err := d.Sync(ctx, testRepo); err != nil {
		t.Fatalf("D: GitRepo Sync after a failed Sync = %v, want the init step to run", err)
	}
	if _, i := initCmd(fd); i < 0 {
		t.Error("D: no init step ran")
	}
}

// TS-17-44: a GitRepo Sync runs exactly InitArgs, bounded by the expiry,
// and the clone is visible to the tools. 17-REQ-7.2.
func TestGitRepoInitStep_TS17_44(t *testing.T) {
	f := withGit(t, newFake(t))
	const readme = "# cloned\n"
	f.answer = initAnswer(func(context.Context) (tools.ExecResult, error) {
		return exitResult(0, ""), os.WriteFile(filepath.Join(f.workdir, "README.md"), []byte(readme), 0o644)
	})
	o := Options{Engine: EnginePodman}
	p := testProvider(f, o)
	spec := sandbox.Spec{Image: "img:1", TTL: 10 * time.Minute}
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	r, err := s.Sync(context.Background(), testRepo)
	if err != nil || r != (sandbox.SyncResult{}) {
		t.Fatalf("GitRepo Sync = %+v, %v; want zeros and nil", r, err)
	}
	c, i := initCmd(f)
	if i < 0 {
		t.Fatal("no init step ran")
	}
	created, perr := time.Parse(time.RFC3339, labelValue(c.argv, "agentkit.sandbox.expires"))
	if perr != nil {
		t.Fatal(perr)
	}
	created = created.Add(-10 * time.Minute)
	eff, _ := sandbox.CheckSpec(p.Capabilities(), spec)
	if want := InitArgs(o, s.ID(), created, eff, testRepo); !slices.Equal(c.argv[1:], want) {
		t.Errorf("init %q,\nwant %q", c.argv[1:], want)
	}
	limit := created.Add(10*time.Minute + time.Second) // the label is truncated to whole seconds
	if c.deadline.IsZero() || c.deadline.After(limit) || c.timeout <= 0 || time.Now().Add(c.timeout).After(limit.Add(time.Second)) {
		t.Errorf("init deadline %v, timeout %v; want both within created+10m (%v)", c.deadline, c.timeout, limit)
	}
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, ts, "read_file", map[string]any{"path": "README.md"}); !res.OK || !strings.Contains(fmt.Sprint(res.Data["content"]), "cloned") {
		t.Errorf("read_file README.md: %+v", res)
	}
}

// TS-17-45: Tools and Sync calls wait for an in-flight GitRepo Sync.
// 17-REQ-7.4.
func TestGitRepoHolds_TS17_45(t *testing.T) {
	f := withGit(t, newFake(t))
	release, started := make(chan struct{}), make(chan struct{})
	f.answer = initAnswer(func(context.Context) (tools.ExecResult, error) {
		close(started)
		<-release
		return exitResult(0, ""), nil
	})
	s := create(t, f, sandbox.Spec{})
	ctx := context.Background()
	var mu sync.Mutex
	var order []string
	mark := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	wg.Add(1)
	go func() { defer wg.Done(); _, err := s.Sync(ctx, testRepo); mark("git"); errs <- err }()
	<-started
	wg.Add(2)
	go func() { defer wg.Done(); _, err := s.Tools(ctx, sandbox.ToolOptions{}); mark("tools"); errs <- err }()
	go func() {
		defer wg.Done()
		_, err := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()})
		mark("sync")
		errs <- err
	}()
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	early := slices.Clone(order)
	mu.Unlock()
	if len(early) != 0 {
		t.Fatalf("%v returned while the init step was running", early)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if order[0] != "git" {
		t.Errorf("order %v, want the GitRepo Sync first", order)
	}
}

// TS-17-46: a GitRepo Sync checks liveness, then the repository, then
// Agent().Git, before any command, and leaves the slot free. 17-REQ-7.5.
func TestGitRepoChecks_TS17_46(t *testing.T) {
	ctx := context.Background()
	bad := sandbox.GitRepo{URL: "ssh://h/r"}
	noCmd := func(t *testing.T, f *fakeRunner, s sandbox.Sandbox, repo sandbox.GitRepo) error {
		t.Helper()
		n := len(f.recorded())
		_, err := s.Sync(ctx, repo)
		if len(f.recorded()) != n {
			t.Errorf("the refused Sync ran %v", f.recorded()[n:])
		}
		return err
	}

	fClosed := withGit(t, newFake(t))
	sClosed := create(t, fClosed, sandbox.Spec{})
	_ = sClosed.Close(ctx)
	if err := noCmd(t, fClosed, sClosed, bad); err != sandbox.ErrClosed {
		t.Errorf("closed: %v, want ErrClosed", err)
	}

	fDead := withGit(t, newFake(t))
	sDead := create(t, fDead, sandbox.Spec{})
	fDead.lastProc().exit(0)
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err := sDead.Tools(ctx, sandbox.ToolOptions{})
		if errors.Is(err, sandbox.ErrUnavailable) || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := noCmd(t, fDead, sDead, bad); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("unavailable: %v, want ErrUnavailable", err)
	}

	fLive := withGit(t, newFake(t))
	fLive.answer = initAnswer(func(context.Context) (tools.ExecResult, error) { return exitResult(0, ""), nil })
	sLive := create(t, fLive, sandbox.Spec{})
	if err := noCmd(t, fLive, sLive, bad); err == nil || err.Error() != gitclone.Check(bad).Error() {
		t.Errorf("bad repository: %v, want gitclone.Check's %v", err, gitclone.Check(bad))
	}
	if _, err := sLive.Sync(ctx, testRepo); err != nil {
		t.Errorf("valid repository after the refusal: %v, want the init step to run", err)
	}

	fNoGit := newFake(t)
	sNoGit := create(t, fNoGit, sandbox.Spec{})
	if err := noCmd(t, fNoGit, sNoGit, testRepo); err == nil || !strings.Contains(err.Error(), "git") ||
		!strings.Contains(err.Error(), "PATH") || !strings.Contains(err.Error(), "GitRepo") {
		t.Errorf("no git: %v, want the image to need git on its PATH", err)
	}
}

// TS-17-48: a failing init step fails the Sync with the right message and
// leaves the slot free. 17-REQ-7.7.
func TestGitRepoInitFails_TS17_48(t *testing.T) {
	f := withGit(t, newFake(t))
	var code int
	var out string
	f.answer = initAnswer(func(context.Context) (tools.ExecResult, error) { return exitResult(code, out), nil })
	s := create(t, f, sandbox.Spec{})
	ctx := context.Background()
	code, out = 127, "exec: git: not found"
	if _, err := s.Sync(ctx, testRepo); err == nil || !strings.Contains(err.Error(), "cannot run git") {
		t.Errorf("exit 127: %v, want the image to be unable to run git", err)
	}
	code, out = 128, "fatal: repository not found"
	if _, err := s.Sync(ctx, testRepo); err == nil || !strings.Contains(err.Error(), testRepo.URL) || !strings.Contains(err.Error(), out) {
		t.Errorf("exit 128: %v, want the URL and the output", err)
	}
	code, out = 0, ""
	if _, err := s.Sync(ctx, testRepo); err != nil {
		t.Errorf("exit 0 after two failures: %v", err)
	}
}

// TS-17-49: a cancelled, timed-out or unstartable init step is followed by
// rm -f of the init container, and the slot stays free. 17-REQ-7.8.
func TestGitRepoInitCleanup_TS17_49(t *testing.T) {
	ctx := context.Background()
	blocked := func(ctx context.Context) (tools.ExecResult, error) {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return tools.ExecResult{Outcome: tools.OutcomeTimeout, ExitCode: -1}, nil
		}
		return tools.ExecResult{Outcome: tools.OutcomeAbort, ExitCode: -1}, nil
	}
	rmAfterInit := func(t *testing.T, f *fakeRunner, id string) {
		t.Helper()
		_, i := initCmd(f)
		if i < 0 || f.index(i+1, "rm", "-f", "agentkit-"+id+"-init") < 0 {
			t.Errorf("no rm -f agentkit-%s-init after the init step: %v", id, f.recorded())
		}
	}

	var fail bool
	f := withGit(t, newFake(t))
	f.answer = initAnswer(func(ictx context.Context) (tools.ExecResult, error) {
		if fail {
			return exitResult(0, ""), nil
		}
		return blocked(ictx)
	})
	s := create(t, f, sandbox.Spec{})
	cctx, cancel := context.WithCancel(ctx)
	time.AfterFunc(100*time.Millisecond, cancel)
	if _, err := s.Sync(cctx, testRepo); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v, want context.Canceled", err)
	}
	rmAfterInit(t, f, s.ID())
	fail = true
	if _, err := s.Sync(ctx, testRepo); err != nil {
		t.Errorf("after the cancelled init step: %v, want the slot free", err)
	}

	f2 := withGit(t, newFake(t))
	f2.answer = initAnswer(blocked)
	s2 := create(t, f2, sandbox.Spec{TTL: 2 * time.Second})
	start := time.Now()
	if _, err := s2.Sync(ctx, testRepo); err == nil {
		t.Error("expiry: the Sync succeeded")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("expiry: the Sync returned after %v, want about the 2s TTL", time.Since(start))
	}
	rmAfterInit(t, f2, s2.ID())

	f3 := withGit(t, newFake(t))
	var started bool
	f3.answer = initAnswer(func(context.Context) (tools.ExecResult, error) {
		if started {
			return exitResult(0, ""), nil
		}
		return tools.ExecResult{}, errors.New(`tools: "podman" not found on PATH`)
	})
	s3 := create(t, f3, sandbox.Spec{})
	if _, err := s3.Sync(ctx, testRepo); err == nil {
		t.Error("unstartable: the Sync succeeded")
	}
	rmAfterInit(t, f3, s3.ID())
	started = true
	if _, err := s3.Sync(ctx, testRepo); err != nil {
		t.Errorf("after the unstartable init step: %v, want the slot free", err)
	}
}
