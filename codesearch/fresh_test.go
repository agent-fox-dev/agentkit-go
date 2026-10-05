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
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-15: Invalidate returns immediately during a blocked build and never
// panics on a closed index.
func TestInvalidateNonBlockingAndSafeOnClosed_TS03_15(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		mkFile(t, root, fmt.Sprintf("file%d.go", i),
			fmt.Sprintf("package pkg\n// word%d\nfunc F%d() {}\n", i, i))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// --- Part 1: Invalidate returns immediately while a build is blocked ---
	blockCh := make(chan struct{})
	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Install a hook that blocks during outline processing.
	idx.testOutlineHook = func(_ string, _ int, _ bool) {
		<-blockCh
	}

	tool := idx.Tools()[0]

	// Start a code_search that will block during the build.
	buildStarted := make(chan struct{}, 1)
	origHook := idx.testOutlineHook
	idx.testOutlineHook = func(root string, batchSize int, runner bool) {
		select {
		case buildStarted <- struct{}{}:
		default:
		}
		origHook(root, batchSize, runner)
	}

	searchDone := make(chan struct{})
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "word0"})
		tool.Execute(context.Background(), in)
		close(searchDone)
	}()

	// Wait for the build to start and block.
	select {
	case <-buildStarted:
	case <-time.After(5 * time.Second):
		close(blockCh)
		t.Fatal("build did not start in time")
	}

	// Invalidate should return within 100ms while the build is blocked.
	start := time.Now()
	idx.Invalidate("file0.go")
	d1 := time.Since(start)
	if d1 > 100*time.Millisecond {
		t.Errorf("Invalidate(rel) took %v, want < 100ms", d1)
	}

	start = time.Now()
	idx.Invalidate("")
	d2 := time.Since(start)
	if d2 > 100*time.Millisecond {
		t.Errorf("Invalidate(\"\") took %v, want < 100ms", d2)
	}

	// Verify the build is still blocked.
	select {
	case <-searchDone:
		t.Error("build should still be blocked")
	default:
		// Good.
	}

	// Unblock and clean up.
	close(blockCh)
	<-searchDone
	idx.Close()

	// --- Part 2: Invalidate does not panic on a closed index ---
	idx2, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	idx2.Close()

	// These must not panic.
	idx2.Invalidate("x.go")
	idx2.Invalidate("")
}

