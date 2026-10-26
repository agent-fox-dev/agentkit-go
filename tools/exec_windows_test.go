//go:build windows

package tools

import "errors"

// alive and mkfifo are unreachable on Windows: every caller is gated by
// needPOSIX, which skips the test before either runs.
func alive(int) bool { return false }

func mkfifo(string) error { return errors.New("named pipes unavailable on windows") }
