package rsyncx

import (
	"os"
	"path/filepath"
	"testing"
)

// Deletion removes what the sender lacks and no rule protects, keeps a
// directory that still holds a protected entry, and never follows a symlink.
func TestDeleteAbsentRemovesOnlyUnprotectedAbsentEntries(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "recv"), filepath.Join(base, "outside")
	writeTree(t, outside, map[string]string{"keep": "k"})
	writeTree(t, root, map[string]string{
		"a.txt": "a", "stale.txt": "s", "out/x.o": "o", "gone/deep/f": "f", ".env": "e",
		".git/HEAD": "ref", "link-out": "->" + outside, "dir/stale": "s", "dir/kept": "k", "link2": "->" + outside,
	})
	n, err := DeleteAbsent(root, NewMatcher([]string{"out/x.o", ".env", ".git", "link-out"}), []string{"a.txt", "dir", "dir/kept/"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.txt", "out/x.o", ".env", ".git/HEAD", "link-out", "dir/kept"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
	for _, p := range []string{"stale.txt", "gone", "dir/stale", "link2"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	if n != 6 {
		t.Errorf("deleted %d entries, want 6 (stale.txt, gone, gone/deep, gone/deep/f, dir/stale, link2)", n)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Error("deletion reached through a symlink")
	}
}

func TestDeleteAbsentLeavesAnUnreadableDirectoryAlone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"locked/f": "x"})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if n, err := DeleteAbsent(root, NewMatcher(nil), nil); err != nil || n != 0 {
		t.Errorf("DeleteAbsent = %d, %v; want nothing deleted", n, err)
	}
	if _, err := DeleteAbsent(filepath.Join(root, "missing"), NewMatcher(nil), nil); err == nil {
		t.Error("DeleteAbsent of a missing root succeeded")
	}
}
