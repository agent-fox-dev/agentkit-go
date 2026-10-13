//go:build unix

package landlock

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

var testRepo = sandbox.GitRepo{URL: "https://example.invalid/r", Ref: "main", Depth: 1}

// gitEnv is a Spec.Env whose PATH holds a fake git, so Hello reports Git.
func gitEnv(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	script(t, dir, "git", "exit 0")
	return map[string]string{"PATH": dir + ":/usr/bin:/bin"}, dir
}

// initScript makes the init command run a shell script with body.
func initScript(t *testing.T, body string) func(*seams) {
	t.Helper()
	p := script(t, t.TempDir(), "init.sh", body)
	return func(s *seams) { s.initCommand = func(sandbox.GitRepo, string) []string { return []string{p} } }
}

// gitSandbox creates a sandbox with a fake git on its PATH.
func gitSandbox(t *testing.T, p *provider, spec sandbox.Spec) *landlockSandbox {
	t.Helper()
	env, _ := gitEnv(t)
	if spec.Env == nil {
		spec.Env = env
	}
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s.(*landlockSandbox)
}

// waitFile waits up to 10s for p to exist.
func waitFile(t *testing.T, p string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(p); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never appeared", p)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TS-18-48: the wrapper forwards ID, Spec, Agent, Export and Close, and
// Tools and LocalDir Sync wait for an in-flight GitRepo. 18-REQ-8.1.
func TestWrapperHolds_TS18_48(t *testing.T) {
	ctl := t.TempDir()
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with, initScript(t, "touch "+ctl+"/entered; while [ ! -e "+ctl+"/release ]; do sleep 0.05; done"))
	s := gitSandbox(t, p, sandbox.Spec{})
	ctx := context.Background()
	var mu sync.Mutex
	at := map[string]time.Time{}
	mark := func(k string) { mu.Lock(); at[k] = time.Now(); mu.Unlock() }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = s.Sync(ctx, testRepo); mark("git") }()
	waitFile(t, ctl+"/entered")
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = s.Tools(ctx, sandbox.ToolOptions{}); mark("tools") }()
	go func() { defer wg.Done(); _, _ = s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); mark("sync") }()
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(ctl+"/release", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if at["tools"].Before(at["git"]) || at["sync"].Before(at["git"]) {
		t.Errorf("returns: git %v, tools %v, sync %v", at["git"], at["tools"], at["sync"])
	}
	if s.ID() != s.inner.ID() || !reflectEqual(s.Spec(), s.inner.Spec()) || !reflectEqual(s.Agent(), s.inner.Agent()) {
		t.Error("ID, Spec or Agent differs from attach's sandbox")
	}
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: t.TempDir()}); err != nil {
		t.Errorf("Export: %v", err)
	}
	if err := s.Close(ctx); err != nil || attach.Live(s.inner) != sandbox.ErrClosed {
		t.Errorf("Close = %v, Live %v", err, attach.Live(s.inner))
	}
}

