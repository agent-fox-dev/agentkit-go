package workspace

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

func TestOpenResolvesTheRootThroughSymlinks(t *testing.T) {
	target := t.TempDir()
	base := t.TempDir()
	link := filepath.Join(base, "link")
	mustSymlink(t, target, link)
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	for _, root := range []string{link, "link"} {
		ws, err := Open(root, Options{})
		if err != nil {
			t.Fatalf("Open(%q): %v", root, err)
		}
		if ws.Root() != want {
			t.Errorf("Open(%q).Root() = %q, want %q", root, ws.Root(), want)
		}
	}
}

func TestOpenOfAnEmptyRootIsTheWorkingDirectory(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "cwd")
	mustSymlink(t, target, link)
	t.Chdir(link)
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := Open("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Root() != want {
		t.Fatalf("Root() = %q, want the resolved working directory %q", ws.Root(), want)
	}
}

func TestOpenKeepsAMissingRootInItsLexicalForm(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	for _, root := range []string{filepath.Join(base, "does", "not", "exist"), "missing-rel"} {
		want, err := filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		ws, err := Open(root, Options{})
		if err != nil {
			t.Fatalf("Open(%q): %v", root, err)
		}
		if ws.Root() != want {
			t.Errorf("Open(%q).Root() = %q, want %q", root, ws.Root(), want)
		}
	}
	if _, err := os.Lstat(filepath.Join(base, "does")); err == nil {
		t.Error("Open created the missing root")
	}
}

func TestOpenFailsWhenTheWorkingDirectoryIsGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Skipf("cannot remove the working directory here: %v", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("os.Getwd still succeeds after the working directory is removed")
	}
	for _, root := range []string{"", "relative"} {
		if ws, err := Open(root, Options{}); ws != nil || err == nil {
			t.Errorf("Open(%q) = %v, %v; want nil and an error", root, ws, err)
		}
	}
}

func TestOpenReadsNoIgnoreFileAndStartsNoProcess(t *testing.T) {
	root := t.TempDir()
	// A .gitignore that blocks whoever opens it, where there are named pipes.
	trapped := mkfifo(filepath.Join(root, ".gitignore")) == nil
	bin := t.TempDir()
	log := filepath.Join(bin, "git.log")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho called >> "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	c := &counting{}
	for name, opts := range map[string]IgnoreOptions{"hooks": c.options(), "zero": {}} {
		done := make(chan error, 1)
		go func() { _, err := Open(root, Options{Ignore: opts}); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: Open: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Open did not return (opened the .gitignore: %v)", name, trapped)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("Open ran git")
	}
	if n := c.calls.Load() + c.lookups.Load(); n != 0 {
		t.Errorf("Open called its ignore hooks %d times, want 0", n)
	}
}

func TestIgnoreHandsOnTheHooksWithTheLookupMemoized(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global-ignore")
	testutil.WriteTree(t, dir, map[string]string{"global-ignore": "*.glob\n", "root/x.glob": ""})
	c := &counting{path: global}
	ws, err := Open(filepath.Join(dir, "root"), Options{Ignore: c.options()})
	if err != nil {
		t.Fatal(err)
	}
	opts := ws.Ignore()
	if opts.Getenv == nil || opts.Home == nil {
		t.Error("Ignore() dropped the Getenv or Home hook")
	}
	for range 3 {
		if got := opts.GitConfig(); got != global {
			t.Fatalf("GitConfig() = %q, want %q", got, global)
		}
	}
	// A second workspace opened with them reuses the answer.
	ws2, err := Open(ws.Root(), Options{Ignore: opts})
	if err != nil {
		t.Fatal(err)
	}
	if !ws2.Ignored("x.glob", false) {
		t.Error("the second workspace did not apply the global excludes file")
	}
	if n := c.lookups.Load(); n != 1 {
		t.Errorf("the lookup ran %d times, want 1", n)
	}
}

// answer is everything one goroutine learns from a workspace, as strings so a
// concurrent result can be compared with a sequential one.
func answer(ws *Workspace, paths []string) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }
	for _, p := range paths {
		got, err := ws.Resolve(p)
		add("resolve %q = %q, %v", p, got, err)
		add("rel %q = %q", p, ws.Rel(filepath.Join(ws.Root(), p)))
		add("write %q = %v", p, ws.CheckWriteTarget(filepath.Join(ws.Root(), p)))
		add("ignored %q = %v %v", p, ws.Ignored(p, false), ws.Ignored(p, true))
	}
	var walked []string
	err := ws.Walk(context.Background(), "", WalkOptions{IncludeHidden: true}, func(rel string, _ fs.DirEntry) error {
		walked = append(walked, rel)
		return nil
	})
	slices.Sort(walked)
	add("walk %v %v", walked, err)
	return out
}

func TestConcurrentUseGivesTheSequentialAnswers(t *testing.T) {
	root := testutil.Tree(t, map[string]string{
		".gitignore": "*.log\n!keep.log\nbuild/\n", "a/.gitignore": "/top\n", "a/top": "", "a/b/top": "",
		"x.log": "", "keep.log": "", "build/o": "", "v/.git/HEAD": "", "v/x.log": "", ".hid/f": "", "g.glob": "",
	})
	mustSymlink(t, t.TempDir(), filepath.Join(root, "out"))
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.glob\n")})
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"a/top", "a/b/top", "x.log", "keep.log", "build/o", "v/x.log", ".hid/f", "g.glob", "out/f", "../x", "new/f"}
	want := answer(ws, paths)
	got := make([][]string, 16)
	var wg sync.WaitGroup
	for i := range got {
		wg.Go(func() { got[i] = answer(ws, paths) })
	}
	wg.Wait()
	for i, g := range got {
		if !slices.Equal(g, want) {
			t.Fatalf("goroutine %d: %v\nwant (sequential): %v", i, g, want)
		}
	}
}
