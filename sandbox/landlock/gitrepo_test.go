//go:build unix

package landlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
)

var testRepo = sandbox.GitRepo{URL: "https://example.invalid/r", Ref: "main", Depth: 1}

// initScript makes the init agent's command a shell script holding body.
func initScript(t *testing.T, body string) func(*seams) {
	t.Helper()
	p := script(t, t.TempDir(), "init.sh", body)
	return func(s *seams) { s.initCommand = func(sandbox.GitRepo, string) []string { return []string{p} } }
}

// gitSandbox creates a sandbox whose PATH holds a fake git, unless spec sets
// Env, so Hello reports git.
func gitSandbox(t *testing.T, p *provider, spec sandbox.Spec) *landlockSandbox {
	t.Helper()
	if spec.Env == nil {
		spec.Env = map[string]string{"PATH": filepath.Dir(script(t, t.TempDir(), "git", "exit 0")) + ":/usr/bin:/bin"}
	}
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s.(*landlockSandbox)
}

// initDone reports whether every agent f started after the first is gone.
func initDone(f *inProc) bool {
	for pid := 40001; pid < f.pid; pid++ {
		if f.table.alive(pid) {
			return false
		}
	}
	return true
}

// The init agent gets unrestricted network, the TTL left in whole seconds, the
// same roots plus the resolver directory, and only git settings, HOME, TMPDIR and PATH.
func TestGitRepoClonesThroughAnInitAgent(t *testing.T) {
	f := newInProc()
	p := newTestProvider(t, Options{ExtraRO: []string{t.TempDir()}, ExtraRW: []string{t.TempDir()}}, f.with,
		initScript(t, "mkdir -p .git && echo ref > .git/HEAD && echo readme > README.md"),
		func(s *seams) { s.resolver = func(string) string { return "/run/systemd/resolve" } })
	fakeGit := filepath.Dir(script(t, t.TempDir(), "git", "exit 0"))
	start := time.Now()
	s := gitSandbox(t, p, sandbox.Spec{TTL: 10 * time.Minute, Env: map[string]string{"SPEC_SENTINEL": "s-1", "PATH": fakeGit + ":/usr/bin"}})
	if res, err := s.Sync(context.Background(), testRepo); err != nil || res != (sandbox.SyncResult{}) {
		t.Fatalf("Sync = %+v, %v", res, err)
	}
	rec := f.starts()[1]
	ttl, err := time.ParseDuration(argAfter(rec.args, "--ttl"))
	if err != nil || ttl%time.Second != 0 || ttl > 10*time.Minute || ttl < 10*time.Minute-time.Since(start)-time.Second {
		t.Errorf("the init agent's --ttl %v (%v)", ttl, err)
	}
	want := append(sandbox.AgentArgs(sandbox.Spec{TTL: ttl, Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, Workdir: s.ws}),
		"--landlock-ro", p.extraRO[0], "--landlock-ro", "/run/systemd/resolve", "--landlock-rw", p.extraRW[0])
	wantEnv := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "HOME=" + s.dir + "/home", "PATH=" + fakeGit + ":/usr/bin", "TMPDIR=" + s.dir + "/tmp"}
	if !slices.Equal(rec.args, want) || !slices.Equal(rec.env, wantEnv) || rec.dir != s.ws {
		t.Errorf("the init agent started as %+v,\nwant %q and %q", rec, want, wantEnv)
	}
	if b, err := os.ReadFile(filepath.Join(s.ws, ".git", "HEAD")); err != nil || string(b) != "ref\n" || !testutil.Within(time.Second, func() bool { return initDone(f) }) {
		t.Errorf("after the clone: HEAD %q, %v; init agent gone %t", b, err, initDone(f))
	}
	if got := defaultInitCommand(testRepo, s.ws); !slices.Equal(got, append([]string{"git"}, gitclone.Args(testRepo, s.ws)...)) {
		t.Errorf("the default init command is %q", got)
	}
}

// Tools and other Syncs wait for an in-flight GitRepo Sync.
func TestGitRepoHoldsToolsAndSyncUntilItEnds(t *testing.T) {
	ctl := t.TempDir()
	f := newInProc()
	p := newTestProvider(t, Options{}, f.with, initScript(t, "touch "+ctl+"/entered; while [ ! -e "+ctl+"/release ]; do sleep 0.02; done"))
	s := gitSandbox(t, p, sandbox.Spec{})
	ctx := context.Background()
	var mu sync.Mutex
	var order []string
	mark := func(k string) { mu.Lock(); order = append(order, k); mu.Unlock() }
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = s.Sync(ctx, testRepo); mark("git") })
	testutil.Eventually(t, "the clone to start", func() bool { _, err := os.Stat(ctl + "/entered"); return err == nil })
	wg.Go(func() { _, _ = s.Tools(ctx, sandbox.ToolOptions{}); mark("tools") })
	wg.Go(func() { _, _ = s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); mark("sync") })
	if testutil.Within(100*time.Millisecond, func() bool { mu.Lock(); defer mu.Unlock(); return len(order) > 0 }) {
		t.Errorf("%v returned during the clone", order)
	}
	if err := os.WriteFile(ctl+"/release", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if order[0] != "git" {
		t.Errorf("returns in order %v", order)
	}
}

