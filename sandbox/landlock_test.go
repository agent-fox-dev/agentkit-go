package sandbox_test

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-15-22: ProtocolMinor is 2 with major unchanged, a fresh sandbox reports
// protocol 1.2, and attach still accepts an agent of 1.0 or 1.1. 15-REQ-4.2.
func TestProtocolMinorTwo_TS15_22(t *testing.T) {
	if sandbox.ProtocolMajor != 1 || sandbox.ProtocolMinor != 2 {
		t.Errorf("protocol %d.%d, want 1.2", sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	}
	isolate(t)
	sb := newLocal(t)
	if a := sb.Agent(); a.ProtocolMajor != 1 || a.ProtocolMinor != 2 {
		t.Errorf("Agent() reports %d.%d, want 1.2", a.ProtocolMajor, a.ProtocolMinor)
	}
	for _, minor := range []uint32{0, 1} {
		f := fakeagent.New()
		f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: minor, Version: "v", Workdir: "/ws"})
		if sb := fakeSandbox(t, f); sb.Agent().ProtocolMinor != minor {
			t.Errorf("a 1.%d agent was not attached as 1.%d", minor, minor)
		}
	}
}

// TS-15-23: package sandbox exports LandlockInfo with its six fields,
// LandlockMinABI 2, LandlockTCPABI 4 and AgentInfo.Landlock. 15-REQ-4.3.
func TestLandlockInfoShape_TS15_23(t *testing.T) {
	want := []struct {
		name string
		typ  reflect.Type
	}{{"ABI", reflect.TypeFor[int]()}, {"KernelABI", reflect.TypeFor[int]()}, {"TCP", reflect.TypeFor[bool]()},
		{"Note", reflect.TypeFor[string]()}, {"Writable", reflect.TypeFor[[]string]()}, {"ReadOnly", reflect.TypeFor[[]string]()}}
	lt := reflect.TypeFor[sandbox.LandlockInfo]()
	if lt.NumField() != len(want) {
		t.Fatalf("LandlockInfo has %d fields, want %d", lt.NumField(), len(want))
	}
	for i, w := range want {
		if f := lt.Field(i); f.Name != w.name || f.Type != w.typ {
			t.Errorf("field %d is %s %v, want %s %v", i, f.Name, f.Type, w.name, w.typ)
		}
	}
	if sandbox.LandlockMinABI != 2 || sandbox.LandlockTCPABI != 4 {
		t.Errorf("LandlockMinABI %d, LandlockTCPABI %d; want 2 and 4", sandbox.LandlockMinABI, sandbox.LandlockTCPABI)
	}
	if f, ok := reflect.TypeFor[sandbox.AgentInfo]().FieldByName("Landlock"); !ok || f.Type != lt {
		t.Errorf("AgentInfo.Landlock is %v, %v", f.Type, ok)
	}
}

// TS-15-24: for any LandlockInfo and network mode, Satisfies holds exactly
// when ABI is at least 2 and, under none, TCP is set. 15-REQ-4.4.
func TestSatisfies_TS15_24(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 24))
	modes := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted}
	for range 2000 {
		l := sandbox.LandlockInfo{ABI: r.IntN(12) - 1, KernelABI: r.IntN(11), TCP: r.IntN(2) == 0, Note: "n"}
		if r.IntN(2) == 0 {
			l.Writable, l.ReadOnly = []string{"/w"}, []string{"/usr"}
		}
		for _, m := range modes {
			want := l.ABI >= sandbox.LandlockMinABI && (m != sandbox.NetworkNone || l.TCP)
			if got := l.Satisfies(m); got != want {
				t.Fatalf("%+v.Satisfies(%v) = %v, want %v", l, m, got, want)
			}
			other := l
			other.KernelABI, other.Note, other.Writable, other.ReadOnly = 99, "other", nil, []string{"/x"}
			if other.Satisfies(m) != l.Satisfies(m) {
				t.Fatalf("KernelABI, Note or the lists changed the result for %+v", l)
			}
		}
	}
	if (sandbox.LandlockInfo{ABI: 4}).Satisfies(sandbox.NetworkNone) {
		t.Error("{ABI 4, TCP false} satisfies none")
	}
	for _, m := range modes[1:] {
		if !(sandbox.LandlockInfo{ABI: 2}).Satisfies(m) {
			t.Errorf("{ABI 2, TCP false} fails %v", m)
		}
	}
	for _, m := range modes {
		if (sandbox.LandlockInfo{ABI: 1, TCP: true}).Satisfies(m) {
			t.Errorf("{ABI 1, TCP true} satisfies %v", m)
		}
	}
}
