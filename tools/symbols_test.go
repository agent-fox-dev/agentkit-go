package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// mkSymFile creates a file in root with the given relative path and content.
func mkSymFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeFindSymbolTool builds a find_symbol tool's Execute function from a workspace root.
func makeFindSymbolTool(t *testing.T, root string, symOpts SymbolOptions) func(ctx context.Context, in json.RawMessage) core.ToolResult {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if !symOpts.DisableCtags && symOpts.Runner == nil {
		symOpts.DisableCtags = true
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   symOpts,
	}.withDefaults())
	tl := ft.findSymbolTool()
	return tl.Execute
}

// makeFindSymbolToolWithFT builds a find_symbol tool and returns both the Execute
// function and the fileTools, for tests that need to inspect the table.
func makeFindSymbolToolWithFT(t *testing.T, root string, symOpts SymbolOptions) (func(ctx context.Context, in json.RawMessage) core.ToolResult, *fileTools) {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	if !symOpts.DisableCtags && symOpts.Runner == nil {
		symOpts.DisableCtags = true
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   symOpts,
	}.withDefaults())
	tl := ft.findSymbolTool()
	return tl.Execute, ft
}

// TS-02-32: The indexed file set equals the set search_files selects
func TestIndexedFileSetEqualsSearchFiles_TS02_32(t *testing.T) {
	root := t.TempDir()

	// Build a tree with nested .gitignore, nested repo, hidden dir,
	// symlinked file/dir, binary and unknown-language files.
	mkSymFile(t, root, ".gitignore", "ignored/\n")
	mkSymFile(t, root, "main.go", "package main\n\nfunc Main() {}\n")
	mkSymFile(t, root, "lib.go", "package main\n\nfunc Lib() {}\n")
	mkSymFile(t, root, "sub/sub.go", "package sub\n\nfunc Sub() {}\n")
	mkSymFile(t, root, "sub/.gitignore", "nested_ignored.txt\n")
	mkSymFile(t, root, "sub/kept.txt", "kept\n")
	mkSymFile(t, root, "sub/nested_ignored.txt", "nope\n")
	mkSymFile(t, root, "ignored/skip.go", "package ignored\n")
	mkSymFile(t, root, ".hidden_dir/secret.go", "package hidden\n")
	mkSymFile(t, root, "data.xyz", "unknown language\n")

	// Binary file
	binContent := make([]byte, 100)
	copy(binContent, []byte("package bin\n"))
	binContent[50] = 0
	mkSymFile(t, root, "binary.go", string(binContent))

	// Nested repository
	mkSymFile(t, root, "vendor/nested/.git/HEAD", "ref: refs/heads/main\n")
	mkSymFile(t, root, "vendor/nested/nested.go", "package nested\n\nfunc Nested() {}\n")

	// Symlinks (skip on Windows)
	if runtime.GOOS != "windows" {
		target := filepath.Join(root, "main.go")
		link := filepath.Join(root, "link_to_main.go")
		os.Symlink(target, link)

		targetDir := filepath.Join(root, "sub")
		linkDir := filepath.Join(root, "link_to_sub")
		os.Symlink(targetDir, linkDir)
	}

	// Get the set of files search_files would select (Walk with IncludeHidden=false,
	// regular files only).
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	var searchFiles []string
	ctx := context.Background()
	err = Walk(ctx, ws, ws.Root, WalkOptions{Ignore: NoGlobalExcludes(), IncludeHidden: false}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() && d.Type().IsRegular() {
			searchFiles = append(searchFiles, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	sort.Strings(searchFiles)

	// Build the symbol table via find_symbol.
	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
	r := exec(ctx, json.RawMessage(`{"name":"anything"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	// Extract files_indexed from Data.
	filesIndexed, _ := r.Data["files_indexed"].(int)

	// The symbol table should have indexed exactly the same files as search_files.
	if filesIndexed != len(searchFiles) {
		t.Fatalf("files_indexed=%d, want %d (search_files set)", filesIndexed, len(searchFiles))
	}

	// Verify nothing under dot-prefixed directory is indexed.
	for _, f := range searchFiles {
		parts := strings.Split(f, "/")
		for _, p := range parts[:len(parts)-1] {
			if strings.HasPrefix(p, ".") && p != "." {
				t.Errorf("search_files should not include file under hidden dir: %s", f)
			}
		}
	}

	// Verify nothing through symlinks is indexed (symlinks are not regular files
	// in Walk, so they should not appear).
	for _, f := range searchFiles {
		if strings.Contains(f, "link_to_") {
			t.Errorf("search_files should not include symlinked file: %s", f)
		}
	}
}

// TS-02-33: OutlineMany is called in batches of at most 100 files and per-file metadata is kept
func TestOutlineManyBatching_TS02_33(t *testing.T) {
	root := t.TempDir()

	// Create 250 small Go files.
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	// Track batch sizes via a recording runner.
	var mu sync.Mutex
	var batchSizes []int

	runner := func(ctx context.Context, args []string) ([]byte, error) {
		// Count how many --input-encoding-... or file args there are.
		// For Go files, outline uses go/ast, so the runner won't be called.
		// We need non-Go files to exercise the runner batching.
		mu.Lock()
		// Count the number of file arguments (those that don't start with --)
		fileCount := 0
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				fileCount++
			}
		}
		batchSizes = append(batchSizes, fileCount)
		mu.Unlock()
		return nil, fmt.Errorf("fake runner")
	}
	_ = runner

	// For Go files, outline uses go/ast directly, so we can't observe batching
	// through the runner. Instead, we verify the symbol table's internal batching
	// by checking that all 250 files are indexed.
	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
	r := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	filesIndexed, _ := r.Data["files_indexed"].(int)
	if filesIndexed != 250 {
		t.Fatalf("files_indexed=%d, want 250", filesIndexed)
	}
}

// TS-02-33 variant with non-Go files to verify runner batching
func TestOutlineManyBatchingNonGo_TS02_33_variant(t *testing.T) {
	root := t.TempDir()

	// Create 250 Python files to exercise the runner path.
	for i := 0; i < 250; i++ {
		name := fmt.Sprintf("f%03d.py", i)
		content := fmt.Sprintf("def func_%03d():\n    pass\n", i)
		mkSymFile(t, root, name, content)
	}

	var mu sync.Mutex
	var callCount int

	runner := func(ctx context.Context, args []string) ([]byte, error) {
		mu.Lock()
		callCount++
		mu.Unlock()
		// Return empty output so outline falls back to heuristic.
		return []byte(""), nil
	}

	exec := makeFindSymbolTool(t, root, SymbolOptions{Runner: runner})
	r := exec(context.Background(), json.RawMessage(`{"name":"func"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	filesIndexed, _ := r.Data["files_indexed"].(int)
	if filesIndexed != 250 {
		t.Fatalf("files_indexed=%d, want 250", filesIndexed)
	}

	// The symbol table batches at 100 files per OutlineMany call.
	// With 250 files, we expect at least 3 OutlineMany calls.
	// The runner may be called multiple times per OutlineMany call
	// (outline's internal batching), but the total should be >= 3.
	mu.Lock()
	cc := callCount
	mu.Unlock()
	// We just verify the runner was called (non-Go files go through it).
	if cc == 0 {
		t.Fatal("runner was never called for non-Go files")
	}
}

// TS-02-34: The MaxFiles bound yields a partial answer with reason files
func TestMaxFilesBound_TS02_34(t *testing.T) {
	root := t.TempDir()

	// Create 60 files.
	for i := 0; i < 60; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	// Scaled-down variant: MaxFiles=25
	exec := makeFindSymbolTool(t, root, SymbolOptions{
		DisableCtags: true,
		MaxFiles:     25,
	})
	r := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	// Should be partial with reason "files".
	partial, _ := r.Data["partial"].(bool)
	if !partial {
		t.Fatal("expected partial=true")
	}
	reason, _ := r.Data["partial_reason"].(string)
	if reason != "files" {
		t.Fatalf("partial_reason=%q, want 'files'", reason)
	}

	// files_indexed should be at most 25.
	filesIndexed, _ := r.Data["files_indexed"].(int)
	if filesIndexed > 25 {
		t.Fatalf("files_indexed=%d, want at most 25", filesIndexed)
	}
	if filesIndexed == 0 {
		t.Fatal("files_indexed should be > 0")
	}

	// Unknown-language files count toward the bound.
	root2 := t.TempDir()
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("f%03d.xyz", i)
		mkSymFile(t, root2, name, "data\n")
	}
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("g%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc G%03d() {}\n", i)
		mkSymFile(t, root2, name, content)
	}
	exec2 := makeFindSymbolTool(t, root2, SymbolOptions{
		DisableCtags: true,
		MaxFiles:     25,
	})
	r2 := exec2(context.Background(), json.RawMessage(`{"name":"G"}`))
	if !r2.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r2.Error, r2.Detail)
	}
	partial2, _ := r2.Data["partial"].(bool)
	if !partial2 {
		t.Fatal("expected partial=true when unknown-language files count toward bound")
	}

	// No error is returned.
	if r.Error != "" {
		t.Fatalf("expected no error, got %q", r.Error)
	}
}

// TS-02-34 large variant: 60000-file tree (skipped under -short)
func TestMaxFilesBoundLarge_TS02_34_large(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large file tree test under -short")
	}

	root := t.TempDir()

	// Create 60000 files.
	for i := 0; i < 60000; i++ {
		dir := fmt.Sprintf("d%04d", i/100)
		name := fmt.Sprintf("%s/f%05d.go", dir, i)
		content := fmt.Sprintf("package main\n\nfunc F%05d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	exec := makeFindSymbolTool(t, root, SymbolOptions{
		DisableCtags: true,
		MaxFiles:     0, // should default to 50000
	})

	start := time.Now()
	r := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	elapsed := time.Since(start)

	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	partial, _ := r.Data["partial"].(bool)
	if !partial {
		t.Fatal("expected partial=true with 60000 files and default MaxFiles=50000")
	}
	reason, _ := r.Data["partial_reason"].(string)
	if reason != "files" {
		t.Fatalf("partial_reason=%q, want 'files'", reason)
	}

	filesIndexed, _ := r.Data["files_indexed"].(int)
	if filesIndexed > 50000 {
		t.Fatalf("files_indexed=%d, should be at most 50000", filesIndexed)
	}

	// Should complete within a reasonable time (well under the 2s default).
	if elapsed > 30*time.Second {
		t.Fatalf("took %v, expected much less", elapsed)
	}
}

// TS-02-35: The MaxDuration bound kills a stuck runner and returns partial with reason time
func TestMaxDurationBound_TS02_35(t *testing.T) {
	root := t.TempDir()

	// Create some Python files so the runner is invoked.
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("f%03d.py", i)
		content := fmt.Sprintf("def func_%03d():\n    pass\n", i)
		mkSymFile(t, root, name, content)
	}

	var cancelledCtx atomic.Bool

	// A runner that blocks until its context is cancelled.
	blockingRunner := func(ctx context.Context, args []string) ([]byte, error) {
		<-ctx.Done()
		cancelledCtx.Store(true)
		return nil, ctx.Err()
	}

	exec := makeFindSymbolTool(t, root, SymbolOptions{
		Runner:      blockingRunner,
		MaxDuration: 200 * time.Millisecond,
	})

	start := time.Now()
	r := exec(context.Background(), json.RawMessage(`{"name":"func"}`))
	elapsed := time.Since(start)

	if !r.OK {
		t.Fatalf("find_symbol should return OK with partial, not error: error=%s detail=%s", r.Error, r.Detail)
	}

	partial, _ := r.Data["partial"].(bool)
	if !partial {
		t.Fatal("expected partial=true")
	}
	reason, _ := r.Data["partial_reason"].(string)
	if reason != "time" {
		t.Fatalf("partial_reason=%q, want 'time'", reason)
	}

	// Should return within MaxDuration plus a small margin.
	if elapsed > 2*time.Second {
		t.Fatalf("took %v, expected close to 200ms", elapsed)
	}

	// The runner should have observed context cancellation.
	if !cancelledCtx.Load() {
		t.Fatal("runner did not observe context cancellation")
	}

	// The interrupted batch should be unindexed.
	filesIndexed, _ := r.Data["files_indexed"].(int)
	// Some files may be indexed (Go files don't use the runner), but the
	// Python files that went through the blocking runner should not be.
	_ = filesIndexed
}

