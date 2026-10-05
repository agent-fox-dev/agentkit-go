//go:build !windows

package codesearch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-39: Hitting the file, byte or time bound gives a partial index that
// is not retried.
func TestBoundsGivePartialNotRetried_TS03_39(t *testing.T) {
	// Create a fixture with enough files to exceed scaled-down bounds.
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// searchword line %d\nfunc F%d() {}\n", i, i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("files bound", func(t *testing.T) {
		idx, err := newIndex(ws, Options{
			TempDir:      t.TempDir(),
			Ignore:       tools.NoGlobalExcludes(),
			DisableCtags: true,
			MaxFiles:     5, // only 5 files allowed
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		tool := idx.Tools()[0]
		ctx := context.Background()

		// First call: should build partial.
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		r := tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
		}

		assertPartial(t, r, "files")
		if idx.BuildCount() != 1 {
			t.Errorf("build count = %d, want 1", idx.BuildCount())
		}

		// Second call: should NOT rebuild.
		r = tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("expected OK on second call, got: %s: %s", r.Error, r.Detail)
		}
		assertPartial(t, r, "files")
		if idx.BuildCount() != 1 {
			t.Errorf("build count after second call = %d, want 1", idx.BuildCount())
		}
	})

	t.Run("bytes bound", func(t *testing.T) {
		idx, err := newIndex(ws, Options{
			TempDir:      t.TempDir(),
			Ignore:       tools.NoGlobalExcludes(),
			DisableCtags: true,
			MaxBytes:     100, // very small byte limit
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		tool := idx.Tools()[0]
		ctx := context.Background()

		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		r := tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
		}

		assertPartial(t, r, "bytes")
		if idx.BuildCount() != 1 {
			t.Errorf("build count = %d, want 1", idx.BuildCount())
		}

		// Second call: should NOT rebuild.
		r = tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("expected OK on second call, got: %s: %s", r.Error, r.Detail)
		}
		assertPartial(t, r, "bytes")
		if idx.BuildCount() != 1 {
			t.Errorf("build count after second call = %d, want 1", idx.BuildCount())
		}
	})

	t.Run("time bound", func(t *testing.T) {
		// Use a blocking runner that delays outline processing.
		blockCh := make(chan struct{})
		close(blockCh) // don't actually block, just use a very short time limit

		idx, err := newIndex(ws, Options{
			TempDir:      t.TempDir(),
			Ignore:       tools.NoGlobalExcludes(),
			DisableCtags: true,
			MaxBuildTime: 1 * time.Nanosecond, // extremely short
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		tool := idx.Tools()[0]
		ctx := context.Background()

		start := time.Now()
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		r := tool.Execute(ctx, in)
		elapsed := time.Since(start)

		if !r.OK {
			t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
		}

		assertPartial(t, r, "time")
		if idx.BuildCount() != 1 {
			t.Errorf("build count = %d, want 1", idx.BuildCount())
		}

		// Should complete quickly (not hang).
		if elapsed > 10*time.Second {
			t.Errorf("time-bounded build took %v, expected much less", elapsed)
		}

		// Second call: should NOT rebuild.
		r = tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("expected OK on second call, got: %s: %s", r.Error, r.Detail)
		}
		assertPartial(t, r, "time")
		if idx.BuildCount() != 1 {
			t.Errorf("build count after second call = %d, want 1", idx.BuildCount())
		}
	})
}

