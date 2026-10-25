//go:build !windows

package codesearch

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Dirty files are searched through an overlay shard and merged by score, so
// a fresh edit is not pushed off the page by indexed hits; the overlay is
// built once per dirty set.
func TestAFreshEditSurvivesTheMaxFilesCap(t *testing.T) {
	files := with(filler(20), numbered(30, "hot%02d.go", "package pkg\n// HOTWORD HOTWORD HOTWORD %d\nfunc Hot%d() { /* HOTWORD */ }\n"))
	idx := newFixture(t, with(files, map[string]string{"edited.go": "package pkg\n// nothing here\nfunc Edited() {}\n"}), Options{})
	find(t, idx, "HOTWORD")

	write(t, idx, "edited.go", "package pkg\n// HOTWORD appears here\nfunc Edited() {}\n")
	idx.Invalidate("edited.go")
	r := find(t, idx, "HOTWORD", "max_files", 10)
	if got := paths(r); len(got) != 10 || !slices.Contains(got, "edited.go") {
		t.Errorf("results %v, want ten files including edited.go", got)
	}
	if r.Data["dirty_files"] != 1 || !strings.Contains(r.Text, "1 dirty files overlaid") {
		t.Errorf("dirty_files = %v, first line %q", r.Data["dirty_files"], strings.SplitN(r.Text, "\n", 2)[0])
	}
	var scores []float64
	for _, f := range r.Data["files"].([]any) {
		scores = append(scores, f.(map[string]any)["score"].(float64))
	}
	if !slices.IsSortedFunc(scores, func(a, b float64) int { return cmp.Compare(b, a) }) {
		t.Errorf("scores %v are not in descending order", scores)
	}

	find(t, idx, "HOTWORD", "max_files", 10)
	if n := idx.OverlayBuildCount(); n != 1 {
		t.Errorf("OverlayBuildCount = %d after a repeat query, want 1", n)
	}
	idx.Invalidate("hot00.go")
	find(t, idx, "HOTWORD")
	if n := idx.OverlayBuildCount(); n != 2 {
		t.Errorf("OverlayBuildCount = %d after the dirty set changed, want 2", n)
	}
}

func TestConcurrentQueriesOverOneDirtySetBuildOneOverlay(t *testing.T) {
	idx := newFixture(t, numbered(40, "f%02d.go", "package p\n// word %d\n"), Options{})
	find(t, idx, "word")
	write(t, idx, "f00.go", "package p\n// word fresh\n")
	idx.Invalidate("f00.go")

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if got := paths(find(t, idx, "fresh")); !slices.Equal(got, []string{"f00.go"}) {
				t.Errorf("fresh found in %v", got)
			}
		})
	}
	wg.Wait()
	if n := idx.OverlayBuildCount(); n != 1 {
		t.Fatalf("%d overlay builds for one dirty set, want 1", n)
	}
}

// The overlay lives under the run directory, so the sweep of a dead
// process's run directory, and a rebuild's removal of it, take it along.
func TestOverlayShardsLiveUnderTheRunDirectory(t *testing.T) {
	idx := newFixture(t, with(numbered(30, "f%02d.go", "package p\n"), map[string]string{"a.go": "package p\n// needle one\n"}), Options{})
	find(t, idx, "needle")
	write(t, idx, "a.go", "package p\n// needle two\n")
	idx.Invalidate("a.go")
	if r := find(t, idx, "needle"); !strings.Contains(r.Text, "needle two") {
		t.Fatalf("the edit is not found:\n%s", r.Text)
	}
	idx.mu.RLock()
	ov, runDir := idx.overlay, idx.runDir
	idx.mu.RUnlock()
	if ov == nil || ov.dir == "" {
		t.Fatal("no overlay shard was built")
	}
	if rel, err := filepath.Rel(runDir, ov.dir); err != nil || strings.HasPrefix(rel, "..") {
		t.Errorf("overlay %s is outside the run directory %s", ov.dir, runDir)
	}
}

// An edited file the build would not index is not searched from the overlay
// either, and its stale indexed content is gone too.
func TestAnEditedFileTheBuildWouldSkipIsNotSearched(t *testing.T) {
	for _, c := range []struct {
		name   string
		change func(t *testing.T, idx *Index)
	}{
		{"deleted", func(t *testing.T, idx *Index) { remove(t, idx, "e.go") }},
		{"grown past 1 MiB", func(t *testing.T, idx *Index) {
			write(t, idx, "e.go", strings.Repeat("needle\n", maxFileSize/7+1))
		}},
		{"binary now", func(t *testing.T, idx *Index) { write(t, idx, "e.go", "needle\x00\n") }},
		{"a directory now", func(t *testing.T, idx *Index) {
			remove(t, idx, "e.go")
			if err := os.Mkdir(filepath.Join(idx.ws.Root(), "e.go"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, with(filler(30), map[string]string{"e.go": "package p\n// needle\n"}), Options{})
			find(t, idx, "needle")
			c.change(t, idx)
			idx.Invalidate("e.go")
			if got := paths(find(t, idx, "needle")); len(got) != 0 {
				t.Errorf("needle found in %v", got)
			}
		})
	}
}

func TestMergeByScoreKeepsEveryOverlayHitWithinTheCap(t *testing.T) {
	f := func(path string, score float64) searchResultFile { return searchResultFile{path: path, score: score} }
	indexed := []searchResultFile{f("a", 5), f("b", 3), f("c", 1)}
	overlay := []searchResultFile{f("b", 4), f("d", 0.5)}
	for _, c := range []struct {
		max  int
		want []string
	}{
		{3, []string{"a", "b", "d"}}, // c gives way to the overlay's d
		{10, []string{"a", "b", "c", "d"}},
		{1, []string{"b"}},
	} {
		var got []string
		for _, m := range mergeByScore(slices.Clone(indexed), overlay, c.max) {
			got = append(got, m.path)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("max %d: merged %v, want %v", c.max, got, c.want)
		}
	}
}

// Dropping a dirty file from the indexed hits does not shrink the page or
// lose the cap marker while more files still match.
func TestADirtyTopResultKeepsThePageFull(t *testing.T) {
	idx := newFixture(t, numbered(30, "f%02d.go", "package p\n// needle\n"), Options{})
	top := paths(find(t, idx, "needle", "max_files", 10))[0]
	write(t, idx, top, "package p\n// no longer\n")
	idx.Invalidate(top)

	r := find(t, idx, "needle", "max_files", 10)
	if got := paths(r); len(got) != 10 || slices.Contains(got, top) || r.Data["truncated"] != true {
		t.Errorf("after editing the top result: %v, truncated %v; want ten other files and true", got, r.Data["truncated"])
	}
	if !strings.Contains(r.Text, "max_files") {
		t.Errorf("the cap marker is missing:\n%s", r.Text)
	}
}
