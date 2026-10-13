//go:build !unix

package main

import "os"

// pollable returns f: outside Unix, best effort (steering).
func pollable(f *os.File, _ int) *os.File { return f }
