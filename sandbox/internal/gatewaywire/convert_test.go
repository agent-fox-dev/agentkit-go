package gatewaywire

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// genMap is nil or a map of up to four entries.
func genMap(r *rand.Rand, prefix string) map[string]string {
	n := r.IntN(5)
	if n == 0 {
		return nil
	}
	m := map[string]string{}
	for i := range n {
		m[fmt.Sprintf("%s%d", prefix, i)] = fmt.Sprintf("v%d-%d", r.IntN(100), i)
	}
	return m
}

// genProvider is nil or a map whose values encode as JSON: nil, a number, a
// string, a slice or a map.
func genProvider(r *rand.Rand) map[string]any {
	n := r.IntN(4)
	if n == 0 {
		return nil
	}
	m := map[string]any{}
	for i := range n {
		var v any
		switch r.IntN(5) {
		case 0:
			v = nil
		case 1:
			v = r.Float64() * 1000
		case 2:
			v = fmt.Sprintf("s%d", r.IntN(100))
		case 3:
			v = []any{"a", float64(r.IntN(9))}
		default:
			v = map[string]any{"k": "v", "n": float64(r.IntN(9))}
		}
		m[fmt.Sprintf("p%d", i)] = v
	}
	return m
}

// genSpec is a random Spec; collections are nil rather than empty, because
// proto cannot tell the two apart.
func genSpec(r *rand.Rand) sandbox.Spec {
	s := sandbox.Spec{
		Image:   fmt.Sprintf("img%d", r.IntN(10)),
		Workdir: fmt.Sprintf("/w%d", r.IntN(10)),
		Env:     genMap(r, "E"),
		Resources: sandbox.Resources{CPUs: float64(r.IntN(8)) / 2, MemoryBytes: r.Int64N(1 << 40),
			PIDs: r.Int64N(1000), DiskBytes: r.Int64N(1 << 40)},
		Network:         sandbox.Network{Mode: sandbox.NetworkMode(r.IntN(3))},
		TTL:             time.Duration(r.Int64N(int64(48 * time.Hour))),
		Labels:          genMap(r, "l"),
		Provider:        genProvider(r),
		RequireLandlock: r.IntN(2) == 0,
	}
	if s.Network.Mode == sandbox.NetworkAllowlist {
		for i := range r.IntN(3) {
			s.Network.Allow = append(s.Network.Allow, sandbox.Endpoint{Host: fmt.Sprintf("h%d.example", i), Port: 1 + r.IntN(65535)})
		}
	}
	return s
}

// genCaps is a random Capabilities.
func genCaps(r *rand.Rand) sandbox.Capabilities {
	c := sandbox.Capabilities{
		Provider:  fmt.Sprintf("prov%d", r.IntN(10)),
		Isolation: sandbox.Isolation(r.IntN(4)),
		Resources: sandbox.ResourceCaps{CPUs: r.IntN(2) == 0, MemoryBytes: r.IntN(2) == 0, PIDs: r.IntN(2) == 0, DiskBytes: r.IntN(2) == 0},
		Image:     r.IntN(2) == 0, Workdir: r.IntN(2) == 0, Landlock: r.IntN(2) == 0,
		Notes: fmt.Sprintf("notes %d", r.IntN(100)),
	}
	for _, m := range []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted} {
		if r.IntN(2) == 0 {
			c.Network = append(c.Network, m)
		}
	}
	return c
}

// jsonEqual reports whether a and b encode to the same JSON.
func jsonEqual(t *testing.T, a, b map[string]any) bool {
	t.Helper()
	ja, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return string(ja) == string(jb)
}

// TS-19-3: Spec and Capabilities survive a round trip through the wire
// messages unchanged. 19-REQ-1.3.
func TestRoundTrip_TS19_3(t *testing.T) {
	for seed := range uint64(501) {
		r := rand.New(rand.NewPCG(seed, 19))
		s := genSpec(r)
		pb, err := SpecToPB(s)
		if err != nil {
			t.Fatalf("seed %d: SpecToPB: %v", seed, err)
		}
		got, err := SpecFromPB(pb)
		if err != nil {
			t.Fatalf("seed %d: SpecFromPB: %v", seed, err)
		}
		if !jsonEqual(t, got.Provider, s.Provider) {
			t.Fatalf("seed %d: Provider %v, want %v", seed, got.Provider, s.Provider)
		}
		got.Provider, s.Provider = nil, nil
		if !reflect.DeepEqual(got, s) {
			t.Fatalf("seed %d: spec round trip\n got %+v\nwant %+v", seed, got, s)
		}
		c := genCaps(r)
		if gc := CapsFromPB(CapsToPB(c)); !reflect.DeepEqual(gc, c) {
			t.Fatalf("seed %d: capabilities round trip\n got %+v\nwant %+v", seed, gc, c)
		}
	}
}