// TS-03-41: After edit_file or write_file or a delete, code_search shows the
// new content and omits the old.
func TestFreshnessAfterWriteAndEdit_TS03_41(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n// oldToken unique123\nfunc A() {}\n")
	mkFile(t, root, "b.go", "package pkg\n// keepToken\nfunc B() {}\n")

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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Build the index with the initial content.
	in, _ := json.Marshal(map[string]any{"query": "oldToken"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "oldToken") {
		t.Error("initial search should find oldToken")
	}

	// Simulate edit_file: change oldToken to newToken and call Invalidate.
	os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package pkg\n// newToken unique123\nfunc A() {}\n"), 0o644)
	idx.Invalidate("a.go")

	// Search for newToken: should find it.
	in, _ = json.Marshal(map[string]any{"query": "newToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for newToken failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "newToken") {
		t.Error("after edit, code_search should find newToken")
	}

	// Search for oldToken: should NOT find it.
	in, _ = json.Marshal(map[string]any{"query": "oldToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for oldToken failed: %s: %s", r.Error, r.Detail)
	}
	if strings.Contains(r.Text, "oldToken") {
		t.Error("after edit, code_search should NOT find oldToken")
	}

	// Simulate write_file: create a new file and call Invalidate.
	os.WriteFile(filepath.Join(root, "new.go"),
		[]byte("package pkg\n// freshWord\nfunc New() {}\n"), 0o644)
	idx.Invalidate("new.go")

	in, _ = json.Marshal(map[string]any{"query": "freshWord"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for freshWord failed: %s: %s", r.Error, r.Detail)
	}
	paths := getResultFilePaths(r)
	found := false
	for _, p := range paths {
		if p == "new.go" {
			found = true
			break
		}
	}
	if !found {
		t.Error("newly written file new.go should appear in code_search results")
	}

	// Simulate delete: remove b.go and call Invalidate.
	os.Remove(filepath.Join(root, "b.go"))
	idx.Invalidate("b.go")

	in, _ = json.Marshal(map[string]any{"query": "keepToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for keepToken failed: %s: %s", r.Error, r.Detail)
	}
	for _, p := range getResultFilePaths(r) {
		if p == "b.go" {
			t.Error("deleted file b.go should not appear in code_search results")
		}
	}
}

// TS-03-42: Invalidate("") triggers a revalidation walk that sees a same-size
// edit made through execute within the 2 s window.
func TestRevalidationSeesSameSizeEdit_TS03_42(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "f.txt", "aaaa content here\n")
	mkFile(t, root, "gone.txt", "goneToken content\n")

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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Build the index.
	in, _ := json.Marshal(map[string]any{"query": "aaaa"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}

	// Simulate execute: same-size edit, delete, and create.
	os.WriteFile(filepath.Join(root, "f.txt"),
		[]byte("bbbb content here\n"), 0o644)
	os.Remove(filepath.Join(root, "gone.txt"))
	os.WriteFile(filepath.Join(root, "n.txt"),
		[]byte("newFileToken\n"), 0o644)

	// Invalidate("") triggers revalidation.
	idx.Invalidate("")

	// Search for bbbb: should find it (same-size edit within 2s window).
	in, _ = json.Marshal(map[string]any{"query": "bbbb"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for bbbb failed: %s: %s", r.Error, r.Detail)
	}
	paths := getResultFilePaths(r)
	foundF := false
	for _, p := range paths {
		if p == "f.txt" {
			foundF = true
		}
	}
	if !foundF {
		t.Error("same-size edit in f.txt should be found after revalidation")
	}

	// Search for goneToken: gone.txt should be absent.
	in, _ = json.Marshal(map[string]any{"query": "goneToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for goneToken failed: %s: %s", r.Error, r.Detail)
	}
	for _, p := range getResultFilePaths(r) {
		if p == "gone.txt" {
			t.Error("deleted file gone.txt should not appear after revalidation")
		}
	}

	// Search for newFileToken: n.txt should appear.
	in, _ = json.Marshal(map[string]any{"query": "newFileToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for newFileToken failed: %s: %s", r.Error, r.Detail)
	}
	foundN := false
	for _, p := range getResultFilePaths(r) {
		if p == "n.txt" {
			foundN = true
		}
	}
	if !foundN {
		t.Error("new file n.txt should appear after revalidation")
	}
}

// TS-03-43: A dirty path not in the index or a changed .gitignore forces a
// revalidation walk before answering.
func TestDirtyPathForcesRevalidation_TS03_43(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n// aToken\nfunc A() {}\n")
	mkFile(t, root, "b.go", "package pkg\n// bToken\nfunc B() {}\n")

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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Build the index.
	in, _ := json.Marshal(map[string]any{"query": "aToken"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}

	revalsBefore := idx.RevalCount()

	// Invalidate an unknown path (not in the index) — should force revalidation.
	os.WriteFile(filepath.Join(root, "unknown.go"),
		[]byte("package pkg\n// unknownToken\n"), 0o644)
	idx.Invalidate("unknown.go")

	in, _ = json.Marshal(map[string]any{"query": "unknownToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for unknownToken failed: %s: %s", r.Error, r.Detail)
	}

	revalsAfterUnknown := idx.RevalCount()
	if revalsAfterUnknown <= revalsBefore {
		t.Errorf("revalidation count should increase for unknown path: before=%d after=%d",
			revalsBefore, revalsAfterUnknown)
	}

	// Invalidate a known non-.gitignore path — should NOT force revalidation.
	os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package pkg\n// aTokenUpdated\nfunc A() {}\n"), 0o644)
	idx.Invalidate("a.go")

	revalsBefore2 := idx.RevalCount()
	in, _ = json.Marshal(map[string]any{"query": "aTokenUpdated"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for aTokenUpdated failed: %s: %s", r.Error, r.Detail)
	}

	revalsAfterKnown := idx.RevalCount()
	if revalsAfterKnown != revalsBefore2 {
		t.Errorf("revalidation count should NOT increase for known non-.gitignore path: before=%d after=%d",
			revalsBefore2, revalsAfterKnown)
	}

	// Invalidate a .gitignore change — should force revalidation.
	os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte("b.go\n"), 0o644)
	idx.Invalidate(".gitignore")

	revalsBefore3 := idx.RevalCount()
	in, _ = json.Marshal(map[string]any{"query": "bToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for bToken failed: %s: %s", r.Error, r.Detail)
	}

	revalsAfterGitignore := idx.RevalCount()
	if revalsAfterGitignore <= revalsBefore3 {
		t.Errorf("revalidation count should increase for .gitignore change: before=%d after=%d",
			revalsBefore3, revalsAfterGitignore)
	}

	// After .gitignore ignores b.go, it should not appear in results.
	for _, p := range getResultFilePaths(r) {
		if p == "b.go" {
			t.Error("b.go should be ignored after .gitignore change")
		}
	}
}

// TS-03-47: A dirty mark made while a build or revalidation runs is not lost.
func TestDirtyMarkDuringBuildSurvives_TS03_47(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n// originalContent\nfunc A() {}\n")
	mkFile(t, root, "b.go", "package pkg\n// bContent\nfunc B() {}\n")

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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Step 1: Build the index with original content.
	in, _ := json.Marshal(map[string]any{"query": "originalContent"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("initial search failed: %s: %s", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "originalContent") {
		t.Fatal("initial search should find originalContent")
	}

	// Step 2: Now set up a blocking hook for the NEXT build (triggered by
	// the 5% rebuild threshold or a revalidation). We simulate this by
	// calling Invalidate("") which triggers revalidation on the next query.
	// During the revalidation walk, we write a.go with midBuildToken and
	// call Invalidate("a.go"). The mark must survive the revalidation pass.
	buildBlocked := make(chan struct{}, 1)
	releaseBuild := make(chan struct{})
	markDone := make(chan struct{})

	// Install a test hook on the revalidation walk that blocks and lets us
	// inject a concurrent write.
	idx.testRevalHook = func() {
		select {
		case buildBlocked <- struct{}{}:
		default:
		}
		<-releaseBuild
	}

	// Trigger revalidation.
	idx.Invalidate("")

	// Start a search that will trigger the revalidation.
	searchDone := make(chan struct{})
	var searchResult core.ToolResult
	go func() {
		in, _ := json.Marshal(map[string]any{"query": "bContent"})
		searchResult = tool.Execute(ctx, in)
		close(searchDone)
	}()

	// Wait for the revalidation to block.
	select {
	case <-buildBlocked:
	case <-time.After(5 * time.Second):
		close(releaseBuild)
		t.Fatal("revalidation did not block in time")
	}

	// While the revalidation is running, write a.go with new content.
	os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package pkg\n// midBuildToken\nfunc A() {}\n"), 0o644)
	idx.Invalidate("a.go")
	close(markDone)

	// Release the revalidation.
	close(releaseBuild)
	<-searchDone
	_ = searchResult

	// Clear the hook.
	idx.testRevalHook = nil

	// The mark made during the revalidation pass must survive.
	// Search for midBuildToken: should find it.
	in, _ = json.Marshal(map[string]any{"query": "midBuildToken"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("search for midBuildToken failed: %s: %s", r.Error, r.Detail)
	}
	paths := getResultFilePaths(r)
	found := false
	for _, p := range paths {
		if p == "a.go" {
			found = true
		}
	}
	_ = markDone
	if !found {
		t.Error("a.go with midBuildToken should appear — mark made during revalidation must survive")
	}
}

// TS-03-48: With no Invalidate call the index builds once and never
// revalidates or rebuilds.
func TestNoInvalidateNeverRebuilds_TS03_48(t *testing.T) {
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
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Run code_search 20 times with no Invalidate.
	for i := 0; i < 20; i++ {
		in, _ := json.Marshal(map[string]any{"query": "searchword"})
		r := tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("search %d failed: %s: %s", i, r.Error, r.Detail)
		}
	}

	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count = %d, want 1", bc)
	}
	if rc := idx.RevalCount(); rc != 0 {
		t.Errorf("revalidation count = %d, want 0", rc)
	}
}