// TS-18-49: the GitRepo slot is taken by a successful GitRepo or other Sync
// and by the first Tools call, but not by a failed GitRepo or Sync.
// 18-REQ-8.2.
func TestGitRepoSlot_TS18_49(t *testing.T) {
	ctx := context.Background()
	const rule = "landlock: GitRepo must be the first Sync and come before Tools"
	ctl := t.TempDir()
	newP := func(f *inProc, mods ...func(*seams)) *provider {
		return newTestProvider(t, Options{}, append([]func(*seams){f.with, initScript(t, "[ -e "+ctl+"/ok ] || exit 1")}, mods...)...)
	}
	_ = os.Remove(ctl + "/ok")
	f := newInProc(t)
	a := gitSandbox(t, newP(f), sandbox.Spec{})
	if _, err := a.Sync(ctx, testRepo); err == nil {
		t.Fatal("(a) the failing GitRepo succeeded")
	}
	n := len(f.starts())
	_, _ = a.Sync(ctx, testRepo)
	if len(f.starts()) != n+1 {
		t.Errorf("(a) the second GitRepo started no init agent")
	}
	if err := os.WriteFile(ctl+"/ok", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, take := range map[string]func(*testing.T, *landlockSandbox){
		"GitRepo": func(t *testing.T, s *landlockSandbox) {
			if _, err := s.Sync(ctx, testRepo); err != nil {
				t.Fatal(err)
			}
		},
		"LocalDir": func(t *testing.T, s *landlockSandbox) {
			if _, err := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		f := newInProc(t)
		s := gitSandbox(t, newP(f), sandbox.Spec{})
		take(t, s)
		n := len(f.starts())
		if _, err := s.Sync(ctx, testRepo); err == nil || err.Error() != rule || len(f.starts()) != n {
			t.Errorf("after a %s Sync: %v, %d new starts", name, err, len(f.starts())-n)
		}
	}
	fd := newInProc(t)
	d := failingToolsSandbox(t, fd, newP)
	if _, err := d.Tools(ctx, sandbox.ToolOptions{}); err == nil {
		t.Fatal("(d) Tools succeeded")
	}
	n = len(fd.starts())
	if _, err := d.Sync(ctx, testRepo); err == nil || err.Error() != rule || len(fd.starts()) != n {
		t.Errorf("(d) after a failing Tools: %v", err)
	}
	fe := newInProc(t)
	e := gitSandbox(t, newP(fe), sandbox.Spec{})
	if _, err := e.Sync(ctx, sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("(e) the LocalDir Sync of a missing directory succeeded")
	}
	n = len(fe.starts())
	if _, err := e.Sync(ctx, testRepo); err != nil || len(fe.starts()) != n+1 {
		t.Errorf("(e) after a failed Sync: %v, %d new starts", err, len(fe.starts())-n)
	}
}

// failingToolsSandbox is a sandbox whose agent's tool digests mismatch, so
// its Tools call fails.
func failingToolsSandbox(t *testing.T, f *inProc, newP func(*inProc, ...func(*seams)) *provider) *landlockSandbox {
	t.Helper()
	p := newP(f, func(s *seams) {
		s.start = func(req startReq) (*agentProc, net.Conn, error) {
			ws := argAfter(req.args, "--workdir")
			r := goodReport(filepath.Dir(ws))
			fa := fakeagent.New()
			fa.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: sandbox.ProtocolMajor, ProtocolMinor: sandbox.ProtocolMinor, Workdir: ws, Git: true,
				Landlock: &sandboxpb.LandlockReport{Abi: uint32(r.ABI), KernelAbi: uint32(r.KernelABI), Tcp: r.TCP, Writable: r.Writable, ReadOnly: r.ReadOnly}})
			fa.SetTools(&sandboxpb.ToolsResponse{Tools: []*sandboxpb.ToolInfo{{Name: "read_file", Digest: []byte("mismatch")}}}, nil)
			return f.fakeAgentStarter(fa)(req)
		}
	})
	return gitSandbox(t, p, sandbox.Spec{})
}

