// Package treegen generates seeded file trees for the LocalDir property
// tests, and the oracles they are checked against. Only tests import it.
package treegen

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// names are the file and directory names trees draw from: wildcard
// characters, spaces and hidden names included.
var names = []string{"a.txt", "b.go", "keep.log", "x.log", "sp ace.txt", "q?x", "br[c]k", "st*r", ".hidden",
	"notes.md", "out.o", "secret.txt", "data.bin", "-dash", "unié.txt"}

// dirs are the directory names trees draw from.
var dirs = []string{"src", "build", "tmp", "docs", "sp ace", "lib", "cache", ".config", "w*ld"}

// patterns are the .gitignore lines trees draw from.
var patterns = []string{"*.log", "!keep.log", "build/", "tmp", "/out.o", "*.o", "sp ace.txt", "q\\?x", "cache/",
	"docs/*.md", "secret.txt", ".hidden", "*.bin", "w\\*ld/"}

// tree writes generated content into root.
type tree struct {
	t    *testing.T
	r    *rand.Rand
	root string
	// outside is a directory escaping symlinks may point at.
	outside string
}

func (g *tree) write(rel, body string, mode os.FileMode) {
	g.t.Helper()
	p := filepath.Join(g.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		return // a directory or link already took the name
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		g.t.Fatal(err)
	}
}

func (g *tree) link(rel, target string) {
	g.t.Helper()
	p := filepath.Join(g.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		g.t.Fatal(err)
	}
	if _, err := os.Lstat(p); err == nil {
		return
	}
	if err := os.Symlink(target, p); err != nil {
		g.t.Fatal(err)
	}
}

func (g *tree) pick(from []string) string { return from[g.r.IntN(len(from))] }

func (g *tree) gitignore(dir string, n int) {
	var lines []string
	for range n {
		lines = append(lines, g.pick(patterns))
	}
	g.write(path.Join(dir, ".gitignore"), strings.Join(lines, "\n")+"\n", 0o644)
}

func (g *tree) files(dir string, n int) {
	for range n {
		mode := os.FileMode(0o644)
		if g.r.IntN(4) == 0 {
			mode = 0o755
		}
		g.write(path.Join(dir, g.pick(names)), fmt.Sprintf("%s %d\n", dir, g.r.Int()), mode)
	}
}

// repo writes a .git directory under dir.
func (g *tree) repo(dir string) {
	g.write(path.Join(dir, ".git/HEAD"), "ref: refs/heads/main\n", 0o644)
	g.write(path.Join(dir, ".git/config"), "[core]\n\tbare = false\n", 0o644)
	g.write(path.Join(dir, ".git/objects/ab/cdef"), fmt.Sprintf("obj %d", g.r.Int()), 0o444)
	g.write(path.Join(dir, ".git/info/exclude"), g.pick(patterns)+"\n", 0o644)
}

// links writes internal and escaping symlinks under dir.
func (g *tree) links(dir string) {
	depth := strings.Count(dir, "/") + 1
	if dir == "." {
		depth = 0
	}
	up := strings.Repeat("../", depth)
	switch g.r.IntN(3) {
	case 0:
		g.link(path.Join(dir, "in-link"), up+"a.txt")
	case 1:
		g.link(path.Join(dir, "in-dir"), ".")
	}
	switch g.r.IntN(4) {
	case 0:
		g.link(path.Join(dir, "abs-link"), "/etc")
	case 1:
		g.link(path.Join(dir, "up-link"), up+"../escape")
	case 2:
		g.link(path.Join(dir, "out-link"), g.outside)
	}
}

// fill writes a generated tree into root: nested .gitignore files with `!`
// re-includes, .git/info/exclude, a nested repository, hidden files,
// internal and escaping symlinks, and names with wildcards and spaces.
func (g *tree) fill(git bool) {
	g.files(".", 2+g.r.IntN(4))
	g.gitignore(".", 1+g.r.IntN(4))
	if git {
		g.repo(".")
	}
	g.links(".")
	for range 2 + g.r.IntN(3) {
		d := g.pick(dirs)
		if g.r.IntN(2) == 0 {
			d = path.Join(d, g.pick(dirs))
		}
		g.files(d, 1+g.r.IntN(4))
		if g.r.IntN(2) == 0 {
			g.gitignore(d, 1+g.r.IntN(2))
		}
		if g.r.IntN(3) == 0 {
			g.links(d)
		}
	}
	if g.r.IntN(2) == 0 {
		g.repo("nested")
		g.files("nested", 2)
		g.gitignore("nested", 1)
	}
}

// Gen writes the tree for seed under a fresh directory and returns it.
func Gen(t *testing.T, seed uint64) string {
	t.Helper()
	t.Logf("treegen seed %d", seed)
	root := t.TempDir()
	g := &tree{t: t, r: rand.New(rand.NewPCG(seed, 19)), root: root, outside: t.TempDir()}
	g.fill(true)
	return root
}

