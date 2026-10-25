//go:build !unix

package workspace

import "errors"

func mkfifo(string) error { return errors.New("no named pipes on this platform") }