// TS-18-50: GitRepo preconditions are checked in order (live, repository,
// slot, git) and refuse before any process starts. 18-REQ-8.3.
func TestGitRepoPreconditions_TS18_50(t *testing.T) {
	ctx := context.Background()
	bad := sandbox.GitRepo{URL: "http://h/r"}
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with, initScript(t, "exit 0"))
	unchanged := func(name string, s *landlockSandbox, before int, ws []string) {
		t.Helper()
		if len(f.starts()) != before {
			t.Errorf("%s: an init agent started", name)
		}
		if got := entries(t, s.ws); !slices.Equal(got, ws) {
			t.Errorf("%s: the workspace changed: %v", name, got)
		}
	}

	closed := gitSandbox(t, p, sandbox.Spec{})
	_ = closed.Close(ctx)
	n := len(f.starts())
	if _, err := closed.Sync(ctx, bad); err == nil || err.Error() != attach.Live(closed.inner).Error() {
		t.Errorf("(a) closed: %v", err)
	}
	unchanged("(a)", closed, n, nil)

	taken := gitSandbox(t, p, sandbox.Spec{})
	_, _ = taken.Tools(ctx, sandbox.ToolOptions{})
	n = len(f.starts())
	if _, err := taken.Sync(ctx, bad); err == nil || err.Error() != gitclone.Check(bad).Error() || !strings.Contains(err.Error(), "URL") {
		t.Errorf("(b) taken, bad URL: %v", err)
	}
	unchanged("(b)", taken, n, nil)

	takenNoGit := gitSandbox(t, p, sandbox.Spec{Env: map[string]string{"PATH": "/nowhere"}})
	_, _ = takenNoGit.Tools(ctx, sandbox.ToolOptions{})
	n = len(f.starts())
	if _, err := takenNoGit.Sync(ctx, testRepo); err == nil || err.Error() != "landlock: GitRepo must be the first Sync and come before Tools" {
		t.Errorf("(c) taken, no git: %v", err)
	}
	unchanged("(c)", takenNoGit, n, nil)

	noGit := gitSandbox(t, p, sandbox.Spec{Env: map[string]string{"PATH": "/nowhere"}})
	n = len(f.starts())
	if _, err := noGit.Sync(ctx, testRepo); err == nil || err.Error() != "landlock: git is not on the sandbox's PATH (/nowhere); a GitRepo needs it" {
		t.Errorf("(d) no git: %v", err)
	}
	unchanged("(d)", noGit, n, nil)
}

// TS-18-51: the init agent gets the expiry-bounded TTL, unrestricted
// network, the same roots plus the resolver directory, and a git-only
// environment. 18-REQ-8.4.
func TestInitAgentArgs_TS18_51(t *testing.T) {
	a, c := t.TempDir(), t.TempDir()
	f := newInProc(t)
	p := newTestProvider(t, Options{ExtraRO: []string{a}, ExtraRW: []string{c}}, f.with, initScript(t, "exit 0"),
		func(s *seams) { s.resolver = func(string) string { return "/run/systemd/resolve" } })
	_, fakeGit := gitEnv(t)
	start := time.Now()
	s := gitSandbox(t, p, sandbox.Spec{TTL: 10 * time.Minute, Env: map[string]string{"SPEC_SENTINEL": "s-1", "PATH": fakeGit + ":/usr/bin"}})
	if _, err := s.Sync(context.Background(), testRepo); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	rec := f.starts()[1]
	ttl, err := time.ParseDuration(argAfter(rec.args, "--ttl"))
	if err != nil || ttl%time.Second != 0 || ttl > 600*time.Second || ttl < 600*time.Second-elapsed-time.Second {
		t.Errorf("init --ttl %v (%v), elapsed %v", ttl, err, elapsed)
	}
	want := append(sandbox.AgentArgs(sandbox.Spec{TTL: ttl, Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, Workdir: s.ws}),
		"--landlock-ro", p.extraRO[0], "--landlock-ro", "/run/systemd/resolve", "--landlock-rw", p.extraRW[0])
	if !slices.Equal(rec.args, want) {
		t.Errorf("init args %q,\nwant %q", rec.args, want)
	}
	wantEnv := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "HOME=" + s.dir + "/home", "PATH=" + fakeGit + ":/usr/bin", "TMPDIR=" + s.dir + "/tmp"}
	if !slices.Equal(rec.env, wantEnv) {
		t.Errorf("init env %q,\nwant %q", rec.env, wantEnv)
	}
	if _, err := os.Stat(s.dir); err != nil || s.Agent().Version == "" {
		t.Errorf("after the init agent closed: dir %v, agent %+v", err, s.Agent())
	}
}

