package sandbox_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

var (
	// localCaps honours only an unrestricted network, like local.
	localCaps = sandbox.Capabilities{Provider: "p", Network: []sandbox.NetworkMode{sandbox.NetworkUnrestricted}}
	// fullCaps honours every field.
	fullCaps = sandbox.Capabilities{
		Provider:  "full",
		Network:   []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted},
		Resources: sandbox.ResourceCaps{CPUs: true, MemoryBytes: true, PIDs: true, DiskBytes: true},
		Image:     true, Workdir: true, Landlock: true,
	}
)

func TestCheckSpecAppliesTheDefaultsAndKeepsEverythingElse(t *testing.T) {
	withWorkdir := localCaps
	withWorkdir.Workdir = true
	base := sandbox.Spec{Network: unrestricted, Env: map[string]string{"A": "1"}, Labels: map[string]string{"team": "x"}}
	explicit := base
	explicit.TTL, explicit.Workdir = 5*time.Minute, "/src"
	for _, c := range []struct {
		name    string
		caps    sandbox.Capabilities
		spec    sandbox.Spec
		ttl     time.Duration
		workdir string
	}{
		{"defaults where Workdir is honoured", withWorkdir, base, time.Hour, "/workspace"},
		{"defaults where Workdir is not honoured", localCaps, base, time.Hour, ""},
		{"explicit values", withWorkdir, explicit, 5 * time.Minute, "/src"},
	} {
		got, err := sandbox.CheckSpec(c.caps, c.spec)
		if err != nil || got.TTL != c.ttl || got.Workdir != c.workdir {
			t.Errorf("%s: %+v, %v; want TTL %v and Workdir %q", c.name, got, err, c.ttl, c.workdir)
		}
		want := c.spec
		want.TTL, want.Workdir = c.ttl, c.workdir
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: CheckSpec changed other fields: %+v, want %+v", c.name, got, want)
		}
	}
}

func TestCheckSpecRefusesTheFirstFieldTheProviderDoesNotCover(t *testing.T) {
	with := func(f func(*sandbox.Spec)) sandbox.Spec {
		s := sandbox.Spec{Network: unrestricted}
		f(&s)
		return s
	}
	for _, c := range []struct {
		spec         sandbox.Spec
		field, value string
	}{
		{sandbox.Spec{}, "Network.Mode", "none"},
		{sandbox.Spec{Image: "alpine", Resources: sandbox.Resources{CPUs: 1}}, "Network.Mode", "none"},
		{sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkAllowlist}}, "Network.Mode", "allowlist"},
		{with(func(s *sandbox.Spec) { s.Resources.CPUs, s.Image = 1.5, "alpine" }), "Resources.CPUs", "1.5"},
		{with(func(s *sandbox.Spec) { s.Resources.MemoryBytes = 2 }), "Resources.MemoryBytes", "2"},
		{with(func(s *sandbox.Spec) { s.Resources.PIDs = 3 }), "Resources.PIDs", "3"},
		{with(func(s *sandbox.Spec) { s.Resources.DiskBytes = 4 }), "Resources.DiskBytes", "4"},
		{with(func(s *sandbox.Spec) { s.Image, s.Workdir = "alpine", "/w" }), "Image", `"alpine"`},
		{with(func(s *sandbox.Spec) { s.Workdir, s.RequireLandlock = "/w", true }), "Workdir", `"/w"`},
		{with(func(s *sandbox.Spec) { s.RequireLandlock = true }), "RequireLandlock", "true"},
	} {
		got, err := sandbox.CheckSpec(localCaps, c.spec)
		var se *sandbox.SpecError
		if !errors.As(err, &se) || se.Provider != "p" || se.Field != c.field || !errors.Is(err, sandbox.ErrUnsupported) {
			t.Errorf("%s: %v, want a *SpecError for p naming it and matching ErrUnsupported", c.field, err)
			continue
		}
		if prefix := `sandbox: provider "p" cannot honour Spec.` + c.field + " = " + c.value + ": "; !strings.HasPrefix(err.Error(), prefix) || se.Reason == "" {
			t.Errorf("%s: message %q, want %q and a reason", c.field, err, prefix)
		}
		if !reflect.DeepEqual(got, sandbox.Spec{}) {
			t.Errorf("%s: returned %+v, want the zero Spec", c.field, got)
		}
	}
	if _, err := sandbox.CheckSpec(fullCaps, sandbox.Spec{RequireLandlock: true, Image: "i", Workdir: "/w"}); err != nil {
		t.Errorf("capabilities covering every field refused: %v", err)
	}
}

func TestCheckSpecRefusesValuesNoProviderAccepts(t *testing.T) {
	allow := func(eps ...sandbox.Endpoint) sandbox.Network {
		return sandbox.Network{Mode: sandbox.NetworkAllowlist, Allow: eps}
	}
	for _, c := range []struct {
		field string
		spec  sandbox.Spec
	}{
		{"TTL", sandbox.Spec{TTL: -time.Second}},
		{"Resources.CPUs", sandbox.Spec{Resources: sandbox.Resources{CPUs: -1}}},
		{"Resources.MemoryBytes", sandbox.Spec{Resources: sandbox.Resources{MemoryBytes: -1}}},
		{"Resources.PIDs", sandbox.Spec{Resources: sandbox.Resources{PIDs: -1}}},
		{"Resources.DiskBytes", sandbox.Spec{Resources: sandbox.Resources{DiskBytes: -1}}},
		{"Env name", sandbox.Spec{Env: map[string]string{"": "v"}}},
		{"Env name", sandbox.Spec{Env: map[string]string{"A=B": "v"}}},
		{"Env name", sandbox.Spec{Env: map[string]string{"A\x00": "v"}}},
		{"Env value", sandbox.Spec{Env: map[string]string{"A": "v\x00"}}},
		{"Labels", sandbox.Spec{Labels: map[string]string{"agentkit.sandbox": "x"}}},
		{"Network.Allow", sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted, Allow: []sandbox.Endpoint{{Host: "example.com", Port: 443}}}}},
		{"Network.Allow[0].Host", sandbox.Spec{Network: allow(sandbox.Endpoint{Port: 443})}},
		{"Network.Allow[1].Port", sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "a", Port: 1}, sandbox.Endpoint{Host: "b", Port: 0})}},
		{"Network.Allow[0].Port", sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "example.com", Port: 65536})}},
	} {
		got, err := sandbox.CheckSpec(fullCaps, c.spec)
		if err == nil || errors.Is(err, sandbox.ErrUnsupported) || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: %v, want an error naming it that is not ErrUnsupported", c.field, err)
		}
		if !reflect.DeepEqual(got, sandbox.Spec{}) {
			t.Errorf("%s: returned %+v, want the zero Spec", c.field, got)
		}
	}
	if _, err := sandbox.CheckSpec(fullCaps, sandbox.Spec{Network: allow(sandbox.Endpoint{Host: "example.com", Port: 65535})}); err != nil {
		t.Errorf("a valid allowlist was refused: %v", err)
	}
}
