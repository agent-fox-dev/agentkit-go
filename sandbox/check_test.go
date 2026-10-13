package sandbox_test

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

var (
	unrestricted = sandbox.Network{Mode: sandbox.NetworkUnrestricted}
	localCaps    = sandbox.Capabilities{Provider: "local", Network: []sandbox.NetworkMode{sandbox.NetworkUnrestricted}}
	fullCaps     = sandbox.Capabilities{
		Provider:  "full",
		Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted},
		Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
		Image:     true, Workdir: true,
	}
)

// TS-13-9: CheckSpec fills TTL 0 with one hour and Workdir with /workspace
// only when the provider honours Workdir. 13-REQ-2.2.
func TestCheckSpecDefaults_TS13_9(t *testing.T) {
	capsWD := localCaps
	capsWD.Workdir = true
	spec := sandbox.Spec{Network: unrestricted, Env: map[string]string{"A": "1"}, Labels: map[string]string{"team": "x"}}

	s, err := sandbox.CheckSpec(capsWD, spec)
	if err != nil || s.TTL != time.Hour || s.Workdir != "/workspace" {
		t.Fatalf("Workdir caps: %+v, %v", s, err)
	}
	if !reflect.DeepEqual(s.Env, spec.Env) || !reflect.DeepEqual(s.Labels, spec.Labels) || !reflect.DeepEqual(s.Network, spec.Network) {
		t.Errorf("fields changed: %+v", s)
	}
	s, err = sandbox.CheckSpec(localCaps, spec)
	if err != nil || s.TTL != time.Hour || s.Workdir != "" {
		t.Fatalf("no-Workdir caps: %+v, %v", s, err)
	}
	spec.TTL, spec.Workdir = 5*time.Minute, "/src"
	s, err = sandbox.CheckSpec(capsWD, spec)
	if err != nil || s.TTL != 5*time.Minute || s.Workdir != "/src" {
		t.Fatalf("explicit values: %+v, %v", s, err)
	}
	if !reflect.DeepEqual(s, spec) {
		t.Errorf("CheckSpec changed explicit fields: %+v, want %+v", s, spec)
	}
}

// TS-13-10: CheckSpec refuses each field outside the capabilities with a
// *SpecError matching ErrUnsupported and the documented message.
// 13-REQ-2.3.
func TestCheckSpecUnsupported_TS13_10(t *testing.T) {
	cases := []struct {
		field string
		spec  sandbox.Spec
	}{
		{"Network.Mode", sandbox.Spec{}},
		{"Network.Mode", sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkAllowlist}}},
		{"Resources.CPUs", sandbox.Spec{Network: unrestricted, Resources: sandbox.Resources{CPUs: 1}}},
		{"Resources.MemoryBytes", sandbox.Spec{Network: unrestricted, Resources: sandbox.Resources{MemoryBytes: 1}}},
		{"Resources.PIDs", sandbox.Spec{Network: unrestricted, Resources: sandbox.Resources{PIDs: 1}}},
		{"Resources.DiskBytes", sandbox.Spec{Network: unrestricted, Resources: sandbox.Resources{DiskBytes: 1}}},
		{"Image", sandbox.Spec{Network: unrestricted, Image: "alpine"}},
		{"Workdir", sandbox.Spec{Network: unrestricted, Workdir: "/w"}},
	}
	for _, tc := range cases {
		s, err := sandbox.CheckSpec(localCaps, tc.spec)
		if !reflect.DeepEqual(s, sandbox.Spec{}) {
			t.Errorf("%s: returned spec %+v, want the zero Spec", tc.field, s)
		}
		var se *sandbox.SpecError
		if !errors.As(err, &se) || se.Provider != "local" || se.Field != tc.field {
			t.Errorf("%s: error %v (%#v), want a *SpecError for local naming the field", tc.field, err, se)
		}
		if !errors.Is(err, sandbox.ErrUnsupported) {
			t.Errorf("%s: %v does not match ErrUnsupported", tc.field, err)
		}
	}
	_, err := sandbox.CheckSpec(localCaps, sandbox.Spec{})
	const prefix = `sandbox: provider "local" cannot honour Spec.Network.Mode = none: `
	if !strings.HasPrefix(err.Error(), prefix) || len(err.Error()) == len(prefix) {
		t.Errorf("zero-Spec message %q, want %q and a reason", err, prefix)
	}
}

