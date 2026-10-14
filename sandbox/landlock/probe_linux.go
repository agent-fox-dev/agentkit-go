package landlock

import "syscall"

// defaultProbe is the Landlock version query,
// landlock_create_ruleset(NULL, 0, LANDLOCK_CREATE_RULESET_VERSION): the
// kernel's ABI, or the errno. It confines nothing.
func defaultProbe() (int, error) {
	r, _, errno := syscall.Syscall(444, 0, 0, 1)
	if errno != 0 {
		return 0, errno
	}
	return int(r), nil
}
