package sandbox

import (
	"fmt"
	"time"
)

// Spec describes the sandbox an embedder asks a Provider for. A provider
// refuses, through CheckSpec, every field its Capabilities do not cover.
type Spec struct {
	// Image is the container or VM image. Empty means the provider's default.
	Image string
	// Workdir is the workspace path inside the sandbox. Empty means
	// /workspace for a provider that honours it.
	Workdir string
	// Env is the tools' environment, beyond what the provider sets itself.
	Env       map[string]string
	Resources Resources
	Network   Network
	// TTL bounds the sandbox's lifetime. Zero means one hour.
	TTL time.Duration
	// Labels tag the sandbox for the provider. The key agentkit.sandbox is
	// reserved.
	Labels map[string]string
	// Provider holds provider-specific options. Each provider refuses a key
	// it does not define.
	Provider map[string]any
}

// Resources bounds what a sandbox may use. Zero means no bound.
type Resources struct {
	CPUs        float64
	MemoryBytes int64
	PIDs        int64
	DiskBytes   int64
}

// NetworkMode is what the sandbox's network may reach.
type NetworkMode int

const (
	// NetworkNone is no network. It is the zero value, so a Spec that says
	// nothing asks for no network.
	NetworkNone NetworkMode = iota
	// NetworkAllowlist reaches only Network.Allow.
	NetworkAllowlist
	// NetworkUnrestricted reaches anything the host can.
	NetworkUnrestricted
)

func (m NetworkMode) String() string {
	switch m {
	case NetworkNone:
		return "none"
	case NetworkAllowlist:
		return "allowlist"
	case NetworkUnrestricted:
		return "unrestricted"
	}
	return fmt.Sprintf("NetworkMode(%d)", int(m))
}

// Endpoint is one host and port an allowlist reaches.
type Endpoint struct {
	Host string
	Port int
}

// Network is a sandbox's network policy. Allow is set only with
// NetworkAllowlist.
type Network struct {
	Mode  NetworkMode
	Allow []Endpoint
}

// Isolation is the kind of boundary a provider puts around the tools.
type Isolation int

const (
	// IsolationNone: the tools run with the embedder's privileges.
	IsolationNone Isolation = iota
	// IsolationConfinement: a confined process on the host.
	IsolationConfinement
	// IsolationContainer: a container.
	IsolationContainer
	// IsolationMicroVM: a micro virtual machine.
	IsolationMicroVM
)

// ResourceCaps says which Resources fields a provider can bound.
type ResourceCaps struct {
	CPUs        bool
	MemoryBytes bool
	PIDs        bool
	DiskBytes   bool
}

// Capabilities describes a provider before any sandbox exists: what it
// isolates and which Spec fields it honours.
type Capabilities struct {
	// Provider is the provider's name.
	Provider  string
	Isolation Isolation
	// Network lists the modes the provider honours.
	Network   []NetworkMode
	Resources ResourceCaps
	// Image and Workdir say whether the provider honours those fields.
	Image   bool
	Workdir bool
	// Notes is a plain statement of what the provider does not do.
	Notes string
}
