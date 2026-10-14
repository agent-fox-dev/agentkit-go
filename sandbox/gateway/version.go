package gateway

// The gateway protocol version (sandbox/internal/sandboxpb/gateway.proto),
// separate from the Agent protocol's sandbox.ProtocolMajor and
// ProtocolMinor. Fields are added, never renumbered, and any addition bumps
// ProtocolMinor. A client refuses a gateway with a different ProtocolMajor
// and accepts any minor.
const (
	ProtocolMajor = 1
	ProtocolMinor = 0
)

// SandboxIDKey is the metadata key that names the sandbox on every Agent RPC
// sent to a gateway. Gateway RPCs carry the id in their messages instead.
const SandboxIDKey = "agentkit-sandbox-id"
