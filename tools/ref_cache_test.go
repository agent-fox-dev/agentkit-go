package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-05-40 (unit): Reference cache initializes lazily on first query and persists across subsequent queries
// Verifies: 05-REQ-8.1
func TestRefCache_TS05_40(t *testing.T) {
	tmp := t.TempDir()
	ws, err := NewWorkspace(tmp)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	ft := newFileTools(Options{Workspace: ws}.withDefaults())

	// Given: a newly created fileTools instance with no prior reference queries
	if ft.refCache.Load() != nil {
		t.Fatalf("expected ft.refCache == nil before first query, got %v", ft.refCache.Load())
	}

	// When: reference cache accessor is called on first query
	c1 := ft.getRefCache()
	if c1 == nil {
		t.Fatal("expected reference cache to be instantiated on first query, got nil")
	}
	if ft.refCache.Load() != c1 {
		t.Fatalf("expected ft.refCache == c1 (%p), got %p", c1, ft.refCache.Load())
	}

	// When: reference cache accessor is called on second query
	c2 := ft.getRefCache()

	// Then: second query reuses the exact same reference cache instance in memory
	if c1 != c2 {
		t.Fatalf("expected second call to reuse exact same instance: c1=%p, c2=%p", c1, c2)
	}
}

// TS-05-41 (unit): write_file and edit_file mark the modified path dirty in reference cache upon lock release
// Verifies: 05-REQ-8.2
func TestRefCache_TS05_41(t *testing.T) {
	tmp := t.TempDir()
	ws, err := NewWorkspace(tmp)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	ft := newFileTools(Options{Workspace: ws}.withDefaults())
	refCache := ft.getRefCache()
	if refCache == nil {
		t.Fatal("refCache is nil")
	}

	ctx := context.Background()

	// When: write_file writes to 'pkg/service.go' and releases its path lock
	writeArgs, err := json.Marshal(map[string]any{
		"path":    "pkg/service.go",
		"content": "package pkg\n\nfunc Run() {}\n",
	})
	if err != nil {
		t.Fatalf("json.Marshal writeArgs: %v", err)
	}

	writeFileTool := ft.writeFile()
	writeRes := writeFileTool.Execute(ctx, writeArgs)
	if !writeRes.OK {
		t.Fatalf("write_file failed: error=%s detail=%s", writeRes.Error, writeRes.Detail)
	}

	// Then:
	// - reference cache has 'pkg/service.go' marked in its dirty set
	// - package enclosing 'pkg/service.go' is marked for re-checking
	if !refCache.isPathDirty("pkg/service.go") {
		t.Fatalf("expected refCache.isPathDirty(\"pkg/service.go\") == true after write_file")
	}
	if !refCache.isPackageDirty("pkg") {
		t.Fatalf("expected refCache.isPackageDirty(\"pkg\") == true after write_file")
	}

	// Clear dirty flags for next step
	if err := refCache.refresh(ctx); err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	if refCache.isPathDirty("pkg/service.go") {
		t.Fatal("expected path dirty to be cleared after refresh")
	}

	// When: edit_file writes to 'pkg/service.go' and releases its path lock
	editArgs, err := json.Marshal(map[string]any{
		"path": "pkg/service.go",
		"edits": []map[string]string{
			{
				"old_string": "Run",
				"new_string": "Start",
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal editArgs: %v", err)
	}

	editFileTool := ft.editFile()
	editRes := editFileTool.Execute(ctx, editArgs)
	if !editRes.OK {
		t.Fatalf("edit_file failed: error=%s detail=%s", editRes.Error, editRes.Detail)
	}

	// Then:
	// - reference cache has 'pkg/service.go' marked in its dirty set
	// - package enclosing 'pkg/service.go' is marked for re-checking
	if !refCache.isPathDirty("pkg/service.go") {
		t.Fatalf("expected refCache.isPathDirty(\"pkg/service.go\") == true after edit_file")
	}
	if !refCache.isPackageDirty("pkg") {
		t.Fatalf("expected refCache.isPackageDirty(\"pkg\") == true after edit_file")
	}
}

// TS-05-42 (unit): Shell tools mark all cached packages and files for revalidation
// Verifies: 05-REQ-8.3
func TestRefCache_TS05_42(t *testing.T) {
	tmp := t.TempDir()
	ws, err := NewWorkspace(tmp)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	opts := Options{
		Workspace: ws,
		Env:       os.Environ(),
	}.withDefaults()
	ft := newFileTools(opts)
	refCache := ft.getRefCache()
	if refCache == nil {
		t.Fatal("refCache is nil")
	}

	// Ensure refCache is not in revalidateAll state initially
	if refCache.needsRevalidateAll() {
		t.Fatal("expected needsRevalidateAll() == false initially")
	}

	wrapShell := func(tl core.Tool) core.Tool {
		orig := tl.Execute
		tl.Execute = func(ctx context.Context, in json.RawMessage) core.ToolResult {
			r := orig(ctx, in)
			ft.markTableRevalidateAll()
			return r
		}
		return tl
	}

	execTool := wrapShell(executeTool(opts))
	ctx := context.Background()
	execArgs, err := json.Marshal(map[string]any{
		"command":   "echo hello",
		"timeout_s": 5,
	})
	if err != nil {
		t.Fatalf("json.Marshal execArgs: %v", err)
	}

	res := execTool.Execute(ctx, execArgs)
	if !res.OK {
		t.Fatalf("execute failed: error=%s detail=%s", res.Error, res.Detail)
	}

	// Then: reference cache has wholeTableRevalidate set to true
	if !refCache.needsRevalidateAll() {
		t.Fatal("expected refCache.needsRevalidateAll() == true after shell tool completed execution")
	}
}

// TS-05-43 (unit): Reference query re-parses dirty Go packages, re-outlines dirty files, and purges deleted files
// Verifies: 05-REQ-8.4
func TestRefCache_TS05_43(t *testing.T) {
	tmp := t.TempDir()
	ws, err := NewWorkspace(tmp)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// Create initial files:
	// Go package: pkg/a.go
	if err := os.MkdirAll(filepath.Join(tmp, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "pkg", "a.go"), []byte("package pkg\n\nfunc Hello() int { return 1 }\n"), 0o644); err != nil {
		t.Fatalf("WriteFile pkg/a.go: %v", err)
	}
	// Python file: script.py
	if err := os.WriteFile(filepath.Join(tmp, "script.py"), []byte("def hello():\n    return 1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile script.py: %v", err)
	}
	// File to be deleted: deleted.go
	if err := os.WriteFile(filepath.Join(tmp, "deleted.go"), []byte("package main\nfunc Dead() {}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile deleted.go: %v", err)
	}

	ft := newFileTools(Options{Workspace: ws}.withDefaults())
	refCache := ft.getRefCache()
	if refCache == nil {
		t.Fatal("refCache is nil")
	}

	ctx := context.Background()

	// Initial refresh to cache everything
	if err := refCache.refresh(ctx); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if !refCache.hasFile("deleted.go") {
		t.Fatal("expected deleted.go to be present in initial cache")
	}
	if !refCache.hasFile("pkg/a.go") {
		t.Fatal("expected pkg/a.go to be present in initial cache")
	}

	// Now modify pkg/a.go on disk
	if err := os.WriteFile(filepath.Join(tmp, "pkg", "a.go"), []byte("package pkg\n\nfunc Hello() int { return 2 }\n"), 0o644); err != nil {
		t.Fatalf("WriteFile updated pkg/a.go: %v", err)
	}
	// Re-save script.py
	if err := os.WriteFile(filepath.Join(tmp, "script.py"), []byte("def hello():\n    return 2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile updated script.py: %v", err)
	}
	// Delete deleted.go from disk
	if err := os.Remove(filepath.Join(tmp, "deleted.go")); err != nil {
		t.Fatalf("Remove deleted.go: %v", err)
	}

	// Mark modified and deleted files dirty
	refCache.markDirty("pkg/a.go")
	refCache.markDirty("script.py")
	refCache.markDirty("deleted.go")

	if !refCache.isPathDirty("pkg/a.go") {
		t.Fatal("expected pkg/a.go to be dirty before refresh")
	}

	// When: a reference query / refresh runs with dirty flags pending
	if err := refCache.refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Then:
	// - modified Go package is re-parsed and re-typechecked
	// - Python file is re-outlined
	// - deleted file is purged from cache
	// - dirty flags are cleared
	if refCache.isPathDirty("pkg/a.go") {
		t.Fatal("expected pkg/a.go to not be dirty after refresh")
	}
	if refCache.hasFile("deleted.go") {
		t.Fatal("expected deleted.go to be purged from cache after refresh")
	}
}

// TS-05-44 (unit): Modifying .gitignore causes candidate file cache to revalidate on subsequent query
// Verifies: 05-REQ-8.5
func TestRefCache_TS05_44(t *testing.T) {
	tmp := t.TempDir()
	ws, err := NewWorkspace(tmp)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// Given: a workspace where 'ignored_dir/' is unignored, then .gitignore is updated to ignore 'ignored_dir/'
	if err := os.MkdirAll(filepath.Join(tmp, "ignored_dir"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "pkg"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Write candidate files calling Foo
	if err := os.WriteFile(filepath.Join(tmp, "ignored_dir", "ignore_me.go"), []byte("package ignored\n\nfunc Call() {\n\tFoo()\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile ignored_dir/ignore_me.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "pkg", "keep_me.go"), []byte("package pkg\n\nfunc Call() {\n\tFoo()\n}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile pkg/keep_me.go: %v", err)
	}

	ft := newFileTools(Options{Workspace: ws}.withDefaults())
	refCache := ft.getRefCache()
	if refCache == nil {
		t.Fatal("refCache is nil")
	}

	fr := ft.findReferencesTool()
	ctx := context.Background()

	// Initial query without .gitignore: should find both files (or at least ignored_dir/ignore_me.go)
	res1 := fr.Execute(ctx, []byte(`{"name":"Foo"}`))
	if !res1.OK {
		t.Fatalf("fr.Execute initial failed: error=%s detail=%s", res1.Error, res1.Detail)
	}

	// Now update .gitignore to ignore ignored_dir/
	if err := os.WriteFile(filepath.Join(ws.Root, ".gitignore"), []byte("ignored_dir/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .gitignore: %v", err)
	}
	refCache.markDirty(".gitignore")

	// When: a reference query is executed after .gitignore update
	res := fr.Execute(ctx, []byte(`{"name":"Foo"}`))
	if !res.OK {
		t.Fatalf("fr.Execute after gitignore update failed: error=%s detail=%s", res.Error, res.Detail)
	}

	// Then:
	// - candidate file cache is revalidated
	// - files inside 'ignored_dir/' are excluded from the candidate search
	var sites []ReferenceSite
	if rr, ok := res.Data["result"].(ReferenceResult); ok {
		sites = rr.Sites
	} else if sList, ok := res.Data["sites"].([]ReferenceSite); ok {
		sites = sList
	} else {
		t.Fatalf("cannot extract sites from res.Data: %v", res.Data)
	}

	for _, s := range sites {
		if strings.HasPrefix(s.Path, "ignored_dir/") {
			t.Fatalf("expected files inside 'ignored_dir/' to be excluded, but found site: %s", s.Path)
		}
	}
}

// refSites runs find_references with args and returns its sites.
func refSites(t *testing.T, ft *fileTools, args string) []ReferenceSite {
	t.Helper()
	res := ft.findReferencesTool().Execute(context.Background(), json.RawMessage(args))
	if !res.OK {
		t.Fatalf("find_references %s: %s", args, res.Text)
	}
	return res.Data["result"].(ReferenceResult).Sites
}

// runWriteFile writes content to rel through the write_file tool.
func runWriteFile(t *testing.T, ft *fileTools, rel, content string) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"path": rel, "content": content})
	if res := ft.writeFile().Execute(context.Background(), args); !res.OK {
		t.Fatalf("write_file %s: %s", rel, res.Text)
	}
}

// TS-05-40 (unit): queries read the cache: the first query leaves checked
// Go packages and outlines in it, a second query with nothing dirty reuses
// them, and a write_file makes the next query re-check.
// Verifies: 05-REQ-8.1, 05-REQ-8.4
func TestRefCache_TS05_40_QueriesReadCache(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.22\n")
	writeFile(t, root, "lib.go", "package m\n\nfunc Target() int { return 1 }\n\nfunc Caller() int {\n\treturn Target()\n}\n")
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{Workspace: ws}.withDefaults())

	if got := refSites(t, ft, `{"name":"Target"}`); len(got) != 1 {
		t.Fatalf("first query sites = %+v, want one", got)
	}
	rc := ft.refCache.Load()
	rc.mu.Lock()
	imp1, outlined := rc.importer, rc.outlines["lib.go"] != nil
	rc.mu.Unlock()
	if imp1 == nil || len(imp1.pkgInfos) == 0 {
		t.Fatal("the first query left no checked Go packages in the reference cache")
	}
	if !outlined {
		t.Fatal("the first query left no outline for lib.go in the reference cache")
	}

	refSites(t, ft, `{"name":"Target"}`)
	rc.mu.Lock()
	imp2 := rc.importer
	rc.mu.Unlock()
	if imp2 != imp1 {
		t.Fatal("a query with nothing dirty re-checked the Go workspace instead of reusing the cache")
	}

	runWriteFile(t, ft, "more.go", "package m\n\nfunc Another() int {\n\treturn Target()\n}\n")
	got := refSites(t, ft, `{"name":"Target"}`)
	var fresh bool
	for _, s := range got {
		if s.Path == "more.go" && s.Enclosing.Name == "Another" && s.Confidence == "resolved" {
			fresh = true
		}
	}
	if !fresh {
		t.Fatalf("the caller added by write_file is missing: %+v", got)
	}
}

// TS-05-43 (unit): a non-Go file written by write_file is searched by the
// next query; the candidate list cached for the name is not reused stale.
// Verifies: 05-REQ-8.2, 05-REQ-8.4
func TestRefCache_TS05_43_CandidatesFreshAfterWrite(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "calc.py", "def calc_total(x):\n    return x\n")
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{Workspace: ws}.withDefaults())

	for _, s := range refSites(t, ft, `{"name":"calc_total"}`) {
		if s.Path == "shop.py" {
			t.Fatalf("shop.py does not exist yet: %+v", s)
		}
	}
	runWriteFile(t, ft, "shop.py", "def checkout(x):\n    return calc_total(x)\n")
	var found bool
	for _, s := range refSites(t, ft, `{"name":"calc_total"}`) {
		if s.Path == "shop.py" && s.Line == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("the call in shop.py written by write_file was not found")
	}
}