// The GitRepo slot is taken by a successful GitRepo or other Sync and by
// the first Tools call, failing or not, but not by a failed GitRepo or Sync.
func TestGitRepoMustComeFirst(t *testing.T) {
	ctx := context.Background()
	ok := filepath.Join(t.TempDir(), "ok") // the init command succeeds once it exists
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	syncs := func(src sandbox.Source) func(*landlockSandbox) error {
		return func(s *landlockSandbox) error { _, err := s.Sync(ctx, src); return err }
	}
	for _, c := range []struct {
		name  string
		first func(s *landlockSandbox) error // what runs before the GitRepo
		fails bool
		taken bool
	}{
		{"a failed GitRepo", syncs(testRepo), true, false},
		{"a failed Sync", syncs(sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "missing")}), true, false},
		{"a GitRepo", func(s *landlockSandbox) error { _ = os.WriteFile(ok, nil, 0o644); return syncs(testRepo)(s) }, false, true},
		{"a Sync", syncs(sandbox.LocalDir{Path: t.TempDir()}), false, true},
		{"Tools", func(s *landlockSandbox) error { _, err := s.Tools(ctx, sandbox.ToolOptions{}); return err }, false, true},
		{"a failing Tools", func(s *landlockSandbox) error { _, err := s.Tools(cancelled, sandbox.ToolOptions{}); return err }, true, true},
	} {
		_ = os.Remove(ok)
		f := newInProc()
		s := gitSandbox(t, newTestProvider(t, Options{}, f.with, initScript(t, "[ -e "+ok+" ]")), sandbox.Spec{})
		if err := c.first(s); (err != nil) != c.fails {
			t.Fatalf("%s: %v", c.name, err)
		}
		_ = os.WriteFile(ok, nil, 0o644)
		n := len(f.starts())
		_, err := s.Sync(ctx, testRepo)
		if c.taken && (!errors.Is(err, errSlot) || len(f.starts()) != n) || !c.taken && (err != nil || len(f.starts()) != n+1) {
			t.Errorf("a GitRepo after %s: %v after %d starts", c.name, err, len(f.starts())-n)
		}
	}
}

// A GitRepo Sync checks, before starting anything, that the sandbox is
// live, then the repository, then the slot, then that the agent has git.
func TestGitRepoChecksItsPreconditionsInOrder(t *testing.T) {
	ctx := context.Background()
	bad := sandbox.GitRepo{URL: "http://h/r"}
	f := newInProc()
	p := newTestProvider(t, Options{}, f.with, initScript(t, "exit 0"))
	noGit := sandbox.Spec{Env: map[string]string{"PATH": "/nowhere"}}
	closed, taken := gitSandbox(t, p, sandbox.Spec{}), gitSandbox(t, p, noGit)
	_ = closed.Close(ctx)
	_, _ = taken.Tools(ctx, sandbox.ToolOptions{})
	for _, c := range []struct {
		s    *landlockSandbox
		repo sandbox.GitRepo
		want string
	}{
		{closed, bad, attach.Live(closed.inner).Error()},
		{taken, bad, gitclone.Check(bad).Error()},
		{taken, testRepo, errSlot.Error()},
		{gitSandbox(t, p, noGit), testRepo, "landlock: git is not on the sandbox's PATH (/nowhere); a GitRepo needs it"},
	} {
		n := len(f.starts())
		if _, err := c.s.Sync(ctx, c.repo); err == nil || err.Error() != c.want || len(f.starts()) != n || len(entries(t, c.s.ws)) != 0 {
			t.Errorf("%v: %v after %d starts, want %q", c.repo, err, len(f.starts())-n, c.want)
		}
	}
}

