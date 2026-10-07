//go:build !windows

package codesearch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #76 §4: an overlay shard lives under the run directory, so the run
// directory's sweep removes it when a process dies without Close, and a
// rebuild's removal of the old run directory takes it along.
func TestOverlayShardsLiveUnderTheRunDirectory(t *testing.T) {
	files := map[string]string{"a.go": "package p\n// needle one\n"}
	for i := 0; i < 30; i++ { // one dirty file in 31 stays under the rebuild threshold
		files[fmt.Sprintf("f%02d.go", i)] = "package p\n"
	}
	idx, search := devFixture(t, files, Options{})
	if r := search("needle", nil); !r.OK {
		t.Fatalf("first search: %s %s", r.Error, r.Detail)
	}
	if err := os.WriteFile(filepath.Join(idx.ws.Root, "a.go"), []byte("package p\n// needle two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx.Invalidate("a.go")
	if r := search("needle", nil); !r.OK || !strings.Contains(r.Text, "needle two") {
		t.Fatalf("search after the edit: ok=%v %s", r.OK, r.Text)
	}

	idx.mu.RLock()
	ov, runDir := idx.overlay, idx.runDir
	idx.mu.RUnlock()
	if ov == nil || ov.dir == "" {
		t.Fatal("no overlay shard was built for the dirty file")
	}
	if rel, err := filepath.Rel(runDir, ov.dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("overlay shard %s is outside the run directory %s; the sweep would never find it", ov.dir, runDir)
	}
}

// Issue #76 §5: context_lines 0 means no context, as in search_files; only
// an absent context_lines takes the default.
func TestContextLinesZeroMeansNone(t *testing.T) {
	_, search := devFixture(t, map[string]string{
		"a.go": "package p\n\n// before the match\nvar needle = 1\n// after the match\n",
	}, Options{})
	if r := search("needle", nil); !r.OK || !strings.Contains(r.Text, "before the match") {
		t.Fatalf("with context_lines absent the default context is shown:\n%s", r.Text)
	}
	r := search("needle", map[string]any{"context_lines": 0})
	if !r.OK {
		t.Fatalf("%s %s", r.Error, r.Detail)
	}
	if strings.Contains(r.Text, "before the match") || strings.Contains(r.Text, "after the match") {
		t.Fatalf("context_lines 0 returned context:\n%s", r.Text)
	}
}

// Issue #76 §5: a shard directory swapped out by a rebuild (or an overlay
// replaced by another query) is removed only once the last query reading it
// is done. Removing it at once made a query that had already read the path
// fail with "open searcher".
func TestARetiredShardDirectoryOutlivesItsReaders(t *testing.T) {
	idx, _ := devFixture(t, map[string]string{"a.go": "package p\n"}, Options{})
	dir := filepath.Join(t.TempDir(), "shards")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	release := idx.leaseDir(dir)
	idx.retireDir(dir)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the directory was removed while a reader held it: %v", err)
	}
	release()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory survived its last reader: %v", err)
	}

	// A directory nobody reads is removed when it is retired.
	other := filepath.Join(t.TempDir(), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	idx.retireDir(other)
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("an unread retired directory was kept: %v", err)
	}

}
