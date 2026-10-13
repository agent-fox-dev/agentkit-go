//go:build unix

package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// lock sets p's mode to 0000 and restores mode at cleanup.
func lock(t *testing.T, p string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, mode) })
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return fi.Mode().Perm()
}

// TS-14-13: unreadable entries on either side are excluded, neither sent
// nor deleted, and the transfer returns nil. 14-REQ-3.5.
func TestUnreadableEntries_TS14_13(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every entry")
	}
	isolate(t)
	sb := newLocal(t)
	ws := sb.Agent().Workdir
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	writeFiles(t, src, map[string]string{"a.txt": "a", "locked/x.txt": "x", "secret.txt": "s"})
	writeFiles(t, ws, map[string]string{"wslocked/y.txt": "y", "wssecret.txt": "w"})
	writeFiles(t, dst, map[string]string{"hold/z.txt": "z"})
	lock(t, filepath.Join(src, "locked"), 0o755)
	lock(t, filepath.Join(src, "secret.txt"), 0o644)
	lock(t, filepath.Join(ws, "wslocked"), 0o755)
	lock(t, filepath.Join(ws, "wssecret.txt"), 0o644)
	lock(t, filepath.Join(dst, "hold"), 0o755)

	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic, Delete: true}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !lexists(filepath.Join(ws, "a.txt")) || lexists(filepath.Join(ws, "locked")) || lexists(filepath.Join(ws, "secret.txt")) {
		t.Error("the workspace lacks a.txt or holds an unreadable source entry")
	}
	for _, p := range []string{"wslocked", "wssecret.txt"} {
		if m := modeOf(t, filepath.Join(ws, p)); m != 0 {
			t.Errorf("%s has mode %v after Delete, want it kept at 0000", p, m)
		}
	}
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: dst, Ignore: hermetic, Delete: true}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !lexists(filepath.Join(dst, "a.txt")) || lexists(filepath.Join(dst, "wslocked")) || lexists(filepath.Join(dst, "wssecret.txt")) {
		t.Error("dst lacks a.txt or holds an unreadable workspace entry")
	}
	if m := modeOf(t, filepath.Join(dst, "hold")); m != 0 {
		t.Errorf("dst/hold has mode %v, want it kept at 0000", m)
	}
}
