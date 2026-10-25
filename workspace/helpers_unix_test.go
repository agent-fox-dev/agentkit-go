//go:build unix

package workspace

import "syscall"

// mkfifo makes a named pipe: opening it for reading blocks until a writer
// appears, which is how a test learns that code opened a file it should not.
func mkfifo(path string) error { return syscall.Mkfifo(path, 0o644) }
