package podman

import (
	"errors"
	"os"
	"syscall"
)

// sysProcAttr is nil on Windows: there are no process groups to set up.
func sysProcAttr() *syscall.SysProcAttr { return nil }

// killGroup kills p alone on Windows; a process already gone is not an
// error.
func killGroup(p *os.Process) error {
	if err := p.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