// TS-18-52: a successful init runs the clone through the init agent's
// run_command, fills the workspace, takes the slot and closes the init
// sandbox. 18-REQ-8.5.
func TestInitSucceeds_TS18_52(t *testing.T) {
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with, initScript(t, "mkdir -p .git && echo ref > .git/HEAD && echo readme > README.md"))
	s := gitSandbox(t, p, sandbox.Spec{})
	res, err := s.Sync(context.Background(), testRepo)
	if err != nil || res != (sandbox.SyncResult{}) {
		t.Fatalf("Sync = %+v, %v", res, err)
	}
	for _, n := range []string{"README.md", ".git/HEAD"} {
		if _, err := os.Stat(filepath.Join(s.ws, n)); err != nil {
			t.Errorf("the workspace lacks %s: %v", n, err)
		}
	}
	initPID := f.nextPID - 1
	if !eventually2(time.Second, func() bool { return len(f.table.session(initPID)) == 0 }) {
		t.Error("the init agent still runs")
	}
	if !initToolOptions.DisableSpill {
		t.Error("the init agent's tools spill")
	}
	if _, err := s.Sync(context.Background(), testRepo); err == nil || !strings.Contains(err.Error(), "GitRepo must be the first Sync") {
		t.Errorf("a second GitRepo: %v", err)
	}
	if got := defaultInitCommand(testRepo, s.ws); !slices.Equal(got, append([]string{"git"}, gitclone.Args(testRepo, s.ws)...)) {
		t.Errorf("the default init command is %q", got)
	}
}

