//go:build unix

package podman

import (
	"errors"
	"os"
	"syscall"
)

// sysProcAttr puts the engine process in its own process group.
func sysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// killGroup kills p's process group; a group already gone is not an error.
func killGroup(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
