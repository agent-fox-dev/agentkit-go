//go:build !linux

package confine

import (
	"fmt"
	"runtime"
)

// kernelProbe fails: Landlock exists only on Linux.
func kernelProbe() (int, error) {
	return 0, fmt.Errorf("Landlock is Linux-only (GOOS=%s)", runtime.GOOS)
}
