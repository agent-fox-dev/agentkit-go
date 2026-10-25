//go:build unix

package landlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// CheckSpec's refusals, Spec.Provider keys and HOME or TMPDIR in Spec.Env refuse before anything exists.
func TestCreateRefusesWhatItCannotHonour(t *testing.T) {
	for _, c := range []struct {
		spec          sandbox.Spec
		field, reason string
	}{
		{sandbox.Spec{Resources: sandbox.Resources{CPUs: 1}}, "Resources.CPUs", ""},
		{sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{{Host: "h", Port: 443}}}}, "Network.Mode", ""},
		{sandbox.Spec{Image: "x"}, "Image", ""},
		{sandbox.Spec{Workdir: "/w"}, "Workdir", ""},
		{sandbox.Spec{Provider: map[string]any{"zeta": 1, "alpha": 2}}, "Provider", `got "alpha"`},
		{sandbox.Spec{Env: map[string]string{"HOME": "/x"}}, "Env", "HOME is set by the landlock provider to the sandbox's private directory"},
		{sandbox.Spec{Env: map[string]string{"TMPDIR": "/x"}}, "Env", "TMPDIR is set"},
	} {
		f := newInProc()
		p := newTestProvider(t, Options{}, f.with)
		s, err := p.Create(context.Background(), c.spec)
		var se *sandbox.SpecError
		if s != nil || !errors.As(err, &se) || se.Provider != "landlock" || se.Field != c.field || !strings.Contains(se.Reason, c.reason) ||
			!errors.Is(err, sandbox.ErrUnsupported) || len(entries(t, p.root)) != 0 || len(f.starts()) != 0 {
			t.Errorf("Create(%+v) = %v, %v; want a landlock SpecError on %s, nothing left or started", c.spec, s, err, c.field)
		}
	}
}

// Under any umask: a 0700 directory with a 0600 marker and 0700 workspace/, home/ and tmp/.
func TestCreateStartsTheAgentInAPrivateDirectory(t *testing.T) {
	f := newInProc()
	p := newTestProvider(t, Options{ExtraRO: []string{t.TempDir()}}, f.with)
	for _, um := range []int{0, 0o022, 0o077} {
		old := syscall.Umask(um)
		s, err := p.Create(context.Background(), sandbox.Spec{RequireLandlock: true, Labels: map[string]string{"team": "lbl-7"}, Env: map[string]string{"A": "1"}})
		syscall.Umask(old)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close(context.Background()) })
		d := filepath.Join(p.root, s.ID())
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(s.ID()) || !slices.Equal(entries(t, d), []string{markerName, "home", "tmp", "workspace"}) {
			t.Errorf("umask %o: sandbox %q holds %v", um, s.ID(), entries(t, d))
		}
		for x, want := range map[string]os.FileMode{d: 0o700, d + "/workspace": 0o700, d + "/home": 0o700, d + "/tmp": 0o700, d + "/" + markerName: 0o600} {
			if fi, err := os.Stat(x); err != nil || fi.Mode().Perm() != want {
				t.Errorf("umask %o: %s has mode %v, want %o", um, x, fi.Mode(), want)
			}
		}
		rec := f.starts()[len(f.starts())-1]
		eff := sandbox.Spec{TTL: time.Hour, Workdir: d + "/workspace"}
		if ls, ok := s.(*landlockSandbox); !ok || s.Spec().Workdir != eff.Workdir || s.Spec().TTL != time.Hour || rec.dir != eff.Workdir || rec.bin != p.bin ||
			!slices.Equal(rec.args, agentArgs(eff, p.extraRO, nil, "")) || !slices.Equal(rec.env, agentEnv(map[string]string{"A": "1"}, d)) ||
			!reflect.DeepEqual(s.Agent().Landlock, goodReport(d)) || ls.agentPATH != defaultPATH {
			t.Errorf("umask %o: %T %+v started as %+v", um, s, s.Spec(), rec)
		}
	}
}

