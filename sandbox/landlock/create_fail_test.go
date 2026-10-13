package landlock

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// major2 is a fake agent whose Hello reports protocol major 2.
func major2() *fakeagent.Agent {
	fa := fakeagent.New()
	fa.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 2, Workdir: "/fake"})
	return fa
}

// reportWith is a starter report: goodReport with change applied.
func reportWith(change func(*sandbox.LandlockInfo)) func(string) sandbox.LandlockInfo {
	return func(dir string) sandbox.LandlockInfo {
		r := goodReport(dir)
		change(&r)
		return r
	}
}

// TS-18-27: every Create failure after the sandbox directory exists
// removes it before returning. 18-REQ-4.5.
func TestCreateFailureRemovesDir_TS18_27(t *testing.T) {
	for name, setup := range map[string]func(f *inProc) func(*seams){
		"starter fails": func(f *inProc) func(*seams) { f.fail = errors.New("cannot start"); return f.with },
		"protocol 2": func(f *inProc) func(*seams) {
			return func(s *seams) { f.with(s); s.start = f.fakeAgentStarter(major2()) }
		},
		"ABI 3": func(f *inProc) func(*seams) {
			f.report = reportWith(func(r *sandbox.LandlockInfo) { r.ABI = 3 })
			return f.with
		},
	} {
		f := newInProc(t)
		p := newTestProvider(t, Options{}, setup(f))
		s, err := p.Create(context.Background(), sandbox.Spec{})
		if s != nil || err == nil {
			t.Errorf("%s: Create = %v, %v; want a failure", name, s, err)
		}
		if got := entries(t, p.root); len(got) != 0 {
			t.Errorf("%s: Root holds %v", name, got)
		}
	}
}

// TS-18-34: a failing attach is classified: protocol and RequireLandlock
// errors pass through, a done context wraps ctx.Err(), and nothing is left.
// 18-REQ-5.6.
func TestCreateClassifies_TS18_34(t *testing.T) {
	check := func(name string, f *inProc, p *provider, removes *atomic.Int32, s sandbox.Sandbox) {
		t.Helper()
		if s != nil || removes.Load() != 1 || len(entries(t, p.root)) != 0 {
			t.Errorf("%s: sandbox %v, destroy ran %d times, Root %v", name, s, removes.Load(), entries(t, p.root))
		}
		for pid := 40000; pid < f.nextPID; pid++ {
			if live := f.table.session(pid); len(live) != 0 {
				t.Errorf("%s: session %d has %v", name, pid, live)
			}
		}
	}

	f := newInProc(t)
	var removes atomic.Int32
	p := newTestProvider(t, Options{}, func(s *seams) { f.with(s); s.start = f.fakeAgentStarter(major2()) }, countRemove(&removes))
	s, err := p.Create(context.Background(), sandbox.Spec{})
	if !errors.Is(err, sandbox.ErrProtocol) || strings.Contains(fmt.Sprint(err), "exited") {
		t.Errorf("protocol 2: %v", err)
	}
	check("protocol 2", f, p, &removes, s)

	f = newInProc(t)
	f.report = reportWith(func(r *sandbox.LandlockInfo) { r.ABI = 1 })
	removes.Store(0)
	p = newTestProvider(t, Options{}, f.with, countRemove(&removes))
	s, err = p.Create(context.Background(), sandbox.Spec{RequireLandlock: true})
	var se *sandbox.SpecError
	if !errors.As(err, &se) || se.Field != "RequireLandlock" {
		t.Errorf("RequireLandlock: %v", err)
	}
	check("RequireLandlock", f, p, &removes, s)

	f = newInProc(t)
	entered := make(chan struct{})
	removes.Store(0)
	p = newTestProvider(t, Options{}, func(s *seams) { f.with(s); s.start = f.silentStarter(entered) }, countRemove(&removes))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-entered; cancel() }()
	s, err = p.Create(ctx, sandbox.Spec{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	check("cancelled", f, p, &removes, s)
}

// TS-18-35: a report below the ABI 4 floor, without TCP under NetworkNone,
// or missing a sandbox subdirectory closes the sandbox and wraps
// ErrNoLandlock. 18-REQ-5.7.
func TestCreateReportFloor_TS18_35(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(*sandbox.LandlockInfo)
	}{
		{"ABI 3", func(r *sandbox.LandlockInfo) { r.ABI, r.KernelABI, r.TCP, r.Note = 3, 3, false, "abi3-note" }},
		{"no TCP", func(r *sandbox.LandlockInfo) { r.ABI, r.KernelABI, r.TCP, r.Note = 4, 5, false, "tcp-note" }},
		{"no tmp", func(r *sandbox.LandlockInfo) {
			r.Note = "w-note"
			r.Writable = r.Writable[:2]
		}},
	} {
		f := newInProc(t)
		var served sandbox.LandlockInfo
		f.report = func(dir string) sandbox.LandlockInfo { served = reportWith(c.change)(dir); return served }
		var removes atomic.Int32
		p := newTestProvider(t, Options{}, f.with, countRemove(&removes))
		s, err := p.Create(context.Background(), sandbox.Spec{})
		if s != nil || !errors.Is(err, ErrNoLandlock) {
			t.Fatalf("%s: %v, %v; want ErrNoLandlock", c.name, s, err)
		}
		for _, want := range []string{"ABI " + strconv.Itoa(served.ABI), "kernel ABI " + strconv.Itoa(served.KernelABI), "TCP " + strconv.FormatBool(served.TCP), served.Note} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %q lacks %q", c.name, err, want)
			}
		}
		if removes.Load() != 1 || len(entries(t, p.root)) != 0 {
			t.Errorf("%s: destroy ran %d times, Root %v", c.name, removes.Load(), entries(t, p.root))
		}
	}
	f := newInProc(t)
	f.report = reportWith(func(r *sandbox.LandlockInfo) { r.TCP = false })
	p := newTestProvider(t, Options{}, f.with)
	s, err := p.Create(context.Background(), sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}})
	if err != nil {
		t.Fatalf("no TCP under NetworkUnrestricted: %v", err)
	}
	_ = s.Close(context.Background())
}

// TS-18-36: a reported root that equals or contains Root closes the sandbox
// with an error naming Root and the containing root. 18-REQ-5.8.
func TestCreateRootContained_TS18_36(t *testing.T) {
	for _, c := range []struct {
		name       string
		containing func(root string) string
		ro         bool
	}{
		{"ReadOnly parent", func(root string) string { return filepath.Dir(root) }, true},
		{"Writable Root", func(root string) string { return root }, false},
	} {
		f := newInProc(t)
		var removes atomic.Int32
		var p *provider
		f.report = reportWith(func(r *sandbox.LandlockInfo) {
			if c.ro {
				r.ReadOnly = append(r.ReadOnly, c.containing(p.root))
			} else {
				r.Writable = append(r.Writable, c.containing(p.root))
			}
		})
		p = newTestProvider(t, Options{}, f.with, countRemove(&removes))
		s, err := p.Create(context.Background(), sandbox.Spec{})
		if s != nil || err == nil || !strings.Contains(err.Error(), p.root) || !strings.Contains(err.Error(), c.containing(p.root)) ||
			!strings.Contains(err.Error(), "outside the system directories") || removes.Load() != 1 {
			t.Errorf("%s: %v, %v after %d destroys", c.name, s, err, removes.Load())
		}
	}
}