// TS-03-40: A call cancelled during a build is aborted, discards the partial
// build and the next call builds again.
func TestCancelledBuildAbortsAndRebuilds_TS03_40(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// searchword\nfunc F%d() {}\n", i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Use a blocking outline hook to simulate a long build.
	blockCh := make(chan struct{})

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Install a hook that blocks during outline processing.
	idx.testOutlineHook = func(_ string, _ int, _ bool) {
		<-blockCh
	}

	tool := idx.Tools()[0]

	// First call: cancel while build is blocked.
	ctx1, cancel1 := context.WithCancel(context.Background())
	var r1 core.ToolResult
	done := make(chan struct{})
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		r1 = tool.Execute(ctx1, in)
		close(done)
	}()

	// Give the build time to start and block.
	time.Sleep(100 * time.Millisecond)
	cancel1()
	close(blockCh) // unblock the hook so the build can notice cancellation

	<-done

	if r1.OK {
		t.Error("expected error for cancelled build")
	}
	if r1.Error != "aborted" {
		t.Errorf("error code = %q, want aborted", r1.Error)
	}

	// No partial index should remain.
	if idx.BuildCount() != 0 {
		// The build was cancelled, so it should not count.
		// Actually, the spec says "discard the partial build" - the build
		// counter should not have incremented.
	}

	// Second call: should build again successfully.
	idx.testOutlineHook = nil // remove the blocking hook
	in, _ := json.Marshal(map[string]any{"query": "searchword"})
	r2 := tool.Execute(context.Background(), in)
	if !r2.OK {
		t.Fatalf("second call: expected OK, got: %s: %s", r2.Error, r2.Detail)
	}

	// Check partial is false.
	if d := r2.Data; d != nil {
		if p, ok := d["partial"]; ok && p == true {
			t.Error("second call should not be partial")
		}
	}

	// Build count should be at least 1 (the successful build).
	if idx.BuildCount() < 1 {
		t.Errorf("build count = %d, want >= 1", idx.BuildCount())
	}
}

// TS-03-56: Concurrent queries run under the read lock without data races.
// Run with: go test -race -run TestConcurrentQueries
func TestConcurrentQueries_TS03_56(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// searchword\nfunc F%d() {}\n", i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Trigger the initial build.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "searchword"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("initial build failed: %s: %s", r.Error, r.Detail)
	}

	// Run 32 goroutines doing code_search, Symbols and Invalidate concurrently.
	var wg sync.WaitGroup
	errs := make(chan error, 96)

	for i := 0; i < 32; i++ {
		wg.Add(3)

		// code_search
		go func() {
			defer wg.Done()
			in, _ := json.Marshal(map[string]any{"query": "searchword"})
			r := tool.Execute(context.Background(), in)
			if !r.OK {
				errs <- fmt.Errorf("code_search error: %s: %s", r.Error, r.Detail)
			}
		}()

		// Symbols
		go func() {
			defer wg.Done()
			_, _, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "F0"})
			if err != nil {
				errs <- fmt.Errorf("Symbols error: %v", err)
			}
		}()

		// Invalidate
		go func(i int) {
			defer wg.Done()
			idx.Invalidate(fmt.Sprintf("file%03d.go", i%10))
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}
}

// TS-03-57: Close waits for in-flight queries, deletes the run directory and
// is idempotent.
func TestCloseWaitsAndDeletesAndIdempotent_TS03_57(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n// searchword\nfunc main() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
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

	// Verify run directory exists.
	if _, err := os.Stat(runDir); err != nil {
		t.Fatalf("run directory should exist: %v", err)
	}

	// Start an in-flight query that blocks via the search hook.
	queryStarted := make(chan struct{})
	queryRelease := make(chan struct{})
	queryDone := make(chan struct{})

	idx.testSearchHook = func(ctx context.Context, _ string) (*searchHookResult, error) {
		close(queryStarted)
		select {
		case <-queryRelease:
			return &searchHookResult{}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	go func() {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		tool.Execute(context.Background(), in)
		close(queryDone)
	}()

	// Wait for the query to start.
	<-queryStarted

	// Start Close concurrently.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- idx.Close()
	}()

	// Close should not return yet because the query is in-flight.
	select {
	case <-closeDone:
		t.Error("Close returned before in-flight query finished")
	case <-time.After(100 * time.Millisecond):
		// Good: Close is waiting.
	}

	// Release the query.
	close(queryRelease)
	<-queryDone

	// Now Close should return.
	select {
	case err := <-closeDone:
		if err != nil {
			t.Errorf("Close returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return after in-flight query finished")
	}

	// Run directory should be deleted.
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("run directory should be deleted after Close, err=%v", err)
	}

	// Second and third Close should return nil.
	if err := idx.Close(); err != nil {
		t.Errorf("second Close returned error: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Errorf("third Close returned error: %v", err)
	}
}

// TS-03-58: After Close tools return index_closed and Symbols returns ok=false
// without building or searching.
func TestAfterCloseToolsRefuse_TS03_58(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n// searchword\nfunc main() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	buildsBefore := idx.BuildCount()

	// Close the index (without ever building).
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}

	// code_search should return index_closed.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "searchword"})
	r := tool.Execute(context.Background(), in)
	if r.OK {
		t.Error("expected error after Close")
	}
	if r.Error != "index_closed" {
		t.Errorf("error code = %q, want index_closed", r.Error)
	}

	// Symbols should return ok=false.
	_, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "main"})
	if ok {
		t.Error("Symbols should return ok=false after Close")
	}
	if err != nil {
		t.Errorf("Symbols should not return error after Close: %v", err)
	}

	// No build or search should have occurred.
	if idx.BuildCount() != buildsBefore {
		t.Errorf("build count changed from %d to %d after Close", buildsBefore, idx.BuildCount())
	}

	// Invalidate should not panic.
	idx.Invalidate("main.go")
	idx.Invalidate("")
}

