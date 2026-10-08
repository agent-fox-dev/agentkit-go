package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// racyFixture builds the symbol table over one file and returns the tools,
// the file's path and a function that reads the entry's indexedAt.
func racyFixture(t *testing.T, content string) (ft *fileTools, root string, indexedAt func() time.Time) {
	t.Helper()
	root = t.TempDir()
	mkSymFile(t, root, "racy.go", content)
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft = newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	}.withDefaults())
	r := ft.findSymbolTool().Execute(context.Background(), json.RawMessage(`{"name":"func"}`))
	if !r.OK {
		t.Fatalf("initial build failed: error=%s detail=%s", r.Error, r.Detail)
	}
	indexedAt = func() time.Time {
		ft.table.lockBlocking()
		defer ft.table.unlock()
		return ft.table.entries["racy.go"].indexedAt
	}
	return ft, root, indexedAt
}

// setRecord rewrites the entry's record so the file looks as it does when it
// was written mtimeAgo before it was indexed and indexed indexedAgo ago. The
// file's own mtime is set to match, as the table sees it through Stat.
func setRecord(t *testing.T, ft *fileTools, path string, mtimeBeforeIndex, indexedAgo time.Duration) {
	t.Helper()
	indexed := time.Now().Add(-indexedAgo)
	mtime := indexed.Add(-mtimeBeforeIndex)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	ft.table.lockBlocking()
	e := ft.table.entries["racy.go"]
	e.indexedAt = indexed
	e.mtime = fi.ModTime()
	e.size = fi.Size()
	ft.table.unlock()
}

func symbolFound(t *testing.T, ft *fileTools, name string) bool {
	t.Helper()
	r := ft.findSymbolTool().Execute(context.Background(), json.RawMessage(`{"name":"`+name+`","exact":true}`))
	if !r.OK {
		t.Fatalf("find_symbol %s: error=%s detail=%s", name, r.Error, r.Detail)
	}
	return len(extractSymbolNames(t, r)) == 1
}

// 02-REQ-6.6 and Design Decision 12: a file whose mtime lies within 2 s of the
// moment it was indexed is always re-outlined on revalidation, whenever that
// revalidation happens. The case: the file is written at T=0 and indexed at
// T=0.5, a same-size edit in the same timestamp granule leaves (size, mtime)
// as they were, and find_symbol runs at T=5.
func TestRacyEditIsSeenByARevalidationMoreThanTwoSecondsLater(t *testing.T) {
	old := "package main\n\nfunc RacyOld() {}\n"
	edited := "package main\n\nfunc RacyNew() {}\n"
	if len(old) != len(edited) {
		t.Fatal("fixture: the edit must keep the size")
	}
	ft, root, indexedAt := racyFixture(t, old)
	path := filepath.Join(root, "racy.go")

	// Indexed 5 s ago; the file's mtime is 0.5 s before that.
	setRecord(t, ft, path, 500*time.Millisecond, 5*time.Second)
	recorded := indexedAt()

	// The same-size edit, with the mtime put back to what it was.
	fi, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, fi.ModTime(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Stat(path); now.Size() != fi.Size() || !now.ModTime().Equal(fi.ModTime()) {
		t.Fatalf("fixture: (size, mtime) moved: (%d, %v) vs (%d, %v)", now.Size(), now.ModTime(), fi.Size(), fi.ModTime())
	}

	ft.table.markRevalidateAll()
	if !symbolFound(t, ft, "RacyNew") {
		t.Error("RacyNew not found: the same-size edit was missed by a revalidation more than 2 s after indexing")
	}
	if symbolFound(t, ft, "RacyOld") {
		t.Error("RacyOld is still served: stale declarations")
	}
	if !indexedAt().After(recorded) {
		t.Error("the racy file was not re-outlined")
	}
}

// A re-outline makes the entry's indexedAt "now", so once the revalidation is
// 2 s past the file's mtime the entry stops being racy: it is re-outlined once,
// not on every revalidation.
func TestRacyEntryStopsBeingRacyOnceReoutlined(t *testing.T) {
	ft, root, indexedAt := racyFixture(t, "package main\n\nfunc Racy() {}\n")
	path := filepath.Join(root, "racy.go")
	setRecord(t, ft, path, 500*time.Millisecond, 5*time.Second)

	ft.table.markRevalidateAll()
	_ = symbolFound(t, ft, "Racy")
	first := indexedAt()
	if time.Since(first) > time.Second {
		t.Fatalf("the racy file was not re-outlined: indexedAt is %v old", time.Since(first))
	}

	// The file's mtime is 5.5 s before the new indexedAt: not racy.
	time.Sleep(20 * time.Millisecond)
	ft.table.markRevalidateAll()
	_ = symbolFound(t, ft, "Racy")
	if got := indexedAt(); !got.Equal(first) {
		t.Errorf("the file was re-outlined again (indexedAt %v -> %v) although its mtime is far from the index time", first, got)
	}
}

// A file whose mtime is long before it was indexed is not racy, however
// recently it was indexed: revalidation does not re-outline it.
func TestFileWithAnOldMtimeIsNotReoutlined(t *testing.T) {
	ft, root, indexedAt := racyFixture(t, "package main\n\nfunc Racy() {}\n")
	path := filepath.Join(root, "racy.go")
	// Indexed 1 s ago, written an hour before that.
	setRecord(t, ft, path, time.Hour, time.Second)
	before := indexedAt()

	ft.table.markRevalidateAll()
	_ = symbolFound(t, ft, "Racy")
	if got := indexedAt(); !got.Equal(before) {
		t.Errorf("a file with an old mtime was re-outlined (indexedAt %v -> %v)", before, got)
	}
}
