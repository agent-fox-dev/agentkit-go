package gateway_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
)

// TS-19-4: the gateway protocol constants and SandboxIDKey have their stated
// values, and the Agent protocol minor stays 2. 19-REQ-1.4.
func TestProtocolConstants_TS19_4(t *testing.T) {
	if gateway.ProtocolMajor != 1 || gateway.ProtocolMinor != 0 {
		t.Errorf("gateway protocol %d.%d, want 1.0", gateway.ProtocolMajor, gateway.ProtocolMinor)
	}
	if gateway.SandboxIDKey != "agentkit-sandbox-id" {
		t.Errorf("SandboxIDKey %q, want agentkit-sandbox-id", gateway.SandboxIDKey)
	}
	if sandbox.ProtocolMajor != 1 || sandbox.ProtocolMinor != 2 {
		t.Errorf("agent protocol %d.%d, want 1.2", sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	}
}
