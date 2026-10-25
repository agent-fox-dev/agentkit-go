package sandbox_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func TestNetworkModeString(t *testing.T) {
	for m, want := range map[sandbox.NetworkMode]string{
		sandbox.NetworkNone: "none", sandbox.NetworkAllowlist: "allowlist", sandbox.NetworkUnrestricted: "unrestricted", 7: "NetworkMode(7)",
	} {
		if got := m.String(); got != want {
			t.Errorf("NetworkMode(%d).String() = %q, want %q", int(m), got, want)
		}
	}
}
