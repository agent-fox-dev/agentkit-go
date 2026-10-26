//go:build !windows

package tools

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// alive reports whether pid is a running process; a zombie is not.
func alive(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return err != nil || !strings.Contains(string(stat), ") Z ")
}

// mkfifo creates a named pipe at path for the test that measures a call's
// setup time against a slow reader.
func mkfifo(path string) error {
	return syscall.Mkfifo(path, 0o600)
}
