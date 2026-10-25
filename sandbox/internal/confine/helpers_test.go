package confine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// fakeProbe makes the kernel probe return abi and err, counting its calls.
func fakeProbe(t *testing.T, abi int, err error) *int {
	t.Helper()
	calls := new(int)
	old := probeABI
	probeABI = func() (int, error) { *calls++; return abi, err }
	t.Cleanup(func() { probeABI = old })
	return calls
}

// dirs creates the named directories under one temporary "base".
func dirs(t *testing.T, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{"base": t.TempDir()}
	for _, n := range names {
		out[n] = filepath.Join(out["base"], n)
		if err := os.MkdirAll(out[n], 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// resolved is each existing path with symlinks resolved.
func resolved(paths ...string) []string {
	var out []string
	for _, p := range paths {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// extendLists adds paths to the system directories and device files.
func extendLists(t *testing.T, sys, dev []string) {
	t.Helper()
	oldS, oldD := systemDirs, deviceFiles
	systemDirs, deviceFiles = slices.Concat(oldS, sys), slices.Concat(oldD, dev)
	t.Cleanup(func() { systemDirs, deviceFiles = oldS, oldD })
}
