//go:build unix

package landlock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// readOnlyTree is a tree under dir like a Go module cache after chmod -R
// a-w, with symlinks to outside's file, directory and itself.
func readOnlyTree(t *testing.T, dir, outside string) {
	t.Helper()
	testutil.WriteTree(t, dir, map[string]string{"mod/x@v1/a.go": "a", "mod/x@v1/sub/b.go": "b", "ws/f": "f"})
	for name, target := range map[string]string{"ws/to-file": filepath.Join(outside, "f"), "ws/to-dir": filepath.Join(outside, "d"), "mod/to-root": outside} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for p, mode := range map[string]os.FileMode{"mod/x@v1/a.go": 0o444, "mod/x@v1/sub/b.go": 0o444, "mod/x@v1/sub": 0o555, "mod/x@v1": 0o500, "mod": 0o555, "ws": 0o500} {
		if err := os.Chmod(filepath.Join(dir, p), mode); err != nil {
			t.Fatal(err)
		}
	}
}

// emptyDir does the same to the contents; neither minds a missing directory.
func TestRemoveTreeRemovesReadOnlyTreesWithoutFollowingSymlinks(t *testing.T) {
	outside := testutil.Tree(t, map[string]string{"f": "outside", "d/inner": "inner"})
	d := filepath.Join(t.TempDir(), "sandbox")
	readOnlyTree(t, d, outside)
	if err := os.Chmod(d, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := removeTree(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(d); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree remains: %v", err)
	}
	e := filepath.Join(t.TempDir(), "workspace")
	readOnlyTree(t, e, outside)
	if err := os.Chmod(e, 0o500); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(e); emptyDir(e) != nil || err != nil || len(entries(t, e)) != 0 {
		t.Errorf("emptyDir left %v (%v)", entries(t, e), fi)
	}
	if b, err := os.ReadFile(filepath.Join(outside, "d", "inner")); err != nil || string(b) != "inner" || len(entries(t, outside)) != 2 {
		t.Errorf("the outside changed: %v, %v", entries(t, outside), err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := errors.Join(removeTree(missing), emptyDir(missing)); err != nil {
		t.Errorf("a missing directory: %v", err)
	}
}