// An init agent whose report is below ABI 4 or lacks the workspace is
// closed without running the clone, and the Sync wraps ErrNoLandlock.
func TestGitRepoRefusesAnInitAgentBelowTheFloor(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	f := newInProc()
	var initReport func(string) sandbox.LandlockInfo
	f.report = func(dir string) sandbox.LandlockInfo {
		if len(f.starts()) > 1 && initReport != nil {
			return initReport(dir)
		}
		return goodReport(dir)
	}
	s := gitSandbox(t, newTestProvider(t, Options{}, f.with, initScript(t, "echo x >> "+ran)), sandbox.Spec{})
	for name, rep := range map[string]func(string) sandbox.LandlockInfo{
		"ABI 3":        reportWith(func(r *sandbox.LandlockInfo) { r.ABI = 3 }),
		"no workspace": reportWith(func(r *sandbox.LandlockInfo) { r.Writable = r.Writable[1:] }),
	} {
		initReport = rep
		_, err := s.Sync(context.Background(), testRepo)
		if _, serr := os.Stat(ran); !errors.Is(err, ErrNoLandlock) || !strings.Contains(err.Error(), "the GitRepo init agent reported") || serr == nil ||
			!testutil.Within(time.Second, func() bool { return initDone(f) }) {
			t.Errorf("%s: %v; the clone ran %t, the init agent is gone %t", name, err, serr == nil, initDone(f))
		}
	}
	initReport = nil
	if _, err := s.Sync(context.Background(), testRepo); err != nil {
		t.Errorf("a good init after the refusals: %v", err)
	}
}

// A clone that cannot run git fails with advice, and one that exits
// non-zero names the URL and carries at most the last 2 KiB of its output.
func TestGitRepoReportsAFailedClone(t *testing.T) {
	const advice = "landlock: git could not be run under the sandbox's ruleset; install git beneath a system directory or add its directory to Options.ExtraRO: "
	for _, c := range []struct {
		name      string
		mod       func(*seams)
		want, not string
	}{
		{"exec_failed", func(s *seams) {
			s.initCommand = func(sandbox.GitRepo, string) []string { return []string{"/nonexistent/git"} }
		}, advice, ""},
		{"127", initScript(t, "echo not found; exit 127"), advice + "not found", ""},
		{"126", initScript(t, "echo cannot execute; exit 126"), advice + "cannot execute", ""},
		{"128", initScript(t, `printf HEAD; i=0; while [ $i -lt 500 ]; do printf 0123456789; i=$((i+1)); done; printf FATAL-END; exit 128`),
			"landlock: GitRepo clone of https://example.invalid/r failed (", "HEAD"},
	} {
		f := newInProc()
		_, err := gitSandbox(t, newTestProvider(t, Options{}, f.with, c.mod), sandbox.Spec{}).Sync(context.Background(), testRepo)
		if err == nil || !strings.HasPrefix(err.Error(), c.want) || c.not != "" && (strings.Contains(err.Error(), c.not) || !strings.HasSuffix(err.Error(), "FATAL-END")) {
			t.Errorf("%s: %v", c.name, err)
		} else if _, tail, _ := strings.Cut(err.Error(), "output: "); len(tail) > gitOutputTail {
			t.Errorf("%s: the output carried is %d bytes", c.name, len(tail))
		}
	}
}

// A GitRepo Sync whose context ends wraps its error, and one that outlasts
// the sandbox's expiry wraps ErrUnavailable.
func TestGitRepoEndsWithItsContextOrTheExpiry(t *testing.T) {
	entered := filepath.Join(t.TempDir(), "entered")
	f := newInProc()
	p := newTestProvider(t, Options{}, f.with, initScript(t, "touch "+entered+"; exec sleep 30"))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		testutil.Within(10*time.Second, func() bool { _, err := os.Stat(entered); return err == nil })
		cancel()
	}()
	if _, err := gitSandbox(t, p, sandbox.Spec{}).Sync(ctx, testRepo); !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "GitRepo clone of "+testRepo.URL) {
		t.Errorf("cancelled: %v", err)
	}
	// At 2.5 s the init agent's TTL, rounded up to 3 s, outlasts the expiry.
	if _, err := gitSandbox(t, p, sandbox.Spec{TTL: 2500 * time.Millisecond}).Sync(context.Background(), testRepo); !errors.Is(err, sandbox.ErrUnavailable) ||
		!strings.Contains(err.Error(), "the sandbox expired during the GitRepo clone") {
		t.Errorf("expiry: %v", err)
	}
}

// A failed clone empties the workspace, read-only leftovers included, and
// leaves the sandbox's agent working.
func TestGitRepoFailureEmptiesTheWorkspace(t *testing.T) {
	f := newInProc()
	p := newTestProvider(t, Options{}, f.with, initScript(t, "mkdir -p part/ro && echo x > part/a && chmod 555 part/ro; exit 1"))
	s := gitSandbox(t, p, sandbox.Spec{})
	if _, err := s.Sync(context.Background(), testRepo); err == nil {
		t.Fatal("the failing clone succeeded")
	}
	if fi, err := os.Stat(s.ws); err != nil || !fi.IsDir() || len(entries(t, s.ws)) != 0 {
		t.Errorf("the workspace after a failed clone: %v, %v", entries(t, s.ws), err)
	}
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(ts, func(tl core.Tool) bool { return tl.Name == "execute" }); i < 0 || !ts[i].Execute(context.Background(), []byte(`{"command":"true"}`)).OK {
		t.Error("execute true failed after the failed clone")
	}
}
