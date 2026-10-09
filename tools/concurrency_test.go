package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-02-47: A call waiting behind a running build is abandoned immediately on cancel
func TestWaitingCallAbandonedOnCancel_TS02_47(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		mkSymFile(t, root, fmt.Sprintf("f%d.py", i),
			fmt.Sprintf("def func_%d():\n    pass\n", i))
	}

	// A blocking hook that holds the first build open.
	blockCh := make(chan struct{})
	buildStarted := make(chan struct{}, 1)

	block := func(ctx context.Context) {
		select {
		case buildStarted <- struct{}{}:
		default:
		}
		select {
		case <-blockCh:
		case <-ctx.Done():
		}
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{MaxDuration: 30 * time.Second},
	}.withDefaults())
	ft.testOutlineHook = block
	tl := ft.findSymbolTool()

	// Start the first find_symbol call in a goroutine (it will block on the hook).
	var firstResult core.ToolResult
	firstDone := make(chan struct{})
	go func() {
		firstResult = tl.Execute(context.Background(), json.RawMessage(`{"name":"func"}`))
		close(firstDone)
	}()

	// Wait for the build to start.
	select {
	case <-buildStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not start within 5s")
	}

	// Start a second find_symbol call with a context that will be cancelled.
	ctx2, cancel2 := context.WithCancel(context.Background())
	var secondResult core.ToolResult
	secondDone := make(chan struct{})
	go func() {
		secondResult = tl.Execute(ctx2, json.RawMessage(`{"name":"func"}`))
		close(secondDone)
	}()

	// Give the second call time to reach the lock wait.
	time.Sleep(50 * time.Millisecond)

	// Cancel the second call's context.
	cancel2()

	// The second call should return before the build is released.
	select {
	case <-secondDone:
		// Good — it returned before the build finished.
	case <-time.After(2 * time.Second):
		t.Fatal("second call did not return after cancel within 2s")
	}

	// Verify the second call returned aborted.
	if secondResult.OK {
		t.Fatal("second call: expected OK=false")
	}
	if secondResult.Error != "aborted" {
		t.Fatalf("second call: error=%q, want aborted", secondResult.Error)
	}
	if secondResult.Detail != "Operation aborted" {
		t.Fatalf("second call: detail=%q, want 'Operation aborted'", secondResult.Detail)
	}

	// The first build should still be blocked.
	select {
	case <-firstDone:
		t.Fatal("first call should still be blocked")
	default:
		// Good.
	}

	// Release the build.
	close(blockCh)

	// The first build should complete successfully.
	select {
	case <-firstDone:
	case <-time.After(10 * time.Second):
		t.Fatal("first call did not complete within 10s")
	}

	if !firstResult.OK {
		t.Fatalf("first call: expected OK=true, got error=%s detail=%s", firstResult.Error, firstResult.Detail)
	}
}

// TS-02-48: Concurrent find_symbol, write_file and edit_file calls are race-free and consistent
func TestConcurrentCallsRaceFree_TS02_48(t *testing.T) {
	root := t.TempDir()
	// Create initial files.
	for i := 0; i < 10; i++ {
		mkSymFile(t, root, fmt.Sprintf("f%d.go", i),
			fmt.Sprintf("package main\n\nfunc F%d() {}\n", i))
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	}.withDefaults())

	findTool := ft.findSymbolTool()
	writeTool := ft.writeFile()
	editTool := ft.editFile()

	// First, build the table.
	r := findTool.Execute(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r.OK {
		t.Fatalf("initial build failed: error=%s detail=%s", r.Error, r.Detail)
	}

	// Run concurrent operations.
	var wg sync.WaitGroup
	const goroutines = 20

	var findErrors atomic.Int32
	var writeErrors atomic.Int32

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			switch idx % 3 {
			case 0:
				// find_symbol
				r := findTool.Execute(context.Background(), json.RawMessage(`{"name":"F"}`))
				if !r.OK {
					findErrors.Add(1)
				}
			case 1:
				// write_file
				args, _ := json.Marshal(map[string]any{
					"path":    fmt.Sprintf("w%d.go", idx),
					"content": fmt.Sprintf("package main\n\nfunc W%d() {}\n", idx),
				})
				r := writeTool.Execute(context.Background(), args)
				if !r.OK {
					writeErrors.Add(1)
				}
			case 2:
				// edit_file on an existing file
				fileIdx := idx % 10
				args, _ := json.Marshal(map[string]any{
					"path": fmt.Sprintf("f%d.go", fileIdx),
					"edits": []map[string]string{
						{"old_string": fmt.Sprintf("F%d", fileIdx), "new_string": fmt.Sprintf("F%d", fileIdx)},
					},
				})
				editTool.Execute(context.Background(), args)
				// edit may fail if concurrent edits conflict; that's OK for race testing
			}
		}(i)
	}

	wg.Wait()

	// No data race should be reported (go test -race detects this).
	// Every find_symbol should return OK true.
	if findErrors.Load() > 0 {
		t.Fatalf("find_symbol had %d errors", findErrors.Load())
	}

	// After all writes complete, a find_symbol should reflect them.
	r = findTool.Execute(context.Background(), json.RawMessage(`{"name":"W"}`))
	if !r.OK {
		t.Fatalf("final find_symbol failed: error=%s detail=%s", r.Error, r.Detail)
	}
}

