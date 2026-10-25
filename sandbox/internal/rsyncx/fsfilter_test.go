package rsyncx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func walkFS(t *testing.T, fsys fs.FS) []string {
	t.Helper()
	var seen []string
	if err := fs.WalkDir(fsys, ".", func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		seen = append(seen, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return seen
}

func TestFilteredHidesWhatTheRulesExclude(t *testing.T) {
	rules := []string{"build", "esc", ".git", "nested/tmp"}
	fsys := Filtered(openRoot(t, ruleTree(t)), ".", NewMatcher(rules))
	seen := walkFS(t, fsys)
	for _, p := range seen {
		if NewMatcher(rules).Excluded(p) {
			t.Errorf("the filtered tree shows %s", p)
		}
	}
	if !slices.Contains(seen, "keep.txt") || !slices.Contains(seen, "in") || !slices.Contains(seen, "nested/n.txt") {
		t.Errorf("the filtered tree lacks included entries: %v", seen)
	}
	for op, call := range map[string]func() error{
		"open":     func() error { _, err := fsys.Open("build/sub/deep.txt"); return err },
		"stat":     func() error { _, err := fs.Stat(fsys, "build"); return err },
		"lstat":    func() error { _, err := fs.Lstat(fsys, "nested/.git/HEAD"); return err },
		"readdir":  func() error { _, err := fs.ReadDir(fsys, "build"); return err },
		"readlink": func() error { _, err := fs.ReadLink(fsys, "esc"); return err },
	} {
		if err := call(); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s of an excluded path = %v, want not exist", op, err)
		}
	}
	if target, err := fs.ReadLink(fsys, "in"); err != nil || target != "keep.txt" {
		t.Errorf("ReadLink(in) = %q, %v", target, err)
	}
}

// Rules are relative to the transfer root; its ancestors stay reachable.
func TestFilteredAppliesRulesUnderTheTransferRoot(t *testing.T) {
	sub := Filtered(openRoot(t, ruleTree(t)), "nested", NewMatcher([]string{"tmp", "x.log"}))
	if _, err := fs.Stat(sub, "nested/tmp/a"); err == nil {
		t.Error("a rule relative to the transfer root nested did not apply")
	}
	for _, p := range []string{".", "nested", "nested/n.txt", "x.log"} {
		if _, err := fs.Stat(sub, p); err != nil {
			t.Errorf("Stat(%s) = %v, want it served", p, err)
		}
	}
}

// Symlinks are judged when served, so one created after the rules were
// computed, whose target leaves the transfer root, is hidden.
func TestFilteredHidesAnEscapingSymlinkCreatedLater(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	fsys := Filtered(openRoot(t, ws), "sub", NewMatcher(nil))
	writeTree(t, filepath.Join(ws, "sub"), map[string]string{"abs": "->/etc", "up": "->../x", "in": "->a.txt", "deep/up": "->../../y"})
	for _, name := range []string{"sub/abs", "sub/up", "sub/deep/up"} {
		if _, err := fs.Lstat(fsys, name); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Lstat(%s) = %v, want not exist", name, err)
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
	if !slices.Equal(names, []string{"deep", "in"}) {
		t.Errorf("ReadDir(sub) = %v, want [deep in]", names)
	}
}

func TestPresentSetListsTheFilteredEntriesRelativeToTheTransferRoot(t *testing.T) {
	r := openRoot(t, ruleTree(t))
	set, err := PresentSet(Filtered(r, ".", NewMatcher([]string{"build", ".git", "esc", "up"})), ".")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(set, "build") || slices.Contains(set, ".") || !slices.Contains(set, "sub/in2") {
		t.Errorf("PresentSet(.) = %v", set)
	}
	set, err = PresentSet(Filtered(r, "nested", NewMatcher([]string{"tmp", ".git"})), "nested")
	if err != nil || !slices.Equal(set, []string{".gitignore", "n.txt"}) {
		t.Errorf("PresentSet(nested) = %v, %v; want [.gitignore n.txt]", set, err)
	}
	if _, err := PresentSet(Filtered(r, ".", NewMatcher(nil)), "missing"); err == nil {
		t.Error("PresentSet of a missing root succeeded")
	}
}
