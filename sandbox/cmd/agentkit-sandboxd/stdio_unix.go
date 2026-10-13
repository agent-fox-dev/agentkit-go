//go:build unix

package main

import (
	"os"
	"syscall"
)

// pollable returns a file for fd that the runtime poller manages, when f is
// a pipe or a socket, so that closing it interrupts a blocked read or write;
// the agent closes its connection at the TTL. Anything else is returned as
// it is.
func pollable(f *os.File, fd int) *os.File {
	fi, err := f.Stat()
	if err != nil || fi.Mode()&(os.ModeNamedPipe|os.ModeSocket) == 0 {
		return f
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		return f
	}
	return os.NewFile(uintptr(fd), f.Name())
}
