//go:build !linux

package confine

import (
	"runtime"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Setup applies nothing: Landlock exists only on Linux.
func Setup(in Inputs) (sandbox.LandlockInfo, error) {
	return sandbox.LandlockInfo{Note: "Landlock is Linux-only (GOOS=" + runtime.GOOS + ")"}, nil
}