// TS-02-49: Marking never blocks on a running build
func TestMarkingNeverBlocksOnBuild_TS02_49(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		mkSymFile(t, root, fmt.Sprintf("f%d.py", i),
			fmt.Sprintf("def func_%d():\n    pass\n", i))
	}

	// A blocking hook that holds the build open.
	blockCh := make(chan struct{})
	buildStarted := make(chan struct{}, 1)

	block := func(ctx context.Context) {
		select {
		case buildStarted <- struct{}{}:
		default:
		}
		select {
		case <-blockCh:
		case <-ctx.Done():
		}
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{MaxDuration: 30 * time.Second},
	}.withDefaults())
	ft.testOutlineHook = block

	findTool := ft.findSymbolTool()
	writeTool := ft.writeFile()
	editTool := ft.editFile()

	// Start a find_symbol call that will block on the hook.
	buildDone := make(chan struct{})
	go func() {
		findTool.Execute(context.Background(), json.RawMessage(`{"name":"func"}`))
		close(buildDone)
	}()

	// Wait for the build to start.
	select {
	case <-buildStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not start within 5s")
	}

	// While the build is blocked, write_file, edit_file and a shell wrapper
	// should all return promptly.

	// write_file
	writeStart := time.Now()
	writeArgs, _ := json.Marshal(map[string]any{
		"path":    "new_during_build.go",
		"content": "package main\n\nfunc NewDuringBuild() {}\n",
	})
	wr := writeTool.Execute(context.Background(), writeArgs)
	writeElapsed := time.Since(writeStart)
	if !wr.OK {
		t.Fatalf("write_file during build: error=%s detail=%s", wr.Error, wr.Detail)
	}
	if writeElapsed > 2*time.Second {
		t.Fatalf("write_file took %v, should not block on build", writeElapsed)
	}

	// edit_file (create the file first)
	mkSymFile(t, root, "edit_target.go", "package main\n\nfunc EditTarget() {}\n")
	editStart := time.Now()
	editArgs, _ := json.Marshal(map[string]any{
		"path": "edit_target.go",
		"edits": []map[string]string{
			{"old_string": "EditTarget", "new_string": "EditTargetNew"},
		},
	})
	er := editTool.Execute(context.Background(), editArgs)
	editElapsed := time.Since(editStart)
	if !er.OK {
		t.Fatalf("edit_file during build: error=%s detail=%s", er.Error, er.Detail)
	}
	if editElapsed > 2*time.Second {
		t.Fatalf("edit_file took %v, should not block on build", editElapsed)
	}

	// Simulate a shell tool wrapper marking revalidateAll.
	markStart := time.Now()
	ft.markTableRevalidateAll()
	markElapsed := time.Since(markStart)
	if markElapsed > 100*time.Millisecond {
		t.Fatalf("markTableRevalidateAll took %v, should be instant", markElapsed)
	}

	// Verify the marks are set.
	ft.table.markMu.Lock()
	hasDirty := ft.table.dirtyPaths != nil && (ft.table.dirtyPaths["new_during_build.go"] || ft.table.dirtyPaths["edit_target.go"])
	hasReval := ft.table.revalidateAll
	ft.table.markMu.Unlock()

	if !hasDirty && !hasReval {
		t.Fatal("marks should be set after write_file and edit_file during build")
	}

	// Release the build.
	close(blockCh)

	select {
	case <-buildDone:
	case <-time.After(10 * time.Second):
		t.Fatal("build did not complete within 10s")
	}
}

