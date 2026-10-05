package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// refreshFixture builds a complete symbol table over a.go, b.go and c.go.
func refreshFixture(t *testing.T) (*fileTools, string) {
	t.Helper()
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc AFunc() {}\n")
	mkSymFile(t, root, "b.go", "package main\n\nfunc BFunc() {}\n")
	mkSymFile(t, root, "c.go", "package main\n\nfunc CFunc() {}\n")
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{DisableCtags: true},
	}.withDefaults())
	r := ft.findSymbolTool().Execute(context.Background(), json.RawMessage(`{"name":"Func"}`))
	if !r.OK {
		t.Fatalf("initial build failed: error=%s detail=%s", r.Error, r.Detail)
	}
	ft.table.lockBlocking()
	complete := ft.table.complete
	ft.table.unlock()
	if !complete {
		t.Fatal("the table should be complete after the initial build")
	}
	return ft, root
}

func countWalks(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	orig := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(string, fs.DirEntry) error) error {
		n.Add(1)
		return orig(ctx, ws, root, opts, fn)
	}
	t.Cleanup(func() { walkFn = orig })
	return &n
}

func findNames(t *testing.T, ft *fileTools, name string) []string {
	t.Helper()
	r := ft.findSymbolTool().Execute(context.Background(), json.RawMessage(`{"name":"`+name+`","exact":true}`))
	if !r.OK {
		t.Fatalf("find_symbol %s: error=%s detail=%s", name, r.Error, r.Detail)
	}
	return extractSymbolNames(t, r)
}

// 02-REQ-6.4 and 02-REQ-7.4: a dirty path that is still a regular file is
// re-outlined, never dropped. When the call context ends during the refresh,
// the paths not yet refreshed stay in the table and are marked dirty again,
// so the next call refreshes them.
func TestCancelledDirtyPathRefreshKeepsTheEntryAndTheMark(t *testing.T) {
	ft, root := refreshFixture(t)

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc AEdited() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ft.table.lockBlocking()
	ft.table.refreshDirtyPaths(ctx, ft, map[string]bool{"a.go": true})
	_, kept := ft.table.entries["a.go"]
	ft.table.unlock()

	if !kept {
		t.Error("a.go was dropped from the table by a cancelled refresh")
	}
	ft.table.markMu.Lock()
	remarked := ft.table.dirtyPaths["a.go"]
	ft.table.markMu.Unlock()
	if !remarked {
		t.Error("a.go is not marked dirty again after a cancelled refresh, so no later call would refresh it")
	}

	// The consequence the report describes: the next call must see the edit.
	if got := findNames(t, ft, "AEdited"); len(got) != 1 {
		t.Errorf("AEdited = %v after the cancelled refresh, want it found by the next call", got)
	}
	if got := findNames(t, ft, "AFunc"); len(got) != 0 {
		t.Errorf("AFunc = %v, want it gone: the old content is stale", got)
	}
}

// A refresh cancelled before it reached any path marks every one of them
// dirty again, and the next call refreshes them all.
func TestCancelledDirtyPathRefreshBeforeAnyPathRemarksEveryPath(t *testing.T) {
	ft, root := refreshFixture(t)
	for _, f := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("package main\n\nfunc "+strings.ToUpper(f[:1])+"New() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ft.table.lockBlocking()
	ft.table.refreshDirtyPaths(ctx, ft, map[string]bool{"a.go": true, "b.go": true, "c.go": true})
	ft.table.unlock()

	ft.table.markMu.Lock()
	got := len(ft.table.dirtyPaths)
	ft.table.markMu.Unlock()
	if got != 3 {
		t.Errorf("%d paths marked dirty after a refresh cancelled before any was refreshed, want 3", got)
	}

	for _, name := range []string{"ANew", "BNew", "CNew"} {
		if len(findNames(t, ft, name)) != 1 {
			t.Errorf("%s not found by the next call", name)
		}
	}
}

// A new file escalates a dirty-path refresh to a revalidation. That
// revalidation is the one the whole-table mark asks for, so it clears the
// mark: the next call answers from memory instead of walking again.
func TestEscalationFromANewFileDoesNotCostASecondRevalidation(t *testing.T) {
	ft, root := refreshFixture(t)
	walks := countWalks(t)

	mkSymFile(t, root, "new.go", "package main\n\nfunc NewFunc() {}\n")
	ft.table.markDirty("new.go")

	if got := findNames(t, ft, "NewFunc"); len(got) != 1 {
		t.Fatalf("NewFunc = %v, want it found after the escalated revalidation", got)
	}
	if n := walks.Load(); n != 1 {
		t.Fatalf("%d walks for the escalated call, want 1", n)
	}
	if ft.table.needsRefresh() {
		t.Error("the table still needs a refresh after the escalated revalidation completed")
	}

	if got := findNames(t, ft, "NewFunc"); len(got) != 1 {
		t.Fatalf("NewFunc = %v on the second call", got)
	}
	if n := walks.Load(); n != 1 {
		t.Errorf("%d walks after the second call, want still 1: nothing changed", n)
	}
}

// The same for a .gitignore edit.
func TestEscalationFromAGitignoreDoesNotCostASecondRevalidation(t *testing.T) {
	ft, root := refreshFixture(t)
	walks := countWalks(t)

	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("b.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ft.table.markDirty(".gitignore")

	if got := findNames(t, ft, "BFunc"); len(got) != 0 {
		t.Fatalf("BFunc = %v, want it gone: b.go is now ignored", got)
	}
	_ = findNames(t, ft, "AFunc")
	if n := walks.Load(); n != 1 {
		t.Errorf("%d walks over two calls, want 1", n)
	}
}

// A mark made after the escalation, while the revalidation runs or later, is
// still honoured: the pass clears only the marks that predate it.
func TestMarkAfterAnEscalationIsNotLost(t *testing.T) {
	ft, root := refreshFixture(t)

	mkSymFile(t, root, "new.go", "package main\n\nfunc NewFunc() {}\n")
	ft.table.markDirty("new.go")
	_ = findNames(t, ft, "NewFunc")

	ft.table.markRevalidateAll() // a shell command ran afterwards
	if !ft.table.needsRefresh() {
		t.Error("a whole-table mark made after the escalated pass was lost")
	}
}
