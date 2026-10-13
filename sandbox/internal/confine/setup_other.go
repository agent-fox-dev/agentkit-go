//go:build !linux

package confine

import (
	"runtime"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Setup refuses dangerous flag values with a *UsageError, as on Linux, and
// applies nothing: Landlock exists only on Linux.
func Setup(in Inputs) (sandbox.LandlockInfo, error) {
	if err := validate(in); err != nil {
		return sandbox.LandlockInfo{}, err
	}
	return sandbox.LandlockInfo{Note: "Landlock is Linux-only (GOOS=" + runtime.GOOS + ")"}, nil
}
