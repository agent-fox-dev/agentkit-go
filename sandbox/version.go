package sandbox

// AgentPath is where a sandbox image installs the agent binary.
const AgentPath = "/usr/local/bin/agentkit-sandboxd"

// The agentkit.sandbox.v1 protocol version. Fields are added, never
// renumbered; a scope that adds an RPC or a field bumps ProtocolMinor. A host
// refuses an agent with a different ProtocolMajor and accepts any minor.
const (
	ProtocolMajor = 1
	ProtocolMinor = 2
)

// MaxMessageBytes bounds every gRPC message in both directions, on the agent
// and on the host. It equals wire.Defaults().MaxMessageBytes.
const MaxMessageBytes = 16 << 20
