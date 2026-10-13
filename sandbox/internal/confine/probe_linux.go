//go:build linux

package confine

import llsyscall "github.com/landlock-lsm/go-landlock/landlock/syscall"

// kernelProbe asks the kernel for its Landlock ABI version.
func kernelProbe() (int, error) { return llsyscall.LandlockGetABIVersion() }