// eventually2 polls cond for up to d on every platform.
func eventually2(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

// TS-18-53: an init agent whose report is below ABI 4 or lacks the
// workspace is closed without running git, and the Sync wraps
// ErrNoLandlock. 18-REQ-8.6.
func TestInitReport_TS18_53(t *testing.T) {
	ctl := t.TempDir()
	f := newInProc(t)
	var initRep func(string) sandbox.LandlockInfo
	f.report = func(dir string) sandbox.LandlockInfo {
		if f.isInit(dir) && initRep != nil {
			return initRep(dir)
		}
		return goodReport(dir)
	}
	p := newTestProvider(t, Options{}, f.with, initScript(t, "echo x >> "+ctl+"/ran"))
	s := gitSandbox(t, p, sandbox.Spec{})
	for name, rep := range map[string]func(string) sandbox.LandlockInfo{
		"ABI 3":        reportWith(func(r *sandbox.LandlockInfo) { r.ABI = 3 }),
		"no workspace": reportWith(func(r *sandbox.LandlockInfo) { r.Writable = r.Writable[1:] }),
	} {
		initRep = rep
		if _, err := s.Sync(context.Background(), testRepo); !errors.Is(err, ErrNoLandlock) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(ctl + "/ran"); err == nil {
			t.Errorf("%s: the init command ran", name)
		}
		initPID := f.nextPID - 1
		if !eventually2(time.Second, func() bool { return len(f.table.session(initPID)) == 0 }) {
			t.Errorf("%s: the init agent was not closed", name)
		}
	}
	initRep = nil
	if _, err := s.Sync(context.Background(), testRepo); err != nil {
		t.Errorf("a good init after the refusals: %v", err)
	}
}

// TS-18-54: an init command that cannot run git or exits non-zero fails the
// Sync with the advice or the URL and a bounded output tail. 18-REQ-8.7.
func TestInitCommandFails_TS18_54(t *testing.T) {
	for name, mod := range map[string]func(*seams){
		"exec_failed": func(s *seams) {
			s.initCommand = func(sandbox.GitRepo, string) []string { return []string{"/nonexistent/git"} }
		},
		"127": initScript(t, "echo not found; exit 127"),
		"126": initScript(t, "echo cannot execute; exit 126"),
	} {
		f := newInProc(t)
		s := gitSandbox(t, newTestProvider(t, Options{}, f.with, mod), sandbox.Spec{})
		_, err := s.Sync(context.Background(), testRepo)
		if err == nil || !strings.Contains(err.Error(), "could not be run under the sandbox's ruleset") ||
			!strings.Contains(err.Error(), "system director") || !strings.Contains(err.Error(), "Options.ExtraRO") {
			t.Errorf("%s: %v", name, err)
		}
	}
	f := newInProc(t)
	s := gitSandbox(t, newTestProvider(t, Options{}, f.with, initScript(t, `printf HEAD-START; i=0; while [ $i -lt 500 ]; do printf 0123456789; i=$((i+1)); done; printf FATAL-END; exit 128`)), sandbox.Spec{})
	_, err := s.Sync(context.Background(), testRepo)
	if err == nil || !strings.Contains(err.Error(), testRepo.URL) || !strings.Contains(err.Error(), "FATAL-END") || strings.Contains(err.Error(), "HEAD-START") {
		t.Fatalf("exit 128: %v", err)
	}
	if i := strings.Index(err.Error(), "FATAL-END"); i > 2048+len(err.Error())-len(err.Error()[i:])+200 {
		t.Errorf("the carried output is too long: %d bytes", len(err.Error()))
	}
	if _, tail, ok := strings.Cut(err.Error(), "output: "); !ok || len(tail) > 2048 {
		t.Errorf("the carried output is %d bytes (found %t)", len(tail), ok)
	}
}

// TS-18-55: a GitRepo Sync whose context ends wraps ctx.Err(), and one that
// reaches the sandbox's expiry wraps ErrUnavailable. 18-REQ-8.7.
func TestInitCancelAndExpiry_TS18_55(t *testing.T) {
	ctl := t.TempDir()
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with, initScript(t, "touch "+ctl+"/entered; exec sleep 30"))
	s := gitSandbox(t, p, sandbox.Spec{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { waitFile(t, ctl+"/entered"); time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	if _, err := s.Sync(ctx, testRepo); !errors.Is(err, context.Canceled) || time.Since(start) > 10*time.Second {
		t.Errorf("cancelled: %v after %v", err, time.Since(start))
	}
	s3 := gitSandbox(t, p, sandbox.Spec{TTL: 3 * time.Second})
	start = time.Now()
	if _, err := s3.Sync(context.Background(), testRepo); !errors.Is(err, sandbox.ErrUnavailable) || time.Since(start) > 10*time.Second {
		t.Errorf("expiry: %v after %v", err, time.Since(start))
	}
}

// TS-18-56: a failed init empties the workspace, keeps the directory and
// the free slot, and leaves the sandbox's agent working. 18-REQ-8.8.
func TestInitFailureEmpties_TS18_56(t *testing.T) {
	ctl := t.TempDir()
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with, initScript(t, "[ -e "+ctl+"/ok ] && exit 0; mkdir -p part/ro && echo x > part/a && chmod 555 part/ro; exit 1"))
	s := gitSandbox(t, p, sandbox.Spec{})
	if _, err := s.Sync(context.Background(), testRepo); err == nil {
		t.Fatal("the failing init succeeded")
	}
	if fi, err := os.Stat(s.ws); err != nil || !fi.IsDir() || len(entries(t, s.ws)) != 0 {
		t.Errorf("the workspace after a failed init: %v, %v", entries(t, s.ws), err)
	}
	if err := os.WriteFile(ctl+"/ok", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	n := len(f.starts())
	if _, err := s.Sync(context.Background(), testRepo); err != nil || len(f.starts()) != n+1 {
		t.Errorf("the slot after a failed init: %v", err)
	}
	ts, err := s.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r := callExecute(ts, "true"); !r {
		t.Error("execute true failed after the failed init")
	}
}

// reflectEqual is reflect.DeepEqual.
func reflectEqual(a, b any) bool { return reflect.DeepEqual(a, b) }

// callExecute runs command through the proxy execute tool and reports
// whether it succeeded.
func callExecute(ts []core.Tool, command string) bool {
	b, _ := json.Marshal(map[string]string{"command": command})
	for _, tl := range ts {
		if tl.Name == "execute" {
			return tl.Execute(context.Background(), b).OK
		}
	}
	return false
}
