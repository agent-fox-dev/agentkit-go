package landlock

import "syscall"

// rawLandlockABI issues the Landlock version query directly.
func rawLandlockABI() (int, syscall.Errno) {
	r, _, e := syscall.Syscall(444, 0, 0, 1)
	return int(r), e
}
