package confine

import "github.com/agent-fox-dev/agentkit-go/sandbox"

// probeABI is the kernel probe; tests replace it to simulate other kernels.
var probeABI = kernelProbe

// decide probes the kernel once and returns the outcome Plan chooses for
// mode, with KernelABI the probed value (0 after an error, never capped).
func decide(mode sandbox.NetworkMode) (sandbox.LandlockInfo, Decision) {
	k, err := probeABI()
	if err != nil {
		k = 0
	}
	d := Plan(k, err, mode)
	return sandbox.LandlockInfo{ABI: d.ABI, KernelABI: k, TCP: d.TCP, Note: d.Note}, d
}
