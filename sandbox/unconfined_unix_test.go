//go:build unix

package sandbox_test

import "syscall"

// currentUmask reads the process umask by setting and restoring it.
func currentUmask() int {
	m := syscall.Umask(0o022)
	syscall.Umask(m)
	return m
}
