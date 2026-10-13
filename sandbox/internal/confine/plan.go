package confine

import (
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Decision is what Plan chose: the Landlock ABI to apply (0 for none),
// whether the ruleset handles TCP bind and connect, and why nothing, or only
// part, is applied.
type Decision struct {
	ABI  int
	TCP  bool
	Note string
}

// Plan decides what to apply from the kernel's ABI, or the probe's error,
// and the network mode. ABI 1 is not applied (it denies every
// cross-directory rename); the applied ABI is capped at
// sandbox.LandlockTCPABI; TCP is handled only at that ABI under
// NetworkNone. It is pure and has no build constraint.
func Plan(kernelABI int, probeErr error, mode sandbox.NetworkMode) Decision {
	switch {
	case probeErr != nil:
		return Decision{Note: "Landlock unavailable: " + probeErr.Error()}
	case kernelABI <= 0:
		return Decision{Note: "kernel reports no Landlock support"}
	case kernelABI < sandbox.LandlockMinABI:
		return Decision{Note: fmt.Sprintf("kernel Landlock ABI %d cannot permit cross-directory renames (needs ABI 2, Linux 5.19); not applied", kernelABI)}
	}
	d := Decision{ABI: min(kernelABI, sandbox.LandlockTCPABI)}
	switch mode {
	case sandbox.NetworkAllowlist:
		d.Note = "Landlock cannot express a host allowlist; TCP is left to the provider"
	case sandbox.NetworkNone:
		if d.ABI < sandbox.LandlockTCPABI {
			d.Note = fmt.Sprintf("kernel Landlock ABI %d cannot restrict TCP (needs ABI 4, Linux 6.7)", kernelABI)
		} else {
			d.TCP = true
		}
	}
	return d
}