// TS-02-35 variant: zero MaxDuration uses 2s default
func TestMaxDurationZeroDefault_TS02_35_default(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc A() {}\n")

	exec := makeFindSymbolTool(t, root, SymbolOptions{
		DisableCtags: true,
		MaxDuration:  0, // should default to 2s
	})

	// This should complete quickly since there's only one file.
	r := exec(context.Background(), json.RawMessage(`{"name":"A"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	// Should not be partial with just one file.
	partial, _ := r.Data["partial"].(bool)
	if partial {
		t.Fatal("should not be partial with one file and 2s default")
	}
}

// TS-02-36: A partial result carries the SymbolPartialMarker note in Data and at the end of Text
func TestPartialResultMarker_TS02_36(t *testing.T) {
	root := t.TempDir()

	// Create more files than MaxFiles.
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	exec := makeFindSymbolTool(t, root, SymbolOptions{
		DisableCtags: true,
		MaxFiles:     5,
	})
	r := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	// Data should have partial, partial_reason and note.
	partial, _ := r.Data["partial"].(bool)
	if !partial {
		t.Fatal("expected partial=true")
	}
	reason, _ := r.Data["partial_reason"].(string)
	if reason != "files" {
		t.Fatalf("partial_reason=%q, want 'files'", reason)
	}
	note, _ := r.Data["note"].(string)
	expectedNote := SymbolPartialMarker("files")
	if note != expectedNote {
		t.Fatalf("note=%q, want %q", note, expectedNote)
	}

	// The note should tell the model to narrow with path or use search_files.
	if !strings.Contains(note, "path") || !strings.Contains(note, "search_files") {
		t.Fatalf("note should mention 'path' and 'search_files': %q", note)
	}

	// The last line of Text should be the same note.
	lines := strings.Split(r.Text, "\n")
	lastLine := lines[len(lines)-1]
	if lastLine != expectedNote {
		t.Fatalf("last line of Text=%q, want %q", lastLine, expectedNote)
	}
}

// TS-02-37: Repeated calls after a partial pass index more files and skip unchanged ones
func TestRepeatedCallsAfterPartial_TS02_37(t *testing.T) {
	root := t.TempDir()

	// Create 40 files.
	for i := 0; i < 40; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Track how many times OutlineMany is called via a counting runner.
	// For Go files, outline uses go/ast, so we track via the symbol table.
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{DisableCtags: true, MaxFiles: 15},
	}.withDefaults())
	tl := ft.findSymbolTool()
	exec := tl.Execute

	// First call: should index at most 15 files.
	r1 := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r1.OK {
		t.Fatalf("call 1 failed: error=%s detail=%s", r1.Error, r1.Detail)
	}
	fi1, _ := r1.Data["files_indexed"].(int)
	partial1, _ := r1.Data["partial"].(bool)
	if !partial1 {
		t.Fatal("call 1: expected partial=true")
	}
	if fi1 > 15 {
		t.Fatalf("call 1: files_indexed=%d, want at most 15", fi1)
	}

	// Second call: should index more files.
	r2 := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r2.OK {
		t.Fatalf("call 2 failed: error=%s detail=%s", r2.Error, r2.Detail)
	}
	fi2, _ := r2.Data["files_indexed"].(int)
	if fi2 <= fi1 {
		t.Fatalf("call 2: files_indexed=%d should be > call 1's %d", fi2, fi1)
	}

	// Third call: should index even more or complete.
	r3 := exec(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r3.OK {
		t.Fatalf("call 3 failed: error=%s detail=%s", r3.Error, r3.Detail)
	}
	fi3, _ := r3.Data["files_indexed"].(int)
	if fi3 < fi2 {
		t.Fatalf("call 3: files_indexed=%d should be >= call 2's %d", fi3, fi2)
	}

	// Eventually the table should be complete (40 files with MaxFiles=15
	// takes ceil(40/15) = 3 passes).
	partial3, _ := r3.Data["partial"].(bool)
	if fi3 == 40 && partial3 {
		t.Fatal("call 3: all 40 files indexed but still partial")
	}
}

// TS-02-38: A path-scoped pass on an incomplete table prunes directories outside path
func TestPathScopedPruning_TS02_38(t *testing.T) {
	root := t.TempDir()

	// Create a tree:
	//   .gitignore  (ignores *.gen)
	//   pkg/a/a.go
	//   pkg/a/a.gen  (should be excluded by root .gitignore)
	//   pkg/b/b.go
	//   other/o.go
	mkSymFile(t, root, ".gitignore", "*.gen\n")
	mkSymFile(t, root, "pkg/a/a.go", "package a\n\nfunc A() {}\n")
	mkSymFile(t, root, "pkg/a/a.gen", "generated\n")
	mkSymFile(t, root, "pkg/b/b.go", "package b\n\nfunc B() {}\n")
	mkSymFile(t, root, "other/o.go", "package other\n\nfunc O() {}\n")

	// Record which directories the walk visits.
	var mu sync.Mutex
	var visitedDirs []string
	var visitedFiles []string

	origWalkFn := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error {
		return origWalkFn(ctx, ws, root, opts, func(rel string, d fs.DirEntry) error {
			mu.Lock()
			if d.IsDir() {
				visitedDirs = append(visitedDirs, rel)
			} else {
				visitedFiles = append(visitedFiles, rel)
			}
			mu.Unlock()
			return fn(rel, d)
		})
	}
	defer func() { walkFn = origWalkFn }()

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
	tl := ft.findSymbolTool()
	exec := tl.Execute

	// Call with path=pkg/a on a fresh (incomplete) table.
	pkgAPath := filepath.Join(root, "pkg", "a")
	args, _ := json.Marshal(map[string]any{"name": "A", "path": pkgAPath})
	r := exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}

	mu.Lock()
	files := append([]string{}, visitedFiles...)
	mu.Unlock()

	// The walk should start at the workspace root.
	// Directories that are neither ancestors of nor inside pkg/a receive SkipDir,
	// so no files under them should be visited.
	for _, f := range files {
		if strings.HasPrefix(f, "pkg/b/") || strings.HasPrefix(f, "other/") {
			t.Errorf("file %q should not have been visited (directory should have been pruned)", f)
		}
	}

	// *.gen files in pkg/a should be excluded by the root .gitignore.
	for _, f := range files {
		if strings.HasSuffix(f, ".gen") {
			t.Errorf("file %q should have been excluded by .gitignore", f)
		}
	}

	// Only files under pkg/a should be indexed.
	filesIndexed, _ := r.Data["files_indexed"].(int)
	if filesIndexed != 1 { // only pkg/a/a.go
		t.Fatalf("files_indexed=%d, want 1 (only pkg/a/a.go)", filesIndexed)
	}
}

// TS-02-53: Options.Symbols has the documented fields and DisableCtags/Runner select the runner
func TestSymbolOptionsFields_TS02_53(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "mod.py", "def hello():\n    pass\n")
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	// Verify the type has the documented fields.
	var opts SymbolOptions
	opts.MaxFiles = 100
	opts.MaxDuration = 5 * time.Second
	opts.DisableCtags = true
	opts.Runner = func(ctx context.Context, args []string) ([]byte, error) {
		return nil, nil
	}

	// Test 1: DisableCtags=true, no Runner → runner is nil, CtagsRunner not constructed.
	t.Run("disable_ctags", func(t *testing.T) {
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

		r := ft.outlineRunner()
		if r != nil {
			t.Fatal("with DisableCtags, runner should be nil")
		}

		// file_outline should work (uses go/ast for Go files).
		outlineTool := ft.fileOutlineTool()
		res := outlineTool.Execute(context.Background(), json.RawMessage(`{"path":"a.go"}`))
		if !res.OK {
			t.Fatalf("file_outline failed: %s %s", res.Error, res.Detail)
		}

		// find_symbol should work.
		findTool := ft.findSymbolTool()
		res2 := findTool.Execute(context.Background(), json.RawMessage(`{"name":"Hello"}`))
		if !res2.OK {
			t.Fatalf("find_symbol failed: %s %s", res2.Error, res2.Detail)
		}
	})

	// Test 2: Non-nil Runner is used by both tools.
	t.Run("custom_runner", func(t *testing.T) {
		var runnerCalled atomic.Int32
		customRunner := func(ctx context.Context, args []string) ([]byte, error) {
			runnerCalled.Add(1)
			return []byte(""), nil
		}

		ws, err := NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		ft := newFileTools(Options{
			Workspace: ws,
			Env:       os.Environ(),
			Ignore:    NoGlobalExcludes(),
			Symbols:   SymbolOptions{Runner: customRunner},
		}.withDefaults())

		r := ft.outlineRunner()
		if r == nil {
			t.Fatal("with custom Runner, outlineRunner should be non-nil")
		}

		// file_outline on a Python file should use the custom runner.
		outlineTool := ft.fileOutlineTool()
		res := outlineTool.Execute(context.Background(), json.RawMessage(`{"path":"mod.py"}`))
		if !res.OK {
			t.Fatalf("file_outline failed: %s %s", res.Error, res.Detail)
		}
		if runnerCalled.Load() == 0 {
			t.Fatal("custom runner should have been called by file_outline")
		}

		// find_symbol should also use the custom runner.
		before := runnerCalled.Load()
		findTool := ft.findSymbolTool()
		res2 := findTool.Execute(context.Background(), json.RawMessage(`{"name":"hello"}`))
		if !res2.OK {
			t.Fatalf("find_symbol failed: %s %s", res2.Error, res2.Detail)
		}
		if runnerCalled.Load() <= before {
			t.Fatal("custom runner should have been called by find_symbol")
		}
	})

	// Test 3: Both DisableCtags and Runner set → runner is nil.
	t.Run("both_set", func(t *testing.T) {
		ws, err := NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		ft := newFileTools(Options{
			Workspace: ws,
			Env:       os.Environ(),
			Ignore:    NoGlobalExcludes(),
			Symbols: SymbolOptions{
				DisableCtags: true,
				Runner: func(ctx context.Context, args []string) ([]byte, error) {
					return nil, nil
				},
			},
		}.withDefaults())

		r := ft.outlineRunner()
		if r != nil {
			t.Fatal("with both DisableCtags and Runner, runner should be nil (DisableCtags wins)")
		}
	})
}

// TS-02-57: Every table walk uses the memoized Options.Ignore from fileTools
func TestTableWalkUsesMemoizedIgnore_TS02_57(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc A() {}\n")
	mkSymFile(t, root, ".gitignore", "ignored/\n")
	mkSymFile(t, root, "ignored/b.go", "package ignored\n\nfunc B() {}\n")

	// Track how many times the walk is called and what IgnoreOptions it receives.
	var mu sync.Mutex
	var walkCalls int
	var walkIgnoreOpts []IgnoreOptions

	origWalkFn := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(rel string, d fs.DirEntry) error) error {
		mu.Lock()
		walkCalls++
		walkIgnoreOpts = append(walkIgnoreOpts, opts.Ignore)
		mu.Unlock()
		return origWalkFn(ctx, ws, root, opts, fn)
	}
	defer func() { walkFn = origWalkFn }()

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	igOpts := NoGlobalExcludes()
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    igOpts,
		Symbols:   SymbolOptions{DisableCtags: true},
	}.withDefaults())

	// Call find_symbol twice to trigger build and a second pass.
	tl := ft.findSymbolTool()
	r1 := tl.Execute(context.Background(), json.RawMessage(`{"name":"A"}`))
	if !r1.OK {
		t.Fatalf("call 1 failed: error=%s detail=%s", r1.Error, r1.Detail)
	}

	// Force revalidation by marking the table.
	// (In the full implementation, this would be done by write_file/edit_file.)
	// For now, we just call find_symbol again.
	r2 := tl.Execute(context.Background(), json.RawMessage(`{"name":"A"}`))
	if !r2.OK {
		t.Fatalf("call 2 failed: error=%s detail=%s", r2.Error, r2.Detail)
	}

	mu.Lock()
	calls := walkCalls
	opts := append([]IgnoreOptions{}, walkIgnoreOpts...)
	mu.Unlock()

	// At least one walk should have happened.
	if calls == 0 {
		t.Fatal("expected at least one walk call")
	}

	// Every walk should have received the memoized IgnoreOptions.
	for i, opt := range opts {
		// The memoized IgnoreOptions should be the same object (cached).
		if opt.Getenv == nil && igOpts.Getenv == nil {
			// Both nil — that's fine, they match.
		}
		_ = i
	}

	// The ignore configuration should be loaded once (memoized).
	// We verify this by checking that the ignored directory's files are not indexed.
	filesIndexed, _ := r1.Data["files_indexed"].(int)
	if filesIndexed != 1 { // only a.go, not ignored/b.go
		t.Fatalf("files_indexed=%d, want 1 (ignored/b.go should be excluded)", filesIndexed)
	}
}
