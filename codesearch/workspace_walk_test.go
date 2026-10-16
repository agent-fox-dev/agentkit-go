//go:build !windows

package codesearch

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-52: codesearch.New takes a workspace, builds through ws.Walk and
// revalidates with the workspace's rules: a file added and a file a .gitignore
// edit un-ignores both appear, and what a nested .gitignore still ignores does
// not.
func TestNewTakesAWorkspaceAndRevalidatesWithItsRules_TS20_52(t *testing.T) {
	root := t.TempDir()
	// Filler keeps the few dirty paths under the 5% rebuild threshold.
	for i := 0; i < 100; i++ {
		mkFile(t, root, fmt.Sprintf("f%03d.go", i), fmt.Sprintf("package pkg\nfunc F%03d() {}\n", i))
	}
	const body = "package pkg\n// needleToken\nfunc F() {}\n"
	mkFile(t, root, ".gitignore", "ignored.go\n")
	mkFile(t, root, "nested/.gitignore", "inner.go\n")
	for _, rel := range []string{"keep.go", "ignored.go", "nested/inner.go", "nested/ok.go"} {
		mkFile(t, root, rel, body)
	}

	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	built, err := New(ws, Options{TempDir: t.TempDir()})
	if err != nil || built == nil {
		t.Fatalf("New = (%v, %v), want an index and no error", built, err)
	}
	idx := built.(*Index)
	t.Cleanup(func() { idx.Close() })

	search := func() []string {
		t.Helper()
		in := fmt.Sprintf(`{"query":%q}`, "needleToken")
		r := idx.Tools()[0].Execute(context.Background(), []byte(in))
		if !r.OK {
			t.Fatalf("search: %s: %s", r.Error, r.Detail)
		}
		got := getResultFilePaths(r)
		slices.Sort(got)
		return got
	}

	if got, want := search(), []string{"keep.go", "nested/ok.go"}; !slices.Equal(got, want) {
		t.Fatalf("the built index holds %v, want %v: the workspace's .gitignore files apply", got, want)
	}

	// Backdate past the racy window, so revalidation trusts unchanged files.
	idx.mu.Lock()
	for rel, fi := range idx.fileInfos {
		fi.indexedAt = time.Now().Add(-time.Hour)
		idx.fileInfos[rel] = fi
	}
	idx.mu.Unlock()

	mkFile(t, root, "added.go", body)
	mkFile(t, root, ".gitignore", "nothing.txt\n") // un-ignores ignored.go
	idx.Invalidate("added.go")
	idx.Invalidate(".gitignore")
	builds, revals := idx.BuildCount(), idx.RevalCount()
	want := []string{"added.go", "ignored.go", "keep.go", "nested/ok.go"}
	if got := search(); !slices.Equal(got, want) {
		t.Errorf("after revalidation the index holds %v, want %v", got, want)
	}
	// The revalidation walk found the un-ignored file itself: a walk that
	// missed files would mark them removed, cross the rebuild threshold and
	// be repaired by a full build, which would also give this answer.
	if got := idx.RevalCount(); got != revals+1 {
		t.Errorf("revalidation walks went from %d to %d, want one more", revals, got)
	}
	if got := idx.BuildCount(); got != builds {
		t.Errorf("builds went from %d to %d: the change was repaired by a rebuild, not by revalidation", builds, got)
	}
}

// TS-20-54: the index holds exactly the files ws.Walk visits from the root with
// hidden entries off.
func TestIndexedFilesAreTheWalkSet_TS20_54(t *testing.T) {
	trees := 25
	if testing.Short() {
		trees = 5
	}
	for seed := uint64(1); seed <= uint64(trees); seed++ {
		root := t.TempDir()
		genIndexTree(t, rand.New(rand.NewPCG(seed, 54)), root)
		ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
		if err != nil {
			t.Fatal(err)
		}

		want := map[string]bool{}
		err = ws.Walk(context.Background(), "", workspace.WalkOptions{}, func(rel string, d fs.DirEntry) error {
			if d.Type().IsRegular() {
				want[rel] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("seed %d: Walk: %v", seed, err)
		}

		built, err := New(ws, Options{TempDir: t.TempDir()})
		if err != nil {
			t.Fatalf("seed %d: New: %v", seed, err)
		}
		idx := built.(*Index)
		if err := idx.Build(context.Background()); err != nil {
			idx.Close()
			t.Fatalf("seed %d: Build: %v", seed, err)
		}
		got := idx.IndexedFiles()
		idx.Close()

		if !sameFileSet(got, want) {
			t.Fatalf("seed %d: the index and ws.Walk disagree\n only indexed: %v\n only walked:  %v",
				seed, onlyIn(got, want), onlyIn(want, got))
		}
	}
}

func sameFileSet(a, b map[string]bool) bool { return len(onlyIn(a, b)) == 0 && len(onlyIn(b, a)) == 0 }

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

// genIndexTree writes a random tree of small text files: nested .gitignore
// files with negations, nested repositories and hidden entries.
func genIndexTree(t *testing.T, r *rand.Rand, root string) {
	t.Helper()
	names := []string{"a.go", "b.go", "c.txt", "build", "tmp", ".hid", "keep.log", "x.log", "top", "ab.go", "sub"}
	pats := []string{"*.log", "build/", "!keep.log", "/top", "tmp", "a*", "!ab.go", "b.go", "**/x.log", "*.txt", ".hid"}
	var mk func(dir string, depth int)
	mk = func(dir string, depth int) {
		if r.IntN(4) == 0 {
			mkFile(t, root, joinRel(dir, ".git/HEAD"), "ref: refs/heads/main\n")
		}
		if r.IntN(2) == 0 {
			mkFile(t, root, joinRel(dir, ".gitignore"), pats[r.IntN(len(pats))]+"\n"+pats[r.IntN(len(pats))]+"\n")
		}
		for _, n := range names {
			if r.IntN(3) != 0 {
				continue
			}
			p := joinRel(dir, n)
			if depth < 3 && r.IntN(2) == 0 {
				mk(p, depth+1)
				continue
			}
			mkFile(t, root, p, "package p\n\nfunc F() {}\n")
		}
	}
	mk(".", 0)
	mkFile(t, root, "main.go", "package main\n\nfunc main() {}\n") // never an empty tree
}

func joinRel(dir, name string) string {
	if dir == "." {
		return name
	}
	return filepath.ToSlash(filepath.Join(dir, name))
}
