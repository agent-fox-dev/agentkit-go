package sandbox_test

import (
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func TestAgentArgsCarryTheTTLNetworkAndAnyWorkdir(t *testing.T) {
	for _, c := range []struct {
		spec sandbox.Spec
		want []string
	}{
		{sandbox.Spec{TTL: time.Hour}, []string{"--ttl", "1h0m0s", "--network", "none"}},
		{sandbox.Spec{TTL: 90 * time.Second, Network: unrestricted, Workdir: "/src"},
			[]string{"--ttl", "1m30s", "--network", "unrestricted", "--workdir", "/src"}},
	} {
		if got := sandbox.AgentArgs(c.spec); !slices.Equal(got, c.want) {
			t.Errorf("AgentArgs(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
}
