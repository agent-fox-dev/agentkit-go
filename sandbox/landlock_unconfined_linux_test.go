//go:build linux

package sandbox_test

import "syscall"

// On Linux the smoke test also compares PR_GET_NO_NEW_PRIVS before and after.
func init() {
	noNewPrivs = func() int {
		r, _, _ := syscall.Syscall(syscall.SYS_PRCTL, 39, 0, 0)
		return int(r)
	}
}
