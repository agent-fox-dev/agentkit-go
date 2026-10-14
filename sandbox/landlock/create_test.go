package landlock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-18-7: concurrent Create calls on one provider yield independent
// sandboxes with distinct ids, directories and agents. 18-REQ-1.6.
func TestConcurrentCreate_TS18_7(t *testing.T) {
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with)
	ctx := context.Background()
	res := make([]sandbox.Sandbox, 8)
	errs := make([]error, 8)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); res[i], errs[i] = p.Create(ctx, sandbox.Spec{}) }()
	}
	wg.Wait()
	ids := map[string]bool{}
	for i, s := range res {
		if errs[i] != nil {
			t.Fatalf("Create %d: %v", i, errs[i])
		}
		t.Cleanup(func() { _ = s.Close(ctx) })
		ids[s.ID()] = true
		if _, err := os.Stat(filepath.Join(p.root, s.ID())); err != nil {
			t.Errorf("sandbox %s has no directory: %v", s.ID(), err)
		}
	}
	if len(ids) != 8 {
		t.Fatalf("%d distinct ids, want 8", len(ids))
	}
	if err := res[0].Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.root, res[0].ID())); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the closed sandbox's directory remains: %v", err)
	}
	for _, s := range res[1:] {
		if _, err := s.Tools(ctx, sandbox.ToolOptions{}); err != nil {
			t.Errorf("Tools on %s after closing another: %v", s.ID(), err)
		}
	}
}

// refusedBeforeAnything asserts that Create refuses spec with a landlock
// SpecError on field, leaving Root empty and starting nothing.
func refusedBeforeAnything(t *testing.T, spec sandbox.Spec, field string) *sandbox.SpecError {
	t.Helper()
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with)
	s, err := p.Create(context.Background(), spec)
	var se *sandbox.SpecError
	if s != nil || !errors.As(err, &se) || se.Provider != "landlock" || se.Field != field || !errors.Is(err, sandbox.ErrUnsupported) {
		t.Errorf("Create(%+v) = %v, %v; want a landlock SpecError on %s", spec, s, err, field)
		return nil
	}
	if names := entries(t, p.root); len(names) != 0 || len(f.starts()) != 0 {
		t.Errorf("Create(%+v) left %v in Root and started %d agents", spec, names, len(f.starts()))
	}
	return se
}

// TS-18-18: Create refuses Resources, NetworkAllowlist, Image and Workdir
// through CheckSpec with a landlock SpecError, before anything exists.
// 18-REQ-3.3.
func TestCreateRefusesUncovered_TS18_18(t *testing.T) {
	for _, c := range []struct {
		spec  sandbox.Spec
		field string
	}{
		{sandbox.Spec{Resources: sandbox.Resources{CPUs: 1}}, "Resources.CPUs"},
		{sandbox.Spec{Resources: sandbox.Resources{MemoryBytes: 1}}, "Resources.MemoryBytes"},
		{sandbox.Spec{Resources: sandbox.Resources{PIDs: 1}}, "Resources.PIDs"},
		{sandbox.Spec{Resources: sandbox.Resources{DiskBytes: 1}}, "Resources.DiskBytes"},
		{sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{{Host: "h", Port: 443}}}}, "Network.Mode"},
		{sandbox.Spec{Image: "x"}, "Image"},
		{sandbox.Spec{Workdir: "/w"}, "Workdir"},
	} {
		_ = refusedBeforeAnything(t, c.spec, c.field)
	}
}

// TS-18-19: Create refuses any Spec.Provider key, naming the first in
// sorted order. 18-REQ-3.4.
func TestCreateRefusesProviderKeys_TS18_19(t *testing.T) {
	if se := refusedBeforeAnything(t, sandbox.Spec{Provider: map[string]any{"zeta": 1, "alpha": 2}}, "Provider"); se != nil && !strings.Contains(se.Error(), "alpha") {
		t.Errorf("%v does not name alpha", se)
	}
}

// TS-18-20: Create refuses a Spec.Env that sets HOME or TMPDIR instead of
// overriding it. 18-REQ-3.5.
func TestCreateRefusesHomeTmpdir_TS18_20(t *testing.T) {
	for _, v := range []string{"HOME", "TMPDIR"} {
		if se := refusedBeforeAnything(t, sandbox.Spec{Env: map[string]string{v: "/x"}}, "Env"); se != nil && (!strings.Contains(se.Reason, v) || !strings.Contains(se.Reason, "private")) {
			t.Errorf("%s: reason %q", v, se.Reason)
		}
	}
}

