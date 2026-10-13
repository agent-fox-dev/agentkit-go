package attach

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// landlockAgent is a fake agent answering protocol 1.major.minor with report r
// (nil sends none).
func landlockAgent(minor uint32, r *sandboxpb.LandlockReport) *fakeagent.Agent {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: minor, Version: "v", Workdir: "/ws", Landlock: r})
	return f
}

// requireSpec is an effective spec with RequireLandlock under mode.
func requireSpec(mode sandbox.NetworkMode) sandbox.Spec {
	return sandbox.Spec{RequireLandlock: true, Network: sandbox.Network{Mode: mode}, TTL: time.Hour}
}

// refused attaches to f with spec and checks the RequireLandlock refusal:
// a nil Sandbox, a SpecError from provider "fake" matching ErrUnsupported,
// one destroy, and the connection closed. It returns the error.
func refused(t *testing.T, f *fakeagent.Agent, spec sandbox.Spec) error {
	t.Helper()
	destroyed := 0
	sb, err := Dial(context.Background(), f.Serve(), Config{Provider: "fake", Spec: spec,
		Destroy: func(context.Context) error { destroyed++; return nil }})
	t.Cleanup(f.Stop)
	var se *sandbox.SpecError
	if sb != nil || !errors.Is(err, sandbox.ErrUnsupported) || !errors.As(err, &se) || se.Provider != "fake" || se.Field != "RequireLandlock" {
		t.Errorf("Dial = %v, %v; want nil and a RequireLandlock SpecError from fake", sb, err)
	}
	if destroyed != 1 {
		t.Errorf("the destroy step ran %d times, want 1", destroyed)
	}
	select {
	case <-f.Closed():
	case <-time.After(5 * time.Second):
		t.Error("the client did not close its connection")
	}
	return err
}

// TS-15-36: attach refuses a RequireLandlock sandbox whose agent reports ABI
// 0, or ABI 4 without TCP under none, closing the client and destroying it
// once. 15-REQ-6.5, 15-REQ-6.8.
func TestRequireLandlockRefused_TS15_36(t *testing.T) {
	note := "kernel Landlock ABI 1 cannot permit cross-directory renames (needs ABI 2, Linux 5.19); not applied"
	err := refused(t, landlockAgent(2, &sandboxpb.LandlockReport{Abi: 0, KernelAbi: 1, Note: note}), requireSpec(sandbox.NetworkNone))
	var se *sandbox.SpecError
	if errors.As(err, &se) {
		for _, want := range []string{"ABI 0", "kernel ABI 1", "TCP false", note} {
			if !strings.Contains(se.Reason, want) {
				t.Errorf("reason %q lacks %q", se.Reason, want)
			}
		}
	}
	err = refused(t, landlockAgent(2, &sandboxpb.LandlockReport{Abi: 4, KernelAbi: 6, Note: "x"}), requireSpec(sandbox.NetworkNone))
	if errors.As(err, &se) {
		for _, want := range []string{"ABI 4", "kernel ABI 6", "TCP false", "note: x"} {
			if !strings.Contains(se.Reason, want) {
				t.Errorf("reason %q lacks %q", se.Reason, want)
			}
		}
	}
}

// TS-15-37: attach accepts a RequireLandlock sandbox whose agent's report
// satisfies the effective network mode. 15-REQ-6.6.
func TestRequireLandlockAccepted_TS15_37(t *testing.T) {
	for _, c := range []struct {
		r    *sandboxpb.LandlockReport
		mode sandbox.NetworkMode
		want sandbox.LandlockInfo
	}{
		{&sandboxpb.LandlockReport{Abi: 3}, sandbox.NetworkUnrestricted, sandbox.LandlockInfo{ABI: 3}},
		{&sandboxpb.LandlockReport{Abi: 4, KernelAbi: 6, Tcp: true}, sandbox.NetworkNone, sandbox.LandlockInfo{ABI: 4, KernelABI: 6, TCP: true}},
		{&sandboxpb.LandlockReport{Abi: 2}, sandbox.NetworkAllowlist, sandbox.LandlockInfo{ABI: 2}},
	} {
		f := landlockAgent(2, c.r)
		destroyed := 0
		sb := attachFake(t, f, Config{Provider: "fake", Spec: requireSpec(c.mode),
			Destroy: func(context.Context) error { destroyed++; return nil }})
		if _, err := sb.Tools(context.Background(), sandbox.ToolOptions{}); err != nil || f.Count("Tools") != 1 {
			t.Errorf("%v: Tools = %v, count %d", c.mode, err, f.Count("Tools"))
		}
		if destroyed != 0 {
			t.Errorf("%v: destroyed", c.mode)
		}
		if got := sb.Agent().Landlock; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: Agent().Landlock = %+v, want %+v", c.mode, got, c.want)
		}
	}
}

// TS-15-38: attach refuses RequireLandlock against an agent of protocol 1.1
// or 1.0 with an error naming its protocol version, and destroys it once.
// 15-REQ-6.7, 15-REQ-6.8.
func TestRequireLandlockOldAgent_TS15_38(t *testing.T) {
	for _, minor := range []uint32{1, 0} {
		err := refused(t, landlockAgent(minor, nil), requireSpec(sandbox.NetworkUnrestricted))
		if want := "1." + string(rune('0'+minor)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("1.%d: %v does not name %s", minor, err, want)
		}
	}
}
