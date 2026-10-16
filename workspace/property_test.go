package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// genTree writes a random tree under root and returns its directories, root
// ("."), included. The tree has nested .gitignore files with negations,
// repository roots (.git directories, now and then a .git file, with and
// without .git/info/exclude) and hidden entries: everything the ignore engine
// has a rule about.
func genTree(t *testing.T, r *rand.Rand, root string) []string {
	t.Helper()
	names := []string{"a", "b", "c", "build", "tmp", ".hid", "keep.log", "x.log", "top", "ab", "d.txt"}
	pats := []string{"*.log", "build/", "!keep.log", "/top", "tmp", "a*", "!ab", "b/c", "**/x.log", "!*.txt", "*.txt", ".hid"}
	pat := func() string { return pats[r.IntN(len(pats))] }
	write := func(rel, body string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var dirs []string
	var mk func(dir string, depth int)
	mk = func(dir string, depth int) {
		dirs = append(dirs, dir)
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
		if r.IntN(4) == 0 {
			if r.IntN(5) == 0 {
				write(joinRel(dir, ".git"), "gitdir: elsewhere\n")
			} else {
				write(joinRel(dir, ".git/HEAD"), "ref: refs/heads/main\n")
				if r.IntN(2) == 0 {
					write(joinRel(dir, ".git/info/exclude"), pat()+"\n"+pat()+"\n")
				}
			}
		}
		if r.IntN(2) == 0 {
			var b strings.Builder
			for i := 0; i < 1+r.IntN(3); i++ {
				b.WriteString(pat() + "\n")
			}
			write(joinRel(dir, ".gitignore"), b.String())
		}
		for _, n := range names {
			if r.IntN(3) != 0 {
				continue
			}
			if depth < 3 && r.IntN(2) == 0 {
				mk(joinRel(dir, n), depth+1)
			} else {
				write(joinRel(dir, n), "x")
			}
		}
	}
	mk(".", 0)
	return dirs
}

func joinRel(dir, name string) string {
	if dir == "." {
		return name
	}
	return dir + "/" + name
}

// TS-20-32: Walk from the root visits exactly the WalkDir entries that Ignored
// reports as not ignored.
func TestWalkAndIgnoredAgreeOnAnyTree_TS20_32(t *testing.T) {
	trees := 150
	if testing.Short() {
		trees = 20
	}
	for seed := uint64(1); seed <= uint64(trees); seed++ {
		root := t.TempDir()
		genTree(t, rand.New(rand.NewPCG(seed, 32)), root)
		ws := openIgnoring(t, root)

		visited := map[string]bool{}
		err := ws.Walk(context.Background(), "", WalkOptions{IncludeHidden: true}, func(rel string, _ fs.DirEntry) error {
			visited[rel] = true
			return nil
		})
		if err != nil {
			t.Fatalf("seed %d: Walk: %v", seed, err)
		}

		expected := map[string]bool{}
		err = filepath.WalkDir(ws.Root(), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(ws.Root(), p)
			if err != nil {
				return err
			}
			if rel == "." {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if !ws.Ignored(rel, d.IsDir()) {
				expected[rel] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed %d: WalkDir: %v", seed, err)
		}

		if !sameSet(visited, expected) {
			t.Fatalf("seed %d: Walk and Ignored disagree\n only Walk visited:   %v\n only Ignored allows: %v\n tree: %v",
				seed, onlyIn(visited, expected), onlyIn(expected, visited), listTree(t, ws.Root()))
		}
	}
}

func sameSet(a, b map[string]bool) bool {
	return len(onlyIn(a, b)) == 0 && len(onlyIn(b, a)) == 0
}

func onlyIn(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func listTree(t *testing.T, root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != root {
			rel, _ := filepath.Rel(root, p)
			if !d.IsDir() && (d.Name() == ".gitignore" || d.Name() == "exclude") {
				body, _ := os.ReadFile(p)
				rel += fmt.Sprintf(" %q", body)
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	return out
}

// answer is everything one goroutine learns from a workspace, as strings so a
// parallel result can be compared with a sequential one.
func answer(ws *Workspace, paths []string) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	add("root %s", ws.Root())
	for _, p := range paths {
		got, err := ws.Resolve(p)
		add("resolve %q = %q, %v", p, got, err)
		add("rel %q = %q", p, ws.Rel(filepath.Join(ws.Root(), p)))
		add("write %q = %v", p, ws.CheckWriteTarget(filepath.Join(ws.Root(), p)))
		add("ignored %q = %v %v", p, ws.Ignored(p, false), ws.Ignored(p, true))
	}
	opts := ws.Ignore()
	add("ignore %q %v %v", opts.GitConfig(), opts.Getenv != nil, opts.Home != nil)
	var walked []string
	err := ws.Walk(context.Background(), "", WalkOptions{IncludeHidden: true}, func(rel string, _ fs.DirEntry) error {
		walked = append(walked, rel)
		return nil
	})
	slices.Sort(walked)
	add("walk %v %v", walked, err)
	return out
}

// TS-20-7: Concurrent use of one Workspace gives sequential results and no
// data race (run under go test -race).
func TestConcurrentUseMatchesSequentialUse_TS20_7(t *testing.T) {
	root := t.TempDir()
	genTree(t, rand.New(rand.NewPCG(7, 7)), root)
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.glob\n")})
	if err != nil {
		t.Fatal(err)
	}

	r := rand.New(rand.NewPCG(70, 7))
	segs := []string{"a", "b", "c", "build", "tmp", ".hid", "keep.log", "x.log", "top", "ab", "d.txt", "..", "new"}
	makePaths := func() []string {
		var ps []string
		for i := 0; i < 12; i++ {
			n := 1 + r.IntN(4)
			parts := make([]string, n)
			for j := range parts {
				parts[j] = segs[r.IntN(len(segs))]
			}
			ps = append(ps, strings.Join(parts, "/"))
		}
		return ps
	}

	for _, n := range []int{1, 2, 7, 16, 64} {
		paths := makePaths()
		want := answer(ws, paths)

		got := make([][]string, n)
		var wg sync.WaitGroup
		for i := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got[i] = answer(ws, paths)
			}()
		}
		wg.Wait()
		for i, g := range got {
			if !slices.Equal(g, want) {
				t.Fatalf("%d goroutines: goroutine %d got a different answer than the sequential run\n got:  %v\n want: %v", n, i, g, want)
			}
		}
	}
}