// TS-13-11: CheckSpec refuses values no provider accepts, even under
// capabilities that cover every field. 13-REQ-2.4.
func TestCheckSpecInvalid_TS13_11(t *testing.T) {
	allow := func(eps ...sandbox.Endpoint) sandbox.Network {
		return sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: eps}
	}
	cases := []struct {
		field string
		spec  sandbox.Spec
	}{
		{"TTL", sandbox.Spec{TTL: -time.Second}},
		{"Resources.CPUs", sandbox.Spec{Resources: sandbox.Resources{CPUs: -1}}},
		{"Resources.MemoryBytes", sandbox.Spec{Resources: sandbox.Resources{MemoryBytes: -1}}},
		{"Resources.PIDs", sandbox.Spec{Resources: sandbox.Resources{PIDs: -1}}},
		{"Resources.DiskBytes", sandbox.Spec{Resources: sandbox.Resources{DiskBytes: -1}}},
		{"Env", sandbox.Spec{Env: map[string]string{"": "v"}}},
		{"Env", sandbox.Spec{Env: map[string]string{"A=B": "v"}}},
		{"Env", sandbox.Spec{Env: map[string]string{"A\x00": "v"}}},
		{"Env", sandbox.Spec{Env: map[string]string{"A": "v\x00"}}},
		{"Labels", sandbox.Spec{Labels: map[string]string{"agentkit.sandbox": "x"}}},
		{"Network.Allow", sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted, Allow: []sandbox.Endpoint{{"example.com", 443}}}}},
		{"Network.Allow[0].Host", sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "", Port: 443})}},
		{"Network.Allow[0].Port", sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "example.com", Port: 0})}},
		{"Network.Allow[0].Port", sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "example.com", Port: 65536})}},
	}
	for _, tc := range cases {
		s, err := sandbox.CheckSpec(fullCaps, tc.spec)
		if err == nil || !reflect.DeepEqual(s, sandbox.Spec{}) {
			t.Errorf("%s: (%+v, %v), want the zero Spec and an error", tc.field, s, err)
			continue
		}
		if !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: message %q does not name the field", tc.field, err)
		}
	}
	if _, err := sandbox.CheckSpec(fullCaps, sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "example.com", Port: 443})}); err != nil {
		t.Errorf("a valid allowlist spec was refused: %v", err)
	}
}

// TS-13-13: CheckSpec returns ErrUnsupported exactly when the spec sets a
// field the capabilities do not cover. 13-REQ-2.7.
func TestCheckSpecProperty_TS13_13(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 13))
	modes := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted}
	for range 2000 {
		var caps sandbox.Capabilities
		for _, m := range modes {
			if r.IntN(2) == 0 {
				caps.Network = append(caps.Network, m)
			}
		}
		caps.Provider = "p"
		caps.Resources = sandbox.ResourceCaps{CPUs: r.IntN(2) == 0, MemoryBytes: r.IntN(2) == 0, PIDs: r.IntN(2) == 0, DiskBytes: r.IntN(2) == 0}
		caps.Image, caps.Workdir = r.IntN(2) == 0, r.IntN(2) == 0

		var spec sandbox.Spec
		spec.Network.Mode = modes[r.IntN(3)]
		if spec.Network.Mode == sandbox.NetworkAllowlist && r.IntN(2) == 0 {
			spec.Network.Allow = []sandbox.Endpoint{{Host: "example.com", Port: 1 + r.IntN(65535)}}
		}
		pick := func() int64 { return int64(r.IntN(2) * (1 + r.IntN(8))) }
		spec.Resources = sandbox.Resources{CPUs: float64(pick()), MemoryBytes: pick(), PIDs: pick(), DiskBytes: pick()}
		if r.IntN(2) == 0 {
			spec.Image = "img"
		}
		if r.IntN(2) == 0 {
			spec.Workdir = "/w"
		}
		spec.TTL = time.Duration(r.IntN(3)) * time.Minute
		if r.IntN(2) == 0 {
			spec.Env = map[string]string{"K": "v"}
		}

		uncovered := !slices.Contains(caps.Network, spec.Network.Mode) ||
			(spec.Resources.CPUs != 0 && !caps.Resources.CPUs) ||
			(spec.Resources.MemoryBytes != 0 && !caps.Resources.MemoryBytes) ||
			(spec.Resources.PIDs != 0 && !caps.Resources.PIDs) ||
			(spec.Resources.DiskBytes != 0 && !caps.Resources.DiskBytes) ||
			(spec.Image != "" && !caps.Image) || (spec.Workdir != "" && !caps.Workdir)

		out, err := sandbox.CheckSpec(caps, spec)
		if errors.Is(err, sandbox.ErrUnsupported) != uncovered {
			t.Fatalf("caps %+v spec %+v: err %v, oracle uncovered=%v", caps, spec, err, uncovered)
		}
		if err != nil && !uncovered {
			t.Fatalf("caps %+v spec %+v: unexpected error %v", caps, spec, err)
		}
		if err == nil {
			want := spec
			if want.TTL == 0 {
				want.TTL = time.Hour
			}
			if caps.Workdir && want.Workdir == "" {
				want.Workdir = "/workspace"
			}
			if !reflect.DeepEqual(out, want) {
				t.Fatalf("CheckSpec returned %+v, want %+v", out, want)
			}
		}
	}
}
