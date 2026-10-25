package gatewaywire

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func TestWireRoundTripKeepsTheSpec(t *testing.T) {
	for name, s := range map[string]sandbox.Spec{
		"zero": {},
		"full": {
			Image: "img", Workdir: "/w",
			Env:       map[string]string{"A": "1", "B": "2"},
			Resources: sandbox.Resources{CPUs: 1.5, MemoryBytes: 1 << 30, PIDs: 100, DiskBytes: 1 << 33},
			Network: sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: []sandbox.Endpoint{
				{Host: "a.example", Port: 443}, {Host: "b.example", Port: 65535}}},
			TTL:             36 * time.Hour,
			Labels:          map[string]string{"l": "v"},
			Provider:        map[string]any{"n": 2.5, "s": "x", "list": []any{"a", 1.0}, "obj": map[string]any{"k": "v"}, "nil": nil},
			RequireLandlock: true,
		},
		"unrestricted": {Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}},
	} {
		pb, err := SpecToPB(s)
		if err != nil {
			t.Fatalf("%s: SpecToPB: %v", name, err)
		}
		got, err := SpecFromPB(pb)
		if err != nil {
			t.Fatalf("%s: SpecFromPB: %v", name, err)
		}
		if !reflect.DeepEqual(got, s) {
			t.Errorf("%s: round trip\n got %+v\nwant %+v", name, got, s)
		}
	}
}

// Proto cannot tell an empty collection from a nil one; the decoded spec has nil.
func TestEmptyCollectionsComeBackNil(t *testing.T) {
	pb, err := SpecToPB(sandbox.Spec{Env: map[string]string{}, Labels: map[string]string{}, Provider: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(pb.GetProvider()) != 0 {
		t.Errorf("an empty Provider encodes as %q, want no bytes", pb.GetProvider())
	}
	pb.Provider = []byte("{}")
	got, err := SpecFromPB(pb)
	if err != nil {
		t.Fatal(err)
	}
	if got.Env != nil || got.Labels != nil || got.Provider != nil {
		t.Errorf("SpecFromPB = %+v, want nil collections", got)
	}
}

func TestEncodingRefusesAProviderThatIsNotJSON(t *testing.T) {
	_, err := SpecToPB(sandbox.Spec{Provider: map[string]any{"f": func() {}}})
	if err == nil || !strings.Contains(err.Error(), "Provider") {
		t.Errorf("SpecToPB = %v, want an error naming Provider", err)
	}
}

func TestDecodingRefusesAnUnknownModeOrAProviderThatIsNotAnObject(t *testing.T) {
	for name, pb := range map[string]*sandboxpb.SandboxSpec{
		"mode":     {Network: &sandboxpb.Network{Mode: 9}},
		"provider": {Provider: []byte("[1]")},
	} {
		if _, err := SpecFromPB(pb); err == nil {
			t.Errorf("%s: SpecFromPB accepted %v", name, pb)
		}
	}
}

func TestCapabilitiesSurviveARoundTripThroughTheWire(t *testing.T) {
	for _, c := range []sandbox.Capabilities{
		{},
		{
			Provider: "prov", Isolation: sandbox.Isolation(3),
			Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
			Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted},
			Image:     true, Workdir: true, Landlock: true, Notes: "notes",
		},
	} {
		if got := CapsFromPB(CapsToPB(c)); !reflect.DeepEqual(got, c) {
			t.Errorf("round trip\n got %+v\nwant %+v", got, c)
		}
	}
}

func TestCapsFromPBLeavesOutAnUnknownMode(t *testing.T) {
	got := CapsFromPB(&sandboxpb.CapabilitiesResponse{Network: []sandboxpb.NetworkMode{9, sandboxpb.NetworkMode_NONE}})
	if !reflect.DeepEqual(got.Network, []sandbox.NetworkMode{sandbox.NetworkNone}) {
		t.Errorf("Network = %v, want [none]", got.Network)
	}
}

func TestNanosRoundTripsATime(t *testing.T) {
	at := time.Date(2026, 10, 25, 1, 2, 3, 456, time.UTC)
	if got := FromNanos(Nanos(at)); !got.Equal(at) {
		t.Errorf("FromNanos(Nanos(%v)) = %v", at, got)
	}
}