// Pair writes a (source, receiving) pair for seed into src and recv:
// overlapping names, different .gitignore patterns, escaping symlinks named
// like directories the other side has, and optional .git directories.
// outside is where escaping symlinks point.
func Pair(t *testing.T, seed uint64, src, recv, outside string) {
	t.Helper()
	t.Logf("treegen pair seed %d", seed)
	r := rand.New(rand.NewPCG(seed, 16))
	s := &tree{t: t, r: r, root: src, outside: outside}
	v := &tree{t: t, r: r, root: recv, outside: outside}
	s.fill(r.IntN(2) == 0)
	v.fill(r.IntN(2) == 0)
	// An escaping link on each side named like a directory on the other.
	for _, d := range []string{"src", "lib", "cache"} {
		if r.IntN(2) == 0 {
			s.files(d, 1)
			v.link(d, outside)
		} else {
			v.files(d, 1)
			s.link(d, outside)
		}
	}
}

// visited is the set of entries tools.Walk visits under root with
// IncludeHidden and no global excludes.
func visited(t *testing.T, root string) map[string]bool {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	err = tools.Walk(context.Background(), ws, ws.Root, tools.WalkOptions{Ignore: tools.NoGlobalExcludes(), IncludeHidden: true},
		func(rel string, _ fs.DirEntry) error { out[rel] = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// WalkSet is every entry tools.Walk lists under root with IncludeHidden.
func WalkSet(t *testing.T, root string) map[string]bool { return visited(t, root) }

func hasGit(rel string) bool { return slices.Contains(strings.Split(rel, "/"), ".git") }

// GitTrees is every entry of every .git whose parent Walk lists (root's own
// .git included), in full.
func GitTrees(t *testing.T, root string) map[string]bool {
	t.Helper()
	listed := visited(t, root)
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel := Rel(root, p)
		if d.Name() == ".git" {
			if parent := path.Dir(rel); parent == "." || listed[parent] {
				_ = filepath.WalkDir(p, func(q string, _ fs.DirEntry, err error) error {
					if err == nil {
						out[Rel(root, q)] = true
					}
					return nil
				})
			}
			return skipDir(d)
		}
		return nil
	})
	return out
}

// Ignored is every entry filepath.WalkDir finds that tools.Walk does not
// visit, whose parent Walk visits, and that has no .git component: one
// entry per ignored directory, at its highest ignored level.
func Ignored(t *testing.T, root string) map[string]bool {
	t.Helper()
	listed := visited(t, root)
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel := Rel(root, p)
		if hasGit(rel) {
			return skipDir(d)
		}
		if !listed[rel] {
			if parent := path.Dir(rel); parent == "." || listed[parent] {
				out[rel] = true
			}
			return skipDir(d)
		}
		return nil
	})
	return out
}

// Escaping is every symlink under root whose target tarsafe's predicate
// says leaves root.
func Escaping(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			if r := Rel(root, p); tarsafe.SymlinkEscapes(r, target) {
				out[r] = true
			}
		}
		return nil
	})
	return out
}

// Snapshot records, for each path and everything under it, existence,
// type, mode, content, modification time and link target.
func Snapshot(t *testing.T, root string, paths map[string]bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for p := range paths {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if _, err := os.Lstat(abs); err != nil {
			out[p] = "absent"
			continue
		}
		_ = filepath.WalkDir(abs, func(q string, d fs.DirEntry, err error) error {
			r := Rel(root, q)
			if err != nil {
				out[r] = "unreadable"
				return nil
			}
			out[r] = Describe(q, true)
			return nil
		})
	}
	return out
}

// Entries maps every entry under root to Describe of it, without
// modification times.
func Entries(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != root {
			out[Rel(root, p)] = Describe(p, false)
		}
		return nil
	})
	return out
}

// Describe is an entry's type, permission bits, and content or link
// target, with its modification time when withTime.
func Describe(p string, withTime bool) string {
	fi, err := os.Lstat(p)
	if err != nil {
		return "absent"
	}
	var s string
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		l, _ := os.Readlink(p)
		s = "link " + l
	case fi.IsDir():
		s = fmt.Sprintf("dir %v", fi.Mode().Perm())
	default:
		b, _ := os.ReadFile(p)
		s = fmt.Sprintf("file %v %q", fi.Mode().Perm(), b)
	}
	if withTime && fi.Mode()&fs.ModeSymlink == 0 {
		s += " " + fi.ModTime().String()
	}
	return s
}

// Rel is p relative to root, slash-separated.
func Rel(root, p string) string {
	r, _ := filepath.Rel(root, p)
	return filepath.ToSlash(r)
}

func skipDir(d fs.DirEntry) error {
	if d.IsDir() {
		return fs.SkipDir
	}
	return nil
}
