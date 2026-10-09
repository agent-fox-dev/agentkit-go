//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-69 (smoke): An embedder opts in and the model's first sym: search
// builds the index and returns ranked grouped text.
//
// Verifies: 03-PATH-1, 03-REQ-5.1, 03-REQ-4.3
//
// Real components: codesearch.New and index, tools.All, tools.Walk,
// outline.OutlineMany, zoekt builder and searcher, code_search tool, file system.
func TestSmokeFirstSymSearch_TS03_69(t *testing.T) {
	// Given: a fixture workspace with Go files and a nested .gitignore.
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n\nfunc main() {}\n")
	mkFile(t, root, "pkg/runner.go", "package pkg\n\n// Runner handles retry logic.\nfunc Runner() {\n\t// body\n}\n")
	mkFile(t, root, "pkg/helper.go", "package pkg\n\nfunc Helper() {}\n")
	mkFile(t, root, "pkg/sub/.gitignore", "*.log\n")
	mkFile(t, root, "pkg/sub/code.go", "package sub\n\nfunc SubFunc() {}\n")
	mkFile(t, root, "pkg/sub/debug.log", "should be ignored\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Create a real codesearch index.
	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Wire through tools.All with Options.Index.
	allTools, err := tools.All(tools.Options{
		Workspace: ws,
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{},
		Index:     idx,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Then: code_search is the last tool in the list.
	lastTool := allTools[len(allTools)-1]
	if lastTool.Name != "code_search" {
		t.Errorf("last tool = %q, want code_search", lastTool.Name)
	}

	// Verify the built-in tools precede code_search.
	builtinNames := []string{
		"read_file", "write_file", "edit_file",
		"list_files", "find_files", "search_files",
		"file_outline", "find_symbol", "find_references",
		"execute", "run_command",
	}
	for i, name := range builtinNames {
		if i >= len(allTools) || allTools[i].Name != name {
			t.Errorf("tool %d = %q, want %q", i, allTools[i].Name, name)
		}
	}

	// When: code_search is called with query `sym:Runner` and a path.
	var csTool core.Tool
	for _, tl := range allTools {
		if tl.Name == "code_search" {
			csTool = tl
			break
		}
	}

	ctx := context.Background()

	// Verify build count is 0 before the call.
	if bc := idx.BuildCount(); bc != 0 {
		t.Fatalf("build count before code_search = %d, want 0", bc)
	}

	in, _ := json.Marshal(map[string]any{
		"query": "sym:Runner",
		"path":  "pkg",
	})
	r := csTool.Execute(ctx, in)

	// Then: the result is OK.
	if !r.OK {
		t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
	}

	// The index was built (build count = 1).
	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count after code_search = %d, want 1", bc)
	}

	// The result text is file-grouped ranked text.
	if r.Text == "" {
		t.Fatal("result text is empty")
	}

	// The first line contains index metadata.
	lines := strings.Split(r.Text, "\n")
	if len(lines) == 0 {
		t.Fatal("result has no lines")
	}
	firstLine := lines[0]
	if !strings.Contains(firstLine, "files indexed") {
		t.Errorf("first line should contain 'files indexed', got: %s", firstLine)
	}

	// Data fields are populated.
	if r.Data == nil {
		t.Fatal("Data is nil")
	}
	if _, ok := r.Data["files"]; !ok {
		t.Error("Data missing 'files' key")
	}
	if _, ok := r.Data["files_indexed"]; !ok {
		t.Error("Data missing 'files_indexed' key")
	}
	if _, ok := r.Data["symbol_sources"]; !ok {
		t.Error("Data missing 'symbol_sources' key")
	}

	// The result should contain runner.go (the sym: hit).
	paths := getResultFilePaths(r)
	foundRunner := false
	for _, p := range paths {
		if p == "pkg/runner.go" {
			foundRunner = true
		}
	}
	if !foundRunner {
		t.Errorf("sym:Runner should find pkg/runner.go, got paths: %v", paths)
	}

	// The path constraint should confine results to pkg/.
	for _, p := range paths {
		if !strings.HasPrefix(p, "pkg/") {
			t.Errorf("path=pkg should confine results to pkg/, got %q", p)
		}
	}

	// The ignored .log file should not be indexed.
	indexed := idx.IndexedFiles()
	if indexed["pkg/sub/debug.log"] {
		t.Error("debug.log should be excluded by .gitignore")
	}

	// Shard run directory should exist on disk.
	runDir := idx.RunDir()
	if runDir == "" {
		t.Fatal("run directory should exist after build")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run directory should exist on disk: %v", err)
	}
}

// TS-03-70 (smoke): The model edits a line with edit_file and immediately
// finds the new line, not the old one.
//
// Verifies: 03-PATH-2, 03-REQ-6.1, 03-REQ-6.4
//
// Real components: edit_file tool, codesearch index, overlay shard, zoekt
// builder and searcher, code_search tool, file system.
func TestSmokeEditFileFreshness_TS03_70(t *testing.T) {
	// Given: a built real index over a fixture workspace.
	// We need enough files so that 1 dirty file is below the 5% rebuild
	// threshold (1/N < 0.05 means N > 20).
	root := t.TempDir()
	mkFile(t, root, "target.go", "package pkg\n\n// oldUniqueMarker is the original content.\nfunc Target() {}\n")
	for i := 0; i < 25; i++ {
		mkFile(t, root, fmt.Sprintf("filler%02d.go", i),
			fmt.Sprintf("package pkg\n\nfunc Filler%02d() {}\n", i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Wire real tools through tools.All.
	allTools, err := tools.All(tools.Options{
		Workspace: ws,
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{},
		Index:     idx,
	})
	if err != nil {
		t.Fatal(err)
	}

	toolMap := make(map[string]core.Tool)
	for _, tl := range allTools {
		toolMap[tl.Name] = tl
	}

	csTool := toolMap["code_search"]
	editTool := toolMap["edit_file"]
	ctx := context.Background()

	// Build the index by searching for the old marker.
	in, _ := json.Marshal(map[string]any{"query": "oldUniqueMarker"})
	r := csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial code_search failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "oldUniqueMarker") {
		t.Fatal("initial search should find oldUniqueMarker")
	}

	// When: edit_file changes the line.
	editArgs, _ := json.Marshal(map[string]any{
		"path": "target.go",
		"edits": []map[string]string{
			{"old_string": "oldUniqueMarker", "new_string": "newUniqueMarker"},
		},
	})
	editResult := editTool.Execute(ctx, editArgs)
	if !editResult.OK {
		t.Fatalf("edit_file failed: %s: %s", editResult.Error, editResult.Detail)
	}

	// Then: code_search finds the new line.
	in, _ = json.Marshal(map[string]any{"query": "newUniqueMarker"})
	r = csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for newUniqueMarker failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "newUniqueMarker") {
		t.Error("after edit, code_search should find newUniqueMarker")
	}

	// And: code_search does NOT find the old line.
	in, _ = json.Marshal(map[string]any{"query": "oldUniqueMarker"})
	r = csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for oldUniqueMarker failed: %s: %s", r.Error, r.Detail)
	}
	if strings.Contains(r.Text, "oldUniqueMarker") {
		t.Error("after edit, code_search should NOT find oldUniqueMarker")
	}

	// And: Data.dirty_files is 1.
	in, _ = json.Marshal(map[string]any{"query": "newUniqueMarker"})
	r = csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for dirty_files check failed: %s: %s", r.Error, r.Detail)
	}
	if r.Data != nil {
		df, ok := r.Data["dirty_files"]
		if !ok {
			t.Error("Data missing 'dirty_files' key")
		} else {
			switch v := df.(type) {
			case int:
				if v != 1 {
					t.Errorf("dirty_files = %d, want 1", v)
				}
			case float64:
				if int(v) != 1 {
					t.Errorf("dirty_files = %v, want 1", v)
				}
			}
		}
	}
}

// TS-03-71 (smoke): A shell command deletes a file and the next search omits it.
//
// Verifies: 03-PATH-3, 03-REQ-6.2, 03-REQ-2.7
//
// Real components: execute tool, codesearch index, tools.Walk, zoekt searcher,
// code_search tool, file system.
func TestSmokeShellDeleteFreshness_TS03_71(t *testing.T) {
	// Given: a built real index over a fixture workspace.
	root := t.TempDir()
	mkFile(t, root, "keep.go", "package pkg\n\n// keepMarker stays.\nfunc Keep() {}\n")
	mkFile(t, root, "victim.go", "package pkg\n\n// victimOnlyToken is unique to this file.\nfunc Victim() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Wire real tools through tools.All.
	allTools, err := tools.All(tools.Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{},
		Index:     idx,
	})
	if err != nil {
		t.Fatal(err)
	}

	toolMap := make(map[string]core.Tool)
	for _, tl := range allTools {
		toolMap[tl.Name] = tl
	}

	csTool := toolMap["code_search"]
	execTool := toolMap["execute"]
	ctx := context.Background()

	// Build the index.
	in, _ := json.Marshal(map[string]any{"query": "victimOnlyToken"})
	r := csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial code_search failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "victimOnlyToken") {
		t.Fatal("initial search should find victimOnlyToken")
	}

	// When: execute deletes the file.
	execArgs, _ := json.Marshal(map[string]any{
		"command":   fmt.Sprintf("rm %s", filepath.Join(root, "victim.go")),
		"timeout_s": 5,
	})
	execResult := execTool.Execute(ctx, execArgs)
	// The execute tool should succeed (or at least return).
	_ = execResult

	// Verify the file is actually deleted.
	if _, err := os.Stat(filepath.Join(root, "victim.go")); !os.IsNotExist(err) {
		t.Fatal("victim.go should be deleted")
	}

	// Then: code_search for victimOnlyToken should not find it.
	in, _ = json.Marshal(map[string]any{"query": "victimOnlyToken"})
	r = csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search after delete failed: %s: %s", r.Error, r.Detail)
	}

	// The deleted file should not appear in results.
	paths := getResultFilePaths(r)
	for _, p := range paths {
		if p == "victim.go" {
			t.Error("deleted file victim.go should not appear in code_search results")
		}
	}

	// The kept file should still be findable.
	in, _ = json.Marshal(map[string]any{"query": "keepMarker"})
	r = csTool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for keepMarker failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "keepMarker") {
		t.Error("keepMarker should still be findable after deleting victim.go")
	}
}