// The marker names the creator, then the agent; it never holds Spec.Env.
func TestCreateMarksTheSandboxWithItsOwner(t *testing.T) {
	f := newInProc()
	var first marker
	f.before = func(req startReq) { first, _ = readMarker(filepath.Dir(argAfter(req.args, "--workdir"))) }
	p := newTestProvider(t, Options{}, f.with)
	before := time.Now()
	s, err := p.Create(context.Background(), sandbox.Spec{Env: map[string]string{"SECRET": "env-sentinel"}, Labels: map[string]string{"a": "b"},
		TTL: 2 * time.Minute, Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	b, _ := os.ReadFile(filepath.Join(p.root, s.ID(), markerName))
	final, err := readMarker(filepath.Join(p.root, s.ID()))
	want := marker{PID: os.Getpid(), StartTime: creatorStart, BootID: "B1", Role: "creator", Created: first.Created, TTL: 2 * time.Minute,
		Network: "unrestricted", Labels: map[string]string{"a": "b"}}
	if !reflect.DeepEqual(first, want) || first.Created.Before(before) || first.Created.After(time.Now()) {
		t.Errorf("the marker before the agent started: %+v", first)
	}
	want.PID, want.StartTime, want.Role = 40000, 400000, "agent"
	if err != nil || !reflect.DeepEqual(final, want) || strings.Contains(string(b), "env-sentinel") || strings.Contains(string(b), "SECRET") {
		t.Errorf("the marker after Create: %s, %v", b, err)
	}
}

// The resolver directory is looked up, and passed, only for NetworkUnrestricted.
func TestCreateMakesIndependentSandboxes(t *testing.T) {
	f := newInProc()
	var lookups atomic.Int32
	p := newTestProvider(t, Options{}, f.with, func(s *seams) { s.resolver = func(string) string { lookups.Add(1); return "/run/r" } })
	ctx := context.Background()
	res := make([]sandbox.Sandbox, 6)
	var wg sync.WaitGroup
	for i := range res {
		wg.Go(func() {
			res[i], _ = p.Create(ctx, sandbox.Spec{Network: sandbox.Network{Mode: []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}[i%2]}})
		})
	}
	wg.Wait()
	for _, s := range res {
		if s == nil {
			t.Fatal("a Create failed")
		}
		t.Cleanup(func() { _ = s.Close(ctx) })
	}
	if err := res[0].Close(ctx); err != nil || len(entries(t, p.root)) != 5 || lookups.Load() != 3 {
		t.Errorf("Close = %v; Root holds %v after %d resolver lookups", err, entries(t, p.root), lookups.Load())
	}
	for _, rec := range f.starts() {
		if slices.Contains(rec.args, "/run/r") != slices.Contains(rec.args, "unrestricted") {
			t.Errorf("args %q", rec.args)
		}
	}
	if _, err := res[1].Tools(ctx, sandbox.ToolOptions{}); err != nil {
		t.Errorf("Tools after closing another sandbox: %v", err)
	}
}

// Each failure stops any agent started, removes the directory and says why.
func TestCreateLeavesNothingWhenItFails(t *testing.T) {
	major2 := fakeagent.New()
	major2.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 2, Workdir: "/fake"})
	early := script(t, t.TempDir(), "agentkit-sandboxd", `{ printf HEAD; i=0; while [ $i -lt 1000 ]; do printf 0123456789; i=$((i+1)); done; printf TAIL; } >&2; exit 2`)
	rootOf := func(r *sandbox.LandlockInfo) string { return filepath.Dir(filepath.Dir(r.Writable[0])) }
	report := func(change func(*sandbox.LandlockInfo)) func(*inProc, *seams) {
		return func(f *inProc, _ *seams) { f.report = reportWith(change) }
	}
	for _, c := range []struct {
		name    string
		setup   func(*inProc, *seams)
		spec    sandbox.Spec
		want    string // what the error says
		is      error
		destroy int32 // destroy steps run
	}{
		{"start fails", func(f *inProc, _ *seams) { f.fail = errors.New("cannot start") }, sandbox.Spec{}, "landlock: starting agentkit-sandboxd: cannot start", nil, 0},
		{"protocol 2", func(f *inProc, s *seams) { s.start = f.fakeStarter(major2) }, sandbox.Spec{}, "", sandbox.ErrProtocol, 1},
		{"RequireLandlock", report(func(r *sandbox.LandlockInfo) { r.ABI = 1 }), sandbox.Spec{RequireLandlock: true}, "RequireLandlock", nil, 1},
		{"ABI 3", report(func(r *sandbox.LandlockInfo) { r.ABI, r.KernelABI, r.TCP, r.Note = 3, 3, false, "n3" }), sandbox.Spec{},
			`agentkit-sandboxd reported Landlock ABI 3 (kernel ABI 3), TCP false, note "n3"`, ErrNoLandlock, 1},
		{"no TCP", report(func(r *sandbox.LandlockInfo) { r.TCP = false }), sandbox.Spec{}, "TCP false", ErrNoLandlock, 1},
		{"no tmp", report(func(r *sandbox.LandlockInfo) { r.Writable = r.Writable[:2] }), sandbox.Spec{}, "ABI 4 (kernel ABI 6), TCP true", ErrNoLandlock, 1},
		{"read-only root contains Root", report(func(r *sandbox.LandlockInfo) { r.ReadOnly = append(r.ReadOnly, filepath.Dir(rootOf(r))) }),
			sandbox.Spec{}, "contains Options.Root", nil, 1},
		{"writable root is Root", report(func(r *sandbox.LandlockInfo) { r.Writable = append(r.Writable, rootOf(r)) }),
			sandbox.Spec{}, "choose a Root outside the system directories", nil, 1},
		{"agent exits", func(f *inProc, s *seams) { s.start, f.table.anyPID = defaultStart, true }, sandbox.Spec{}, "landlock: agentkit-sandboxd exited with status 2: ", nil, 1},
	} {
		f := newInProc()
		var removes atomic.Int32
		agent := ""
		if c.name == "agent exits" {
			agent = early
		}
		p := newTestProvider(t, Options{AgentBinary: agent}, f.with, func(s *seams) {
			c.setup(f, s)
			s.remove = func(dir string) error { removes.Add(1); return removeTree(dir) }
		})
		s, err := p.Create(context.Background(), c.spec)
		if s != nil || err == nil || !strings.Contains(err.Error(), c.want) || c.is != nil && !errors.Is(err, c.is) ||
			removes.Load() != c.destroy || len(entries(t, p.root)) != 0 {
			t.Errorf("%s: %v, %v after %d destroy steps, Root %v", c.name, s, err, removes.Load(), entries(t, p.root))
		}
		if c.name == "agent exits" && (strings.Contains(err.Error(), "HEAD") || !strings.HasSuffix(err.Error(), "TAIL") || len(err.Error()) > stderrTail+100) {
			t.Errorf("the stderr tail is %d bytes: ...%s", len(err.Error()), err.Error()[len(err.Error())-20:])
		}
		for pid := 40000; pid < f.pid; pid++ {
			if f.table.alive(pid) {
				t.Errorf("%s: agent %d still runs", c.name, pid)
			}
		}
	}
	f := newInProc()
	entered := make(chan struct{})
	p := newTestProvider(t, Options{}, f.with, func(s *seams) { s.start = f.silentStarter(entered) })
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-entered; cancel() }()
	if s, err := p.Create(ctx, sandbox.Spec{}); s != nil || !errors.Is(err, context.Canceled) || len(entries(t, p.root)) != 0 || f.table.alive(40000) {
		t.Errorf("cancelled: %v, %v; Root %v", s, err, entries(t, p.root))
	}
	f = newInProc()
	f.report = reportWith(func(r *sandbox.LandlockInfo) { r.TCP = false })
	p = newTestProvider(t, Options{}, f.with)
	if s, err := p.Create(context.Background(), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}); err != nil {
		t.Errorf("no TCP under NetworkUnrestricted: %v", err)
	} else {
		_ = s.Close(context.Background())
	}
}
