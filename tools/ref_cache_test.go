package tools

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestTheReferenceCacheIsCreatedOnFirstUseAndShared(t *testing.T) {
	ft := newFT(newWS(t, nil), Options{})
	if ft.refCache.Load() != nil {
		t.Fatal("the cache exists before the first query")
	}
	if c := ft.getRefCache(); c == nil || ft.getRefCache() != c {
		t.Fatal("the cache is not created once and reused")
	}
}

func TestTheReferenceCacheTracksDirtyPathsAndPackages(t *testing.T) {
	ws := newWS(t, map[string]string{"pkg/a.go": "package pkg\n\nfunc Hello() int { return 1 }\n", "script.py": "def hello():\n    return 1\n", "deleted.go": "package main\n"})
	ft := newFT(ws, Options{})
	rc := ft.getRefCache()
	ctx := context.Background()
	if err := rc.refresh(ctx); err != nil || !rc.hasFile("deleted.go") || !rc.hasFile("pkg/a.go") || rc.needsRevalidateAll() {
		t.Fatalf("the first refresh lists the files: %v", err)
	}
	mustOK(t, call(ft.writeFile(), `{"path":"pkg/service.go","content":"package pkg\n\nfunc Run() {}\n"}`))
	if !rc.isPathDirty("pkg/service.go") || !rc.isPackageDirty("pkg") || !rc.isPackageDirty("./pkg/service.go") || rc.isPackageDirty("other") {
		t.Fatal("write_file did not mark the path and its Go package")
	}
	rc.markDirty("main.go")
	if !rc.isPackageDirty(".") || !rc.isPackageDirty("") {
		t.Fatal("a file at the root marks the root package")
	}
	_ = os.Remove(abs(ws, "deleted.go"))
	if err := rc.refresh(ctx); err != nil || rc.isPathDirty("pkg/service.go") || rc.isPackageDirty("pkg") || rc.hasFile("deleted.go") || !rc.hasFile("pkg/service.go") {
		t.Fatalf("the refresh clears the marks and purges a deleted file: %v", err)
	}
	ft.markTableRevalidateAll() // what a shell command does
	if !rc.needsRevalidateAll() || !rc.isPackageDirty("anything") {
		t.Fatal("a shell command marks the whole cache")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := rc.refresh(cancelled); err == nil {
		t.Fatal("a cancelled refresh")
	}
}

// TestQueriesReuseTheCacheUntilSomethingChanges counts the work behind
// find_references: the checked Go packages and outlines are kept while
// nothing changes, and every kind of change is seen by the next query.
func TestQueriesReuseTheCacheUntilSomethingChanges(t *testing.T) {
	ws := newWS(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n", "lib.go": "package m\n\nfunc Target() int { return 1 }\n\nfunc Caller() int {\n\treturn Target()\n}\n",
		"calc.py": "def calc_total(x):\n    return x\n", "ignored_dir/x.go": "package ignored\n\nfunc Call() { Foo() }\n", "pkg/keep.go": "package pkg\n\nfunc Call() { Foo() }\n",
	})
	ft := newFT(ws, Options{})
	sitesOf := func(name string) []string { return siteList(refResult(t, ft, `{"name":"`+name+`"}`)) }
	if got := sitesOf("Target"); !slices.Equal(got, []string{"lib.go:6:9 resolved"}) {
		t.Fatalf("the first query: %v", got)
	}
	rc := ft.refCache.Load()
	rc.mu.Lock()
	imp, outlined := rc.importer, rc.outlines["lib.go"] != nil
	rc.mu.Unlock()
	if imp == nil || !outlined {
		t.Fatal("the first query cached neither the checked packages nor the outline")
	}
	sitesOf("Target")
	if rc.importer != imp {
		t.Fatal("a query with nothing dirty re-checked the workspace")
	}
	// A new caller written by write_file; a Python caller in a new file.
	mustOK(t, call(ft.writeFile(), `{"path":"more.go","content":"package m\n\nfunc Another() int {\n\treturn Target()\n}\n"}`))
	mustOK(t, call(ft.writeFile(), `{"path":"shop.py","content":"def checkout(x):\n    return calc_total(x)\n"}`))
	if got := refResult(t, ft, `{"name":"Target"}`); len(got.Sites) != 2 || got.Sites[1].Enclosing.Name != "Another" {
		t.Fatalf("after write_file: %v", siteList(got))
	}
	if got := sitesOf("calc_total"); !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "shop.py:2:") }) {
		t.Fatalf("the new Python caller: %v", got)
	}
	// An edited outline is outlined again.
	mustOK(t, call(ft.editFile(), `{"path":"lib.go","edits":[{"old_string":"func Caller() int {","new_string":"// moved\nfunc Caller() int {"}]}`))
	if got := refResult(t, ft, `{"name":"Target"}`); got.Sites[0].Line != 7 || got.Sites[0].Enclosing.StartLine != 6 {
		t.Fatalf("after edit_file: %+v", got.Sites[0])
	}
	// A new .gitignore re-filters the candidates and the Go packages.
	if got := sitesOf("Foo"); len(got) != 2 {
		t.Fatalf("before the .gitignore: %v", got)
	}
	writeAt(t, ws.Root(), ".gitignore", "ignored_dir/\n")
	rc.markDirty(".gitignore")
	if got := sitesOf("Foo"); len(got) != 1 || !strings.HasPrefix(got[0], "pkg/keep.go") {
		t.Fatalf("after the .gitignore: %v", got)
	}
	// A shell command can change anything: a file it deleted is gone.
	_ = os.Remove(abs(ws, "more.go"))
	ft.markTableRevalidateAll()
	if got := sitesOf("Target"); len(got) != 1 {
		t.Fatalf("after a deletion: %v", got)
	}
}