// TS-03-72 (smoke): find_symbol is served by the index only after a
// code_search built it, with identical matches.
//
// Verifies: 03-PATH-4, 03-REQ-7.2, 03-REQ-7.3
//
// Real components: find_symbol tool, codesearch index, outline.OutlineMany,
// symbol table, code_search tool, file system.
func TestSmokeFindSymbolThroughIndex_TS03_72(t *testing.T) {
	// Given: a real fixture workspace.
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n\nfunc Main() {}\nfunc helper() {}\n")
	mkFile(t, root, "lib.go", "package main\n\nfunc Lib() {}\n")
	mkFile(t, root, "pkg/server.go", "package pkg\n\ntype Server struct{}\nfunc NewServer() *Server { return nil }\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Create a real codesearch index.
	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Build find_symbol WITH the index.
	withIdxAll, err := tools.All(tools.Options{
		Workspace: ws,
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{},
		Index:     idx,
	})
	if err != nil {
		t.Fatal(err)
	}

	var findWithIdx, csToolWithIdx core.Tool
	for _, tl := range withIdxAll {
		if tl.Name == "find_symbol" {
			findWithIdx = tl
		}
		if tl.Name == "code_search" {
			csToolWithIdx = tl
		}
	}

	// Build find_symbol WITHOUT the index.
	noIdxAll, err := tools.All(tools.Options{
		Workspace: ws,
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}

	var findNoIdx core.Tool
	for _, tl := range noIdxAll {
		if tl.Name == "find_symbol" {
			findNoIdx = tl
		}
	}

	ctx := context.Background()

	// When: find_symbol is called BEFORE any code_search.
	findArgs, _ := json.Marshal(map[string]any{"name": "Main"})
	r1 := findWithIdx.Execute(ctx, findArgs)
	if !r1.OK {
		t.Fatalf("first find_symbol failed: %s: %s", r1.Error, r1.Detail)
	}

	// Then: the first find_symbol is answered from its own table (not the index).
	// It should NOT mention "codesearch" in the header.
	if strings.Contains(r1.Text, "codesearch") {
		t.Error("first find_symbol (before code_search) should NOT mention codesearch")
	}

	// When: code_search is called to build the index.
	csArgs, _ := json.Marshal(map[string]any{"query": "Main"})
	csResult := csToolWithIdx.Execute(ctx, csArgs)
	if !csResult.OK {
		t.Fatalf("code_search failed: %s: %s", csResult.Error, csResult.Detail)
	}

	// When: find_symbol is called again with the same arguments.
	r2 := findWithIdx.Execute(ctx, findArgs)
	if !r2.OK {
		t.Fatalf("second find_symbol failed: %s: %s", r2.Error, r2.Detail)
	}

	// Then: the second find_symbol is answered from the index.
	if !strings.Contains(r2.Text, "codesearch") {
		t.Error("second find_symbol (after code_search) should mention codesearch in header")
	}

	// Then: both calls return the same matches in the same order.
	// Compare with the no-index find_symbol.
	rNoIdx := findNoIdx.Execute(ctx, findArgs)
	if !rNoIdx.OK {
		t.Fatalf("no-index find_symbol failed: %s: %s", rNoIdx.Error, rNoIdx.Detail)
	}

	idxMatches := extractSymbolMatches(r2)
	noIdxMatches := extractSymbolMatches(rNoIdx)

	if len(idxMatches) != len(noIdxMatches) {
		t.Errorf("match count differs: withIdx=%d noIdx=%d", len(idxMatches), len(noIdxMatches))
	} else {
		for i := range idxMatches {
			if idxMatches[i] != noIdxMatches[i] {
				t.Errorf("match %d differs:\n  withIdx: %+v\n  noIdx:   %+v",
					i, idxMatches[i], noIdxMatches[i])
			}
		}
	}
}

// TS-03-73 (smoke): A very large tree stops at a bound, answers partially
// and is not rebuilt on the next call.
//
// Verifies: 03-PATH-5, 03-REQ-5.7
//
// Real components: codesearch index, tools.Walk, zoekt builder and searcher,
// code_search tool, file system.
func TestSmokePartialBound_TS03_73(t *testing.T) {
	// Given: a fixture workspace larger than a scaled-down MaxFiles bound.
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// searchable content %d\nfunc F%d() {}\n", i, i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Create a real index with a very low MaxFiles bound.
	idx, err := newIndex(ws, Options{
		TempDir:  t.TempDir(),
		Ignore:   tools.NoGlobalExcludes(),
		MaxFiles: 5, // only 5 files allowed
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// When: code_search is called the first time.
	in, _ := json.Marshal(map[string]any{"query": "searchable"})
	r := tool.Execute(ctx, in)

	// Then: the result has partial=true with a partial_reason.
	if !r.OK {
		t.Fatalf("first code_search failed: %s: %s", r.Error, r.Detail)
	}
	assertPartial(t, r, "files")

	// Build counter is 1.
	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count after first call = %d, want 1", bc)
	}

	// When: code_search is called a second time.
	r2 := tool.Execute(ctx, in)
	if !r2.OK {
		t.Fatalf("second code_search failed: %s: %s", r2.Error, r2.Detail)
	}

	// Then: still partial, same reason.
	assertPartial(t, r2, "files")

	// Build counter stays 1 (no rebuild).
	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count after second call = %d, want 1", bc)
	}

	// The note should tell the model to narrow path or use search_files.
	note, _ := r.Data["note"].(string)
	if note == "" {
		note = r.Text
	}
	if !strings.Contains(note, "search_files") && !strings.Contains(note, "narrow") {
		t.Errorf("note should mention search_files or narrow, got: %q", note)
	}
}

// TS-03-74 (smoke): The embedder closes the index, its shards vanish and a
// later code_search returns index_closed.
//
// Verifies: 03-PATH-6, 03-REQ-8.3, 03-REQ-8.4
//
// Real components: codesearch index, zoekt searcher, code_search tool, file system.
func TestSmokeCloseAndIndexClosed_TS03_74(t *testing.T) {
	// Given: a real built index whose run directory exists on disk.
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n\n// searchword\nfunc main() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Build the index.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "searchword"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("build failed: %s: %s", r.Error, r.Detail)
	}

	runDir := idx.RunDir()
	if runDir == "" {
		t.Fatal("run directory should exist after build")
	}
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run directory should exist on disk: %v", err)
	}

	// When: Close is called.
	if err := idx.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	// Then: the run directory is deleted.
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("run directory should be deleted after Close, err=%v", err)
	}

	// Then: code_search returns index_closed.
	r = tool.Execute(context.Background(), in)
	if r.OK {
		t.Error("expected error after Close")
	}
	if r.Error != "index_closed" {
		t.Errorf("error code = %q, want index_closed", r.Error)
	}

	// No build or search happened after Close.
	// (Build count should still be 1 from before Close.)
	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count after Close = %d, want 1", bc)
	}

	// Close is idempotent.
	if err := idx.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
}
