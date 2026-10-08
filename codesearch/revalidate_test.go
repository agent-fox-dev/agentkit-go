//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// revalFixture builds an index over n small Go files, runs the first search to
// build it, and returns it with a search function. Unlike gitignoreFixture it
// leaves the files' mtimes and the index's records as they are, so the files
// are inside the racy window of the build, as in a fresh checkout. When
// oldFiles is set the files' mtimes are an hour old before the build instead.
func revalFixture(t *testing.T, n int, oldFiles bool) (root string, idx *Index, search func(ctx context.Context, q string) core.ToolResult) {
	t.Helper()

	root = t.TempDir()
	for i := 0; i < n; i++ {
		mkFile(t, root, fmt.Sprintf("f%03d.go", i), fmt.Sprintf("package pkg\n// token%03d\nfunc F%03d() {}\n", i, i))
	}
	if oldFiles {
		old := time.Now().Add(-time.Hour)
		for i := 0; i < n; i++ {
			if err := os.Chtimes(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err = newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })

	tool := idx.Tools()[0]
	search = func(ctx context.Context, q string) core.ToolResult {
		in, _ := json.Marshal(map[string]any{"query": q})
		return tool.Execute(ctx, in)
	}
	if r := search(context.Background(), "token000"); !r.OK {
		t.Fatalf("first search: %s: %s", r.Error, r.Detail)
	}
	return root, idx, search
}

func dirtyFiles(t *testing.T, r core.ToolResult) int {
	t.Helper()
	n, ok := r.Data["dirty_files"].(int)
	if !ok {
		t.Fatalf("dirty_files = %T(%v), want int", r.Data["dirty_files"], r.Data["dirty_files"])
	}
	return n
}

// 03-REQ-8.2: a call whose context ends during the revalidation walk returns
// aborted. The walk is not run to the end, is not counted, and leaves the
// state alone: the next call revalidates in full.
func TestRevalidationIsCancellable(t *testing.T) {
	root, idx, search := revalFixture(t, 20, true)

	os.Remove(filepath.Join(root, "f005.go"))
	idx.Invalidate("")

	// The hook runs just before the walk; ending the context there is the
	// deterministic way to end it during the walk.
	ctx, cancel := context.WithCancel(context.Background())
	idx.testRevalHook = func() { cancel() }
	before := idx.RevalCount()
	r := search(ctx, "token000")
	idx.testRevalHook = nil

	if r.OK || r.Error != "aborted" {
		t.Fatalf("a call whose context ended during revalidation = ok:%v error:%q, want aborted", r.OK, r.Error)
	}
	if got := idx.RevalCount(); got != before {
		t.Errorf("revalidations %d -> %d: an abandoned walk must not count as completed", before, got)
	}

	// Nothing the abandoned walk did may hide a file, and the whole-index mark
	// must survive, so the next call walks again and sees the deletion.
	r = search(context.Background(), "token000")
	if !r.OK {
		t.Fatalf("next call: %s: %s", r.Error, r.Detail)
	}
	if got := getResultFilePaths(r); !slices.Equal(got, []string{"f000.go"}) {
		t.Errorf("after the abandoned walk results = %v, want [f000.go]", got)
	}
	if got := idx.RevalCount(); got != before+1 {
		t.Errorf("revalidations after the next call = %d, want %d", got, before+1)
	}
	r = search(context.Background(), "token005")
	if got := getResultFilePaths(r); len(got) != 0 {
		t.Errorf("deleted f005.go still found after revalidation: %v", got)
	}
}

// Symbols, which find_symbol calls under a 2 s bound, abandons the walk too.
func TestSymbolsAbandonsTheRevalidationWalk(t *testing.T) {
	_, idx, _ := revalFixture(t, 20, true)

	idx.Invalidate("")
	ctx, cancel := context.WithCancel(context.Background())
	idx.testRevalHook = func() { cancel() }
	before := idx.RevalCount()
	_, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "F"})
	idx.testRevalHook = nil

	if err == nil || ok {
		t.Errorf("Symbols = ok:%v err:%v, want the context's error", ok, err)
	}
	if got := idx.RevalCount(); got != before {
		t.Errorf("revalidations %d -> %d: an abandoned walk must not count as completed", before, got)
	}
}

// 03-REQ-6.2: revalidation compares (size, mtime). A file whose size and mtime
// match what was indexed, and whose mtime is well before the build, is neither
// opened nor read: it stays in the index even though its content cannot be
// read now.
func TestRevalidationDoesNotOpenUnchangedFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with mode 000")
	}
	root, idx, search := revalFixture(t, 20, true)

	if err := os.Chmod(filepath.Join(root, "f007.go"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(root, "f007.go"), 0o644) })

	idx.Invalidate("")
	r := search(context.Background(), "token007")
	if !r.OK {
		t.Fatalf("search: %s: %s", r.Error, r.Detail)
	}
	if got := getResultFilePaths(r); !slices.Equal(got, []string{"f007.go"}) {
		t.Errorf("results = %v, want [f007.go]: an unchanged file must not be re-read by the walk", got)
	}
	if n := dirtyFiles(t, r); n != 0 {
		t.Errorf("dirty_files = %d, want 0", n)
	}
}