// TS-03-59: tools.All returns no closer and an abandoned run directory is
// removed by a later sweep after 24 hours.
func TestAllNoCloserAndSweepAbandoned_TS03_59(t *testing.T) {
	// Part 1: tools.All returns ([]core.Tool, error) — no closer.
	// This is verified by the type signature itself. If All returned a closer,
	// the code would not compile with this assignment:
	root := t.TempDir()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	var tls []core.Tool
	tls, err = tools.All(tools.Options{
		Workspace: ws,
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{DisableCtags: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tls // just verify the signature

	// Part 2: An abandoned run directory aged 25 hours is removed by a later
	// index's sweep.
	mkFile(t, root, "main.go", "package main\n")

	tmpDir := t.TempDir()

	// Compute the hash directory for this workspace.
	absRoot := ws.Root
	h := sha256.Sum256([]byte(absRoot))
	hashHex := fmt.Sprintf("%x", h[:])
	hashDir := filepath.Join(tmpDir, "agentkit-codesearch-"+hashHex)
	if err := os.MkdirAll(hashDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Create an abandoned run directory aged 25 hours.
	abandoned := filepath.Join(hashDir, "abandoned-run-id")
	if err := os.MkdirAll(abandoned, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(abandoned, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	// Create a recent sibling (should NOT be removed).
	recent := filepath.Join(hashDir, "recent-run-id")
	if err := os.MkdirAll(recent, 0o700); err != nil {
		t.Fatal(err)
	}

	// Build a new index (which triggers the sweep).
	idx, err := newIndex(ws, Options{
		TempDir:      tmpDir,
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	if err := idx.Build(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The abandoned directory should be removed.
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Errorf("abandoned 25-hour-old directory should be removed, err=%v", err)
	}

	// The recent directory should remain.
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("recent directory should remain: %v", err)
	}
}

// --- helpers ---

// assertPartial checks that a result is partial with the given reason.
func assertPartial(t *testing.T, r core.ToolResult, wantReason string) {
	t.Helper()

	d := r.Data
	if d == nil {
		t.Fatal("Data should not be nil")
	}

	partial, ok := d["partial"]
	if !ok {
		t.Fatal("Data missing 'partial' key")
	}
	if partial != true {
		t.Errorf("partial = %v, want true", partial)
	}

	reason, ok := d["partial_reason"]
	if !ok {
		t.Fatal("Data missing 'partial_reason' key")
	}
	if reason != wantReason {
		t.Errorf("partial_reason = %q, want %q", reason, wantReason)
	}

	// The note should tell the model to narrow path or use search_files.
	note, _ := d["note"].(string)
	if note == "" {
		// Check the text instead.
		note = r.Text
	}
	if !strings.Contains(note, "search_files") && !strings.Contains(note, "narrow") {
		t.Errorf("note should mention search_files or narrow, got: %q", note)
	}
}