// TS-18-21: a spec with RequireLandlock, a zero TTL and Labels is accepted
// with a 1h TTL and the labels recorded only in the marker. 18-REQ-3.6.
func TestCreateAccepts_TS18_21(t *testing.T) {
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with)
	s, err := p.Create(context.Background(), sandbox.Spec{RequireLandlock: true, Labels: map[string]string{"team": "lbl-sentinel-7"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if s.Spec().TTL != time.Hour {
		t.Errorf("Spec().TTL = %v", s.Spec().TTL)
	}
	rec := f.starts()[0]
	want := sandbox.AgentArgs(sandbox.Spec{TTL: time.Hour, Workdir: filepath.Join(p.root, s.ID(), "workspace")})
	if len(rec.args) < len(want) || !slices.Equal(rec.args[:len(want)], want) {
		t.Errorf("args %q do not begin with %q", rec.args, want)
	}
	m, err := readMarker(filepath.Join(p.root, s.ID()))
	if err != nil || m.Labels["team"] != "lbl-sentinel-7" {
		t.Errorf("marker %+v, %v", m, err)
	}
	if strings.Contains(strings.Join(append(rec.args, rec.env...), " "), "lbl-sentinel-7") {
		t.Error("a label reached the agent's arguments or environment")
	}
}

// TS-18-29: the resolver directory is the resolved resolv.conf's directory
// only beneath /run or /var/run, and is added only under
// NetworkUnrestricted. 18-REQ-5.2.
func TestResolverDir_TS18_29(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(regular, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "missing"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{regular, dangling, filepath.Join(dir, "missing")} {
		if got := resolverDir(p); got != "" {
			t.Errorf("resolverDir(%s) = %q, want none", p, got)
		}
	}
	for p, want := range map[string]string{
		"/run/systemd/resolve/stub-resolv.conf": "/run/systemd/resolve", "/var/run/foo/resolv.conf": "/var/run/foo",
		"/run/resolv.conf": "", "/var/run/resolv.conf": "", "/etc/resolv.conf": "", "/runx/a/resolv.conf": "",
	} {
		if got := classifyResolver(p); got != want {
			t.Errorf("classifyResolver(%s) = %q, want %q", p, got, want)
		}
	}
	if onLinux() {
		if _, err := os.Stat("/run/systemd/resolve/stub-resolv.conf"); err == nil {
			link := filepath.Join(dir, "link")
			if err := os.Symlink("/run/systemd/resolve/stub-resolv.conf", link); err != nil {
				t.Fatal(err)
			}
			if got := resolverDir(link); got != "/run/systemd/resolve" {
				t.Errorf("resolverDir(link to stub-resolv.conf) = %q", got)
			}
		}
	}
	f := newInProc(t)
	p := newTestProvider(t, Options{}, f.with, func(s *seams) { s.resolver = func(string) string { return "/run/systemd/resolve" } })
	for _, mode := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted} {
		s, err := p.Create(context.Background(), sandbox.Spec{Network: sandbox.Network{Mode: mode}})
		if err != nil {
			t.Fatal(err)
		}
		_ = s.Close(context.Background())
	}
	recs := f.starts()
	has := func(args []string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--landlock-ro" && args[i+1] == "/run/systemd/resolve" {
				return true
			}
		}
		return false
	}
	if has(recs[0].args) || !has(recs[1].args) {
		t.Errorf("resolver flag: NetworkNone %t, NetworkUnrestricted %t; want false, true", has(recs[0].args), has(recs[1].args))
	}
}

// TS-18-33: when the agent's report passes every check Create returns the
// provider's wrapper with the host workspace as Workdir. 18-REQ-5.5.
func TestCreateReturnsWrapper_TS18_33(t *testing.T) {
	f := newInProc(t)
	var served sandbox.LandlockInfo
	f.report = func(dir string) sandbox.LandlockInfo { served = goodReport(dir); return served }
	p := newTestProvider(t, Options{}, f.with)
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, ok := s.(*landlockSandbox); !ok {
		t.Errorf("Create returned %T, want *landlockSandbox", s)
	}
	if want := filepath.Join(p.root, s.ID(), "workspace"); s.Spec().Workdir != want {
		t.Errorf("Spec().Workdir = %s, want %s", s.Spec().Workdir, want)
	}
	if !reflect.DeepEqual(s.Agent().Landlock, served) {
		t.Errorf("Agent().Landlock = %+v, want %+v", s.Agent().Landlock, served)
	}
}