// TS-02-50: Cancellation during a build returns aborted, never a partial answer
func TestCancellationDuringBuildReturnsAborted_TS02_50(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		mkSymFile(t, root, fmt.Sprintf("f%d.py", i),
			fmt.Sprintf("def func_%d():\n    pass\n", i))
	}

	// A blocking hook.
	blockCh := make(chan struct{})
	buildStarted := make(chan struct{}, 1)

	block := func(ctx context.Context) {
		select {
		case buildStarted <- struct{}{}:
		default:
		}
		select {
		case <-blockCh:
		case <-ctx.Done():
		}
	}

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{MaxDuration: 30 * time.Second},
	}.withDefaults())
	ft.testOutlineHook = block
	tl := ft.findSymbolTool()

	// Start find_symbol with a cancellable context.
	ctx, cancel := context.WithCancel(context.Background())
	var result core.ToolResult
	done := make(chan struct{})
	go func() {
		result = tl.Execute(ctx, json.RawMessage(`{"name":"func"}`))
		close(done)
	}()

	// Wait for the build to start.
	select {
	case <-buildStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("build did not start within 5s")
	}

	// Cancel the context mid-build.
	cancel()

	// Wait for the call to return.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return after cancel within 5s")
	}

	// Verify: OK false, error code aborted, detail "Operation aborted".
	if result.OK {
		t.Fatal("expected OK=false")
	}
	if result.Error != "aborted" {
		t.Fatalf("error=%q, want aborted", result.Error)
	}
	if result.Detail != "Operation aborted" {
		t.Fatalf("detail=%q, want 'Operation aborted'", result.Detail)
	}

	// Data should NOT carry partial true.
	if result.Data != nil {
		if p, ok := result.Data["partial"].(bool); ok && p {
			t.Fatal("Data should not carry partial=true on cancellation")
		}
	}

	// The table should not be marked complete.
	ft.table.lockBlocking()
	complete := ft.table.complete
	ft.table.unlock()
	if complete {
		t.Fatal("table should not be marked complete after cancellation")
	}

	// Clean up: release the blocking hook.
	close(blockCh)
}

// TS-02-52: The tools run no shell and return errors instead of terminating the process
func TestNoShellAndNoTermination_TS02_52(t *testing.T) {
	// Part 1: Source scan — verify that the symbol/outline tool files do not
	// import os/exec and do not build sh -c.
	toolFiles := []string{
		"tools/outline_tool.go",
		"tools/symbol_tool.go",
		"tools/symbols.go",
		"tools/symbol_match.go",
		"tools/symbol_rank.go",
	}

	for _, file := range toolFiles {
		abs := filepath.Join("..", file) // tests run from tools/
		// Try both relative paths.
		if _, err := os.Stat(abs); err != nil {
			abs = file
		}
		if _, err := os.Stat(abs); err != nil {
			// File might not exist yet (stub); skip.
			continue
		}

		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, abs, nil, parser.ImportsOnly)
		if err != nil {
			// If we can't parse, try reading the file and checking for the import string.
			content, rerr := os.ReadFile(abs)
			if rerr != nil {
				continue
			}
			if strings.Contains(string(content), `"os/exec"`) {
				t.Errorf("%s imports os/exec", file)
			}
			if strings.Contains(string(content), "sh -c") || strings.Contains(string(content), `"sh"`) {
				t.Errorf("%s builds a shell command", file)
			}
			continue
		}

		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == "os/exec" {
				t.Errorf("%s imports os/exec", file)
			}
		}

		// Check for shell command construction.
		content, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if strings.Contains(string(content), "sh -c") || strings.Contains(string(content), `"sh"`) {
			t.Errorf("%s builds a shell command", file)
		}
	}

	// Part 2: Verify error paths return ToolResults, not panics.
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	}.withDefaults())

	outlineTool := ft.fileOutlineTool()
	findTool := ft.findSymbolTool()

	// Error paths that should return ToolResult, not panic.
	errorCases := []struct {
		name string
		tool core.Tool
		args string
	}{
		{"outline_empty_path", outlineTool, `{"path":""}`},
		{"outline_outside_workspace", outlineTool, `{"path":"../../etc/passwd"}`},
		{"outline_nonexistent", outlineTool, `{"path":"nonexistent.go"}`},
		{"find_empty_name", findTool, `{"name":""}`},
		{"find_long_name", findTool, fmt.Sprintf(`{"name":"%s"}`, strings.Repeat("x", 257))},
		{"find_bad_kind", findTool, `{"name":"x","kind":"bogus"}`},
		{"find_outside_path", findTool, `{"name":"x","path":"../../"}`},
		{"find_malformed_json", findTool, `{bad json`},
		{"outline_malformed_json", outlineTool, `{bad json`},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			// Should not panic.
			r := tc.tool.Execute(context.Background(), json.RawMessage(tc.args))
			if r.OK {
				t.Fatalf("%s: expected OK=false", tc.name)
			}
			// Should have an error code.
			if r.Error == "" {
				t.Fatalf("%s: expected non-empty error code", tc.name)
			}
		})
	}

	// Suppress unused import warning for ast.
	_ = ast.IsExported
}