// 03-REQ-6.2 and 03-REQ-6.5: a revalidation shortly after a build must not
// mark every file dirty. 100 files created just before the build are all
// inside the racy window, none has changed, and no rebuild may follow.
func TestRevalidationInsideTheRacyWindowDoesNotMarkEveryFileDirty(t *testing.T) {
	_, idx, search := revalFixture(t, 100, false)

	idx.Invalidate("")
	r := search(context.Background(), "token001")
	if !r.OK {
		t.Fatalf("search: %s: %s", r.Error, r.Detail)
	}
	if n := dirtyFiles(t, r); n != 0 {
		t.Errorf("dirty_files = %d, want 0: nothing changed", n)
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1: an unchanged tree must not be rebuilt", n)
	}
}

// 03-REQ-6.2: the window exists to catch a same-size edit whose mtime did not
// move. Only that file is dirty, and its new content is found.
func TestRacyWindowStillCatchesASameSizeSameMtimeEdit(t *testing.T) {
	root, idx, search := revalFixture(t, 100, false)

	path := filepath.Join(root, "f010.go")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same size, and the mtime put back as if the filesystem's timestamp
	// granularity had hidden the edit.
	if err := os.WriteFile(path, []byte("package pkg\n// toXXX010\nfunc F010() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Stat(path); now.Size() != fi.Size() || !now.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("fixture error: size/mtime changed (%d,%v) vs (%d,%v)", now.Size(), now.ModTime(), fi.Size(), fi.ModTime())
	}

	idx.Invalidate("")
	r := search(context.Background(), "toXXX010")
	if !r.OK {
		t.Fatalf("search: %s: %s", r.Error, r.Detail)
	}
	if got := getResultFilePaths(r); !slices.Equal(got, []string{"f010.go"}) {
		t.Errorf("results = %v, want [f010.go]: the same-size edit was missed", got)
	}
	if n := dirtyFiles(t, r); n != 1 {
		t.Errorf("dirty_files = %d, want 1 (only the edited file)", n)
	}
	if n := idx.BuildCount(); n != 1 {
		t.Errorf("BuildCount = %d, want 1", n)
	}
}

// A racy file is compared with its indexed content only until a comparison has
// been made after its timestamp granule closed: from then on no same-mtime
// edit is possible, so the walk trusts (size, mtime) and does not read the
// file again. Without that a freshly written tree would be read in full by
// every walk.
func TestVerifiedRacyFileIsNotReadAgain(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a file with mode 000")
	}

	root := t.TempDir()
	// mtimes 1.5 s old: inside the racy window of the build (racy), and the
	// granule closes 0.5 s from now.
	recent := time.Now().Add(-1500 * time.Millisecond)
	for i := 0; i < 20; i++ {
		rel := fmt.Sprintf("f%03d.go", i)
		mkFile(t, root, rel, fmt.Sprintf("package pkg\n// token%03d\nfunc F%03d() {}\n", i, i))
		if err := os.Chtimes(filepath.Join(root, rel), recent, recent); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := newIndex(ws, Options{TempDir: t.TempDir(), Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	tool := idx.Tools()[0]
	search := func(q string) core.ToolResult {
		in, _ := json.Marshal(map[string]any{"query": q})
		return tool.Execute(context.Background(), in)
	}
	if r := search("token000"); !r.OK {
		t.Fatalf("first search: %s: %s", r.Error, r.Detail)
	}

	time.Sleep(700 * time.Millisecond) // the granule is closed now

	// First walk: reads the racy files, finds them unchanged, remembers it.
	idx.Invalidate("")
	if r := search("token007"); !r.OK || !slices.Equal(getResultFilePaths(r), []string{"f007.go"}) {
		t.Fatalf("after the first walk: ok:%v results:%v", r.OK, getResultFilePaths(r))
	}

	// Second walk: f007.go cannot be read, and must not need to be.
	path := filepath.Join(root, "f007.go")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	idx.Invalidate("")
	r := search("token007")
	if !r.OK || !slices.Equal(getResultFilePaths(r), []string{"f007.go"}) {
		t.Errorf("after the second walk: ok:%v results:%v, want [f007.go]: a verified file was read again",
			r.OK, getResultFilePaths(r))
	}
	if got := idx.RevalCount(); got != 2 {
		t.Errorf("RevalCount = %d, want 2 walks", got)
	}
}
