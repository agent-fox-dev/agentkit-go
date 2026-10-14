// Package gatewaywire converts between the sandbox API's values and the
// gateway protocol's messages. The gateway client and the gateway server
// share it, so both ends read a spec and a capability set the same way.
//
// Proto cannot tell a nil map or slice from an empty one: the From
// functions return nil for an empty collection.
package gatewaywire

import (
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// SpecToPB is spec as a wire message, with Provider encoded as JSON (empty
// when it has no keys).
func SpecToPB(spec sandbox.Spec) (*sandboxpb.SandboxSpec, error) {
	var provider []byte
	if len(spec.Provider) > 0 {
		b, err := json.Marshal(spec.Provider)
		if err != nil {
			return nil, fmt.Errorf("sandbox: Spec.Provider does not encode as JSON: %w", err)
		}
		provider = b
	}
	r := spec.Resources
	pb := &sandboxpb.SandboxSpec{
		Image:     spec.Image,
		Workdir:   spec.Workdir,
		Env:       maps.Clone(spec.Env),
		Resources: &sandboxpb.Resources{Cpus: r.CPUs, MemoryBytes: r.MemoryBytes, Pids: r.PIDs, DiskBytes: r.DiskBytes},
		Network:   &sandboxpb.Network{Mode: ModeToPB(spec.Network.Mode)},
		TtlNanos:  int64(spec.TTL),
		Labels:    maps.Clone(spec.Labels),
		Provider:  provider,

		RequireLandlock: spec.RequireLandlock,
	}
	for _, ep := range spec.Network.Allow {
		pb.Network.Allow = append(pb.Network.Allow, &sandboxpb.Endpoint{Host: ep.Host, Port: int32(ep.Port)})
	}
	return pb, nil
}

// SpecFromPB is the spec pb carries. It refuses a network mode this build
// does not know and provider bytes that do not decode as a JSON object.
func SpecFromPB(pb *sandboxpb.SandboxSpec) (sandbox.Spec, error) {
	mode, err := ModeFromPB(pb.GetNetwork().GetMode())
	if err != nil {
		return sandbox.Spec{}, err
	}
	r := pb.GetResources()
	spec := sandbox.Spec{
		Image:     pb.GetImage(),
		Workdir:   pb.GetWorkdir(),
		Env:       nilIfEmpty(pb.GetEnv()),
		Resources: sandbox.Resources{CPUs: r.GetCpus(), MemoryBytes: r.GetMemoryBytes(), PIDs: r.GetPids(), DiskBytes: r.GetDiskBytes()},
		Network:   sandbox.Network{Mode: mode},
		TTL:       time.Duration(pb.GetTtlNanos()),
		Labels:    nilIfEmpty(pb.GetLabels()),

		RequireLandlock: pb.GetRequireLandlock(),
	}
	for _, ep := range pb.GetNetwork().GetAllow() {
		spec.Network.Allow = append(spec.Network.Allow, sandbox.Endpoint{Host: ep.GetHost(), Port: int(ep.GetPort())})
	}
	if b := pb.GetProvider(); len(b) > 0 {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return sandbox.Spec{}, fmt.Errorf("sandbox: Spec.Provider is not a JSON object: %w", err)
		}
		if len(m) > 0 {
			spec.Provider = m
		}
	}
	return spec, nil
}

// CapsToPB is caps as the capability fields of a CapabilitiesResponse; the
// caller sets the protocol, version and limit fields.
func CapsToPB(caps sandbox.Capabilities) *sandboxpb.CapabilitiesResponse {
	pb := &sandboxpb.CapabilitiesResponse{
		Provider:    caps.Provider,
		Isolation:   sandboxpb.Isolation(caps.Isolation),
		Cpus:        caps.Resources.CPUs,
		MemoryBytes: caps.Resources.MemoryBytes,
		Pids:        caps.Resources.PIDs,
		DiskBytes:   caps.Resources.DiskBytes,
		Image:       caps.Image,
		Workdir:     caps.Workdir,
		Landlock:    caps.Landlock,
		Notes:       caps.Notes,
	}
	for _, m := range caps.Network {
		pb.Network = append(pb.Network, ModeToPB(m))
	}
	return pb
}

// CapsFromPB is the capability set pb carries. A network mode this build
// does not know is left out: it could not be requested anyway.
func CapsFromPB(pb *sandboxpb.CapabilitiesResponse) sandbox.Capabilities {
	caps := sandbox.Capabilities{
		Provider:  pb.GetProvider(),
		Isolation: sandbox.Isolation(pb.GetIsolation()),
		Resources: sandbox.ResourceCaps{CPUs: pb.GetCpus(), MemoryBytes: pb.GetMemoryBytes(), PIDs: pb.GetPids(), DiskBytes: pb.GetDiskBytes()},
		Image:     pb.GetImage(),
		Workdir:   pb.GetWorkdir(),
		Landlock:  pb.GetLandlock(),
		Notes:     pb.GetNotes(),
	}
	for _, m := range pb.GetNetwork() {
		if mode, err := ModeFromPB(m); err == nil {
			caps.Network = append(caps.Network, mode)
		}
	}
	return caps
}

// ModeToPB is m as the wire enum; the two are numbered alike.
func ModeToPB(m sandbox.NetworkMode) sandboxpb.NetworkMode { return sandboxpb.NetworkMode(m) }

// ModeFromPB is the network mode m names, or an error for a value this build
// does not know.
func ModeFromPB(m sandboxpb.NetworkMode) (sandbox.NetworkMode, error) {
	switch m {
	case sandboxpb.NetworkMode_NONE, sandboxpb.NetworkMode_ALLOWLIST, sandboxpb.NetworkMode_UNRESTRICTED:
		return sandbox.NetworkMode(m), nil
	}
	return 0, fmt.Errorf("sandbox: unknown network mode %d", int32(m))
}

// Nanos is t in Unix nanoseconds.
func Nanos(t time.Time) int64 { return t.UnixNano() }

// FromNanos is the time n Unix nanoseconds name.
func FromNanos(n int64) time.Time { return time.Unix(0, n) }

// nilIfEmpty is m, or nil when m has no entries.
func nilIfEmpty(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	return m
}
