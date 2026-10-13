//go:build unix

package landlock

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// mustWrite writes a file, failing t.
func mustWrite(t *testing.T, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// chmodTree makes a tree read-only, like chmod -R a-w, children first.
func chmodTree(t *testing.T, root string) {
	t.Helper()
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		paths = append(paths, p)
		return err
	})
	slices.Reverse(paths)
	for _, p := range paths {
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, fi.Mode().Perm()&^0o222); err != nil {
			t.Fatal(err)
		}
	}
}

// TS-18-45: removal restores owner permissions so read-only trees go
// completely, and a missing directory is not an error. 18-REQ-7.4.
func TestRemoveTreeReadOnly_TS18_45(t *testing.T) {
	d := filepath.Join(t.TempDir(), "sandbox")
	mod := filepath.Join(d, "home", "go", "pkg", "mod", "x@v1.0.0")
	mustWrite(t, filepath.Join(mod, "sub", "a.go"), "package a", 0o444)
	mustWrite(t, filepath.Join(mod, "b.go"), "package b", 0o444)
	for _, p := range []string{filepath.Join(mod, "sub"), mod} {
		if err := os.Chmod(p, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(d, "workspace", "ro", "a", "b", "f"), "x", 0o644)
	chmodTree(t, filepath.Join(d, "workspace", "ro"))
	if err := os.Chmod(d, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := removeTree(d); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(d); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree remains: %v", err)
	}
	if err := removeTree(d); err != nil {
		t.Errorf("removing a missing tree: %v", err)
	}
}

// snapshot records every path beneath root: its mode and, for files, a hash.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		v := fi.Mode().String()
		if fi.Mode().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			v += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		snap[p] = v
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// TS-18-46: removal deletes any tree mixing read-only directories,
// read-only files and outward symlinks, leaving every target unchanged.
// 18-REQ-7.5.
func TestRemoveTreeProperty_TS18_46(t *testing.T) {
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "f1"), "one", 0o644)
	mustWrite(t, filepath.Join(outside, "ro"), "ro", 0o444)
	mustWrite(t, filepath.Join(outside, "d", "inner"), "inner", 0o600)
	before := snapshot(t, outside)
	targets := []string{filepath.Join(outside, "f1"), filepath.Join(outside, "ro"), filepath.Join(outside, "d"), outside}
	for seed := uint64(1); seed <= 120; seed++ {
		r := rand.New(rand.NewPCG(seed, 46))
		root := filepath.Join(t.TempDir(), "tree")
		var dirs []string
		var gen func(dir string, depth int)
		gen = func(dir string, depth int) {
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			dirs = append(dirs, dir)
			for i := range 1 + r.IntN(4) {
				p := filepath.Join(dir, fmt.Sprintf("e%d", i))
				switch k := r.IntN(4); {
				case k == 0 && depth < 4:
					gen(p, depth+1)
				case k == 1:
					if err := os.Symlink(targets[r.IntN(len(targets))], p); err != nil {
						t.Fatal(err)
					}
				default:
					mustWrite(t, p, "x", []os.FileMode{0o444, 0o644}[r.IntN(2)])
				}
			}
		}
		gen(root, 0)
		slices.Reverse(dirs)
		for _, d := range dirs {
			if err := os.Chmod(d, []os.FileMode{0o500, 0o555, 0o700}[r.IntN(3)]); err != nil {
				t.Fatal(err)
			}
		}
		if err := removeTree(root); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("seed %d: the tree remains", seed)
		}
		if after := snapshot(t, outside); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("seed %d: the outside changed:\n%v\nwant\n%v", seed, after, before)
		}
	}
}
