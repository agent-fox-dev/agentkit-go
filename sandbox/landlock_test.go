package sandbox_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func TestLandlockInfoSatisfiesNeedsABITwoAndUnderNoNetworkTCP(t *testing.T) {
	for _, c := range []struct {
		abi  int
		tcp  bool
		mode sandbox.NetworkMode
		want bool
	}{
		{0, false, sandbox.NetworkUnrestricted, false},
		{1, true, sandbox.NetworkNone, false},
		{sandbox.LandlockMinABI, false, sandbox.NetworkUnrestricted, true},
		{sandbox.LandlockMinABI, false, sandbox.NetworkAllowlist, true},
		{3, false, sandbox.NetworkNone, false},
		{sandbox.LandlockTCPABI, true, sandbox.NetworkNone, true},
	} {
		if got := (sandbox.LandlockInfo{ABI: c.abi, TCP: c.tcp}).Satisfies(c.mode); got != c.want {
			t.Errorf("ABI %d TCP %v under %s: Satisfies = %v, want %v", c.abi, c.tcp, c.mode, got, c.want)
		}
	}
}
