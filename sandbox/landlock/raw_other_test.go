//go:build !linux

package landlock

import "syscall"

// rawLandlockABI has nothing to query off Linux.
func rawLandlockABI() (int, syscall.Errno) { return 0, 0 }
