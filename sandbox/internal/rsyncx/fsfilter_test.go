package rsyncx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TS-14-29 (filter half): a symlink created after the rules were computed,
// whose target leaves the transfer root, is hidden when it is served; one
// that stays inside is served. 14-REQ-5.6.
func TestFilteredHidesLateEscapingLinks_TS14_29(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(ws)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	fsys := Filtered(root, "sub", NewMatcher(nil)) // no rules: nothing was listed
	for name, target := range map[string]string{"abs": "/etc", "up": "../x", "in": "a.txt", "deep/up": "../../y"} {
		p := filepath.Join(ws, "sub", filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	for _, name := range []string{"sub/abs", "sub/up", "sub/deep/up"} {
		if _, err := fs.Lstat(fsys, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want not exist", name, err)
		}
		if _, err := fs.ReadLink(fsys, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadLink(%s) = %v, want not exist", name, err)
		}
	}
	if _, err := fs.Lstat(fsys, "sub/in"); err != nil {
		t.Errorf("the inside link is hidden: %v", err)
	}
	ents, err := fs.ReadDir(fsys, "sub")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if len(names) != 2 || names[0] != "deep" || names[1] != "in" {
		t.Errorf("ReadDir(sub) = %v, want [deep in]", names)
	}
}
