//go:build unix

package local

import (
	"errors"
	"syscall"
)

// canProbe: Unix has a liveness probe.
const canProbe = true

// alive reports whether pid is running: signal 0 succeeds, or is refused
// because the process belongs to someone else.
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
