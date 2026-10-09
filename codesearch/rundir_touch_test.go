//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// builtIndex returns an index over a small workspace, already built by a first
// query, together with a function that runs another query on it.
func builtIndex(t *testing.T, tempDir string, ws *tools.Workspace) (*Index, func() core.ToolResult) {
	t.Helper()

	idx, err := newIndex(ws, Options{
		TempDir: tempDir,
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })

	tool := idx.Tools()[0]
	query := func() core.ToolResult {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		return tool.Execute(context.Background(), in)
	}
	if r := query(); !r.OK {
		t.Fatalf("first query: %s: %s", r.Error, r.Detail)
	}
	return idx, query
}

func touchWorkspace(t *testing.T) *tools.Workspace {
	t.Helper()
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n// searchword\nfunc A() {}\n")
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// ageRunDir makes idx look as it does when its run directory was last touched
// age ago: the directory's mtime and the index's own record of the touch.
func ageRunDir(t *testing.T, idx *Index, age time.Duration) {
	t.Helper()
	when := time.Now().Add(-age)
	if err := os.Chtimes(idx.RunDir(), when, when); err != nil {
		t.Fatal(err)
	}
	idx.mu.Lock()
	idx.lastTouch = when
	idx.mu.Unlock()
}

func runDirMtime(t *testing.T, idx *Index) time.Time {
	t.Helper()
	fi, err := os.Stat(idx.RunDir())
	if err != nil {
		t.Fatal(err)
	}
	return fi.ModTime()
}

// 03-REQ-5.6: a live index touches its own run directory while it is queried,
// so a sibling's 24-hour sweep leaves it alone.
func TestQueryTouchesTheRunDirAfterAnHour(t *testing.T) {
	idx, query := builtIndex(t, t.TempDir(), touchWorkspace(t))

	ageRunDir(t, idx, 25*time.Hour)
	if r := query(); !r.OK {
		t.Fatalf("query: %s: %s", r.Error, r.Detail)
	}
	if age := time.Since(runDirMtime(t, idx)); age > time.Minute {
		t.Errorf("run directory mtime is %v old after a query, want it touched", age)
	}
}

// 03-REQ-5.6: at most once an hour.
func TestQueryTouchesTheRunDirAtMostOncePerHour(t *testing.T) {
	idx, query := builtIndex(t, t.TempDir(), touchWorkspace(t))

	// Touched 30 minutes ago: a query inside the hour must not touch again.
	when := time.Now().Add(-30 * time.Minute)
	idx.mu.Lock()
	idx.lastTouch = when
	idx.mu.Unlock()
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(idx.RunDir(), old, old); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if r := query(); !r.OK {
			t.Fatalf("query: %s: %s", r.Error, r.Detail)
		}
	}
	if got := runDirMtime(t, idx); time.Since(got) < 24*time.Hour {
		t.Errorf("run directory mtime moved to %v within an hour of the last touch", got)
	}
}

// find_symbol's Index.Symbols is a query too.
func TestSymbolsTouchesTheRunDirAfterAnHour(t *testing.T) {
	idx, _ := builtIndex(t, t.TempDir(), touchWorkspace(t))

	ageRunDir(t, idx, 25*time.Hour)
	if _, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "A"}); err != nil || !ok {
		t.Fatalf("Symbols = ok:%v err:%v, want ok", ok, err)
	}
	if age := time.Since(runDirMtime(t, idx)); age > time.Minute {
		t.Errorf("run directory mtime is %v old after Symbols, want it touched", age)
	}
}

// The scenario of Design Decision 11: a second index on the same workspace
// builds after the first has lived for more than 24 hours. The first, which
// has been queried since, keeps its shards.
func TestSiblingBuildDoesNotSweepALiveIndex(t *testing.T) {
	ws := touchWorkspace(t)
	tempDir := t.TempDir()

	older, query := builtIndex(t, tempDir, ws)
	ageRunDir(t, older, 25*time.Hour)
	if r := query(); !r.OK {
		t.Fatalf("query on the older index: %s: %s", r.Error, r.Detail)
	}
	olderDir := older.RunDir()

	newer, _ := builtIndex(t, tempDir, ws) // its build sweeps the hash directory
	if newer.RunDir() == olderDir {
		t.Fatal("the two indexes share a run directory")
	}

	if _, err := os.Stat(olderDir); err != nil {
		t.Fatalf("the live index's run directory was swept: %v", err)
	}
	if r := query(); !r.OK {
		t.Errorf("query on the older index after the sibling's build: %s: %s", r.Error, r.Detail)
	}
}
