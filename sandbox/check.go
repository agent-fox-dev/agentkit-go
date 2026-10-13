package sandbox

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
)

// reservedLabel is the label key providers use for their own bookkeeping.
const reservedLabel = "agentkit.sandbox"

// CheckSpec checks spec against caps and returns the effective spec: a TTL
// of 0 becomes one hour, and an empty Workdir becomes /workspace when caps
// honours Workdir. It refuses values no provider accepts, and refuses with a
// *SpecError (matching ErrUnsupported) every field caps does not cover.
// Every error returns the zero Spec. Every provider calls it first in
// Create.
func CheckSpec(caps Capabilities, spec Spec) (Spec, error) {
	if err := checkValues(spec); err != nil {
		return Spec{}, err
	}
	if err := checkCovered(caps, spec); err != nil {
		return Spec{}, err
	}
	if spec.TTL == 0 {
		spec.TTL = time.Hour
	}
	if caps.Workdir && spec.Workdir == "" {
		spec.Workdir = "/workspace"
	}
	return spec, nil
}

// checkValues refuses what no provider accepts, whatever its capabilities.
func checkValues(spec Spec) error {
	if spec.TTL < 0 {
		return fmt.Errorf("sandbox: Spec.TTL is negative (%v)", spec.TTL)
	}
	r := spec.Resources
	for _, f := range []struct {
		name string
		neg  bool
	}{{"CPUs", r.CPUs < 0}, {"MemoryBytes", r.MemoryBytes < 0}, {"PIDs", r.PIDs < 0}, {"DiskBytes", r.DiskBytes < 0}} {
		if f.neg {
			return fmt.Errorf("sandbox: Spec.Resources.%s is negative", f.name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(spec.Env)) {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return fmt.Errorf("sandbox: Spec.Env name %q is empty or contains '=' or NUL", name)
		}
		if strings.ContainsRune(spec.Env[name], 0) {
			return fmt.Errorf("sandbox: Spec.Env value of %q contains NUL", name)
		}
	}
	if _, ok := spec.Labels[reservedLabel]; ok {
		return fmt.Errorf("sandbox: Spec.Labels key %q is reserved", reservedLabel)
	}
	if len(spec.Network.Allow) > 0 && spec.Network.Mode != NetworkAllowlist {
		return fmt.Errorf("sandbox: Spec.Network.Allow is set but Spec.Network.Mode is %s, not allowlist", spec.Network.Mode)
	}
	for i, ep := range spec.Network.Allow {
		if ep.Host == "" {
			return fmt.Errorf("sandbox: Spec.Network.Allow[%d].Host is empty", i)
		}
		if ep.Port < 1 || ep.Port > 65535 {
			return fmt.Errorf("sandbox: Spec.Network.Allow[%d].Port %d is outside 1-65535", i, ep.Port)
		}
	}
	return nil
}

// checkCovered refuses the first field caps does not cover.
func checkCovered(caps Capabilities, spec Spec) error {
	refuse := func(field, value, reason string) error {
		return &SpecError{Provider: caps.Provider, Field: field, Reason: reason, value: value}
	}
	if !slices.Contains(caps.Network, spec.Network.Mode) {
		honoured := make([]string, len(caps.Network))
		for i, m := range caps.Network {
			honoured[i] = m.String()
		}
		return refuse("Network.Mode", spec.Network.Mode.String(),
			fmt.Sprintf("it honours network modes [%s]", strings.Join(honoured, ", ")))
	}
	r, rc := spec.Resources, caps.Resources
	for _, f := range []struct {
		name  string
		set   bool
		bound bool
		value string
	}{
		{"CPUs", r.CPUs != 0, rc.CPUs, fmt.Sprint(r.CPUs)},
		{"MemoryBytes", r.MemoryBytes != 0, rc.MemoryBytes, fmt.Sprint(r.MemoryBytes)},
		{"PIDs", r.PIDs != 0, rc.PIDs, fmt.Sprint(r.PIDs)},
		{"DiskBytes", r.DiskBytes != 0, rc.DiskBytes, fmt.Sprint(r.DiskBytes)},
	} {
		if f.set && !f.bound {
			return refuse("Resources."+f.name, f.value, "it cannot bound "+f.name)
		}
	}
	if spec.Image != "" && !caps.Image {
		return refuse("Image", fmt.Sprintf("%q", spec.Image), "it does not run images")
	}
	if spec.Workdir != "" && !caps.Workdir {
		return refuse("Workdir", fmt.Sprintf("%q", spec.Workdir), "it does not choose the workspace path")
	}
	if spec.RequireLandlock && !caps.Landlock {
		return refuse("RequireLandlock", "true", "its agents cannot apply a Landlock ruleset")
	}
	return nil
}
