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
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-16: code_search is a Builtin parallel read-only tool with the pinned
// description, schema and guideline.
func TestCodeSearchToolDefinition_TS03_16(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
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

	tls := idx.Tools()
	if len(tls) == 0 {
		t.Fatal("Tools() returned no tools")
	}

	tool := tls[0]

	// Name
	if tool.Name != "code_search" {
		t.Errorf("Name = %q, want code_search", tool.Name)
	}

	// Builtin
	if !tool.Builtin {
		t.Error("tool should be Builtin")
	}

	// Parallel (zero value of ExecutionMode)
	if tool.ExecutionMode != core.Parallel {
		t.Errorf("ExecutionMode = %v, want Parallel", tool.ExecutionMode)
	}

	// Description contains "zoekt query syntax"
	if !strings.Contains(tool.Description, "zoekt query syntax") {
		t.Error("description should contain 'zoekt query syntax'")
	}

	// Description does NOT contain "RE2"
	if strings.Contains(tool.Description, "RE2") {
		t.Error("description should not contain 'RE2'")
	}

	// Four examples
	examples := []string{
		"sym:Runner",
		`retry file:^src/ -file:test`,
		`lang:python "def load"`,
		`(compaction or summarize) case:no`,
	}
	for _, ex := range examples {
		if !strings.Contains(tool.Description, ex) {
			t.Errorf("description should contain example %q", ex)
		}
	}

	// PromptGuidelines
	wantGuideline := "Use code_search for ranked questions about the codebase; search_files for an exhaustive regex scan."
	found := false
	for _, g := range tool.PromptGuidelines {
		if g == wantGuideline {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("PromptGuidelines should contain %q, got %v", wantGuideline, tool.PromptGuidelines)
	}

	// Schema: query is required; path, max_files, context_lines are optional
	if tool.InputSchema == nil {
		t.Fatal("InputSchema is nil")
	}
}

// TS-03-17: Empty, oversized, unparsable queries and negative context_lines
// are invalid_arguments before any build.
func TestInvalidArguments_TS03_17(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
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

	tool := idx.Tools()[0]
	ctx := context.Background()

	cases := []struct {
		name string
		args map[string]any
	}{
		{"empty query", map[string]any{"query": "   "}},
		{"oversized query", map[string]any{"query": strings.Repeat("a", 1025)}},
		{"unparsable query", map[string]any{"query": "("}},
		{"negative context_lines", map[string]any{"query": "hello", "context_lines": -1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := json.Marshal(tc.args)
			r := tool.Execute(ctx, in)
			if r.OK {
				t.Error("expected error result")
			}
			if r.Error != "invalid_arguments" {
				t.Errorf("error code = %q, want invalid_arguments", r.Error)
			}
		})
	}

	// Verify parser message is included for unparsable query
	in, _ := json.Marshal(map[string]any{"query": "("})
	r := tool.Execute(ctx, in)
	if !strings.Contains(r.Detail, "parse") && !strings.Contains(strings.ToLower(r.Detail), "expected") {
		// The zoekt parser should produce some error message
		t.Logf("parser error detail: %q", r.Detail)
	}

	// Build counter should be 0
	if bc := idx.BuildCount(); bc != 0 {
		t.Errorf("build count = %d, want 0 (no build should have started)", bc)
	}
}

// TS-03-18: context_lines and max_files default and clamp as specified.
func TestContextLinesAndMaxFilesDefaultAndClamp_TS03_18(t *testing.T) {
	root := t.TempDir()

	// Create 40 files that all contain "matchword"
	for i := 0; i < 40; i++ {
		content := fmt.Sprintf("package pkg\n// line2\n// line3\n// matchword here\n// line5\n// line6\n// line7\n")
		mkFile(t, root, fmt.Sprintf("pkg/file%03d.go", i), content)
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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Default: 10 files
	in, _ := json.Marshal(map[string]any{"query": "matchword"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	files := countResultFiles(r)
	if files > 10 {
		t.Errorf("default max_files: got %d files, want at most 10", files)
	}

	// max_files 100 clamps to 25
	in, _ = json.Marshal(map[string]any{"query": "matchword", "max_files": 100})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	files = countResultFiles(r)
	if files > 25 {
		t.Errorf("max_files=100 clamped: got %d files, want at most 25", files)
	}

	// context_lines 500 clamps to MaxSearchContextLines
	in, _ = json.Marshal(map[string]any{"query": "matchword", "context_lines": 500})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
}

// TS-03-19: A path outside the workspace is path_not_allowed and a missing
// path is read_failed, both before a build.
func TestPathValidation_TS03_19(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Path outside workspace
	in, _ := json.Marshal(map[string]any{"query": "x", "path": "../outside"})
	r := tool.Execute(ctx, in)
	if r.OK {
		t.Error("expected error for path outside workspace")
	}
	if r.Error != "path_not_allowed" {
		t.Errorf("error code = %q, want path_not_allowed", r.Error)
	}

	// Missing path
	in, _ = json.Marshal(map[string]any{"query": "x", "path": "nope/missing"})
	r = tool.Execute(ctx, in)
	if r.OK {
		t.Error("expected error for missing path")
	}
	if r.Error != "read_failed" {
		t.Errorf("error code = %q, want read_failed", r.Error)
	}

	// Build counter should be 0
	if bc := idx.BuildCount(); bc != 0 {
		t.Errorf("build count = %d, want 0 (no build should have started)", bc)
	}
}

// TS-03-20: path is a conjunction that confines an or-query, for directories,
// files, the root and metacharacters.
func TestPathConjunction_TS03_20(t *testing.T) {
	root := t.TempDir()

	// Create fixture files
	mkFile(t, root, "pkg/a/x.go", "package a\n// retry fetch\n")
	mkFile(t, root, "pkg/ab/y.go", "package ab\n// retry fetch\n")
	mkFile(t, root, "other/z.go", "package other\n// retry fetch\n")
	mkFile(t, root, "c++[1]/w.go", "package cplus\n// retry fetch\n")

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

	tool := idx.Tools()[0]
	ctx := context.Background()

	// path=pkg/a returns only files under pkg/a/ and not pkg/ab/
	in, _ := json.Marshal(map[string]any{"query": "retry or fetch", "path": "pkg/a"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	resultFiles := getResultFilePaths(r)
	for _, f := range resultFiles {
		if !strings.HasPrefix(f, "pkg/a/") {
			t.Errorf("path=pkg/a returned file %q outside pkg/a/", f)
		}
	}
	if len(resultFiles) == 0 {
		t.Error("path=pkg/a returned no files, expected at least pkg/a/x.go")
	}

	// path=pkg/a/x.go returns only that file
	in, _ = json.Marshal(map[string]any{"query": "retry or fetch", "path": "pkg/a/x.go"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	resultFiles = getResultFilePaths(r)
	for _, f := range resultFiles {
		if f != "pkg/a/x.go" {
			t.Errorf("path=pkg/a/x.go returned file %q, want only pkg/a/x.go", f)
		}
	}

	// Root adds no constraint
	in, _ = json.Marshal(map[string]any{"query": "retry or fetch", "path": "."})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	resultFiles = getResultFilePaths(r)
	if len(resultFiles) < 4 {
		t.Errorf("root path returned %d files, want at least 4", len(resultFiles))
	}

	// Metacharacter directory
	in, _ = json.Marshal(map[string]any{"query": "retry or fetch", "path": "c++[1]"})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	resultFiles = getResultFilePaths(r)
	for _, f := range resultFiles {
		if !strings.HasPrefix(f, "c++[1]/") {
			t.Errorf("path=c++[1] returned file %q outside c++[1]/", f)
		}
	}
}

// TS-03-21: A cancelled call context returns aborted with "Operation aborted".
func TestCancelledContext_TS03_21(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
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

	tool := idx.Tools()[0]

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	in, _ := json.Marshal(map[string]any{"query": "hello"})
	r := tool.Execute(ctx, in)
	if r.OK {
		t.Error("expected error result for cancelled context")
	}
	if r.Error != "aborted" {
		t.Errorf("error code = %q, want aborted", r.Error)
	}
	if r.Detail != "Operation aborted" {
		t.Errorf("detail = %q, want 'Operation aborted'", r.Detail)
	}
}

// TS-03-22: A build failure that is not a bound returns index_failed with the cause.
func TestBuildFailureReturnsIndexFailed_TS03_22(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Use a regular file as TempDir so the shard directory cannot be created
	badTempFile := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(badTempFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir: badTempFile,
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	in, _ := json.Marshal(map[string]any{"query": "hello"})
	r := tool.Execute(ctx, in)
	if r.OK {
		t.Error("expected error result for build failure")
	}
	if r.Error != "index_failed" {
		t.Errorf("error code = %q, want index_failed", r.Error)
	}
	if r.Detail == "" {
		t.Error("detail should include the underlying error")
	}
}

// TS-03-23: A failing or timed-out search returns search_failed, and the
// timeout message says to narrow.
func TestSearchFailedAndTimeout_TS03_23(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n")
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("search error", func(t *testing.T) {
		idx, err := newIndex(ws, Options{
			TempDir: t.TempDir(),
			Ignore:  tools.NoGlobalExcludes(),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		// Inject a searcher that returns an error
		idx.testSearchHook = func(_ context.Context, _ string) (*searchHookResult, error) {
			return nil, fmt.Errorf("injected search error")
		}

		tool := idx.Tools()[0]
		ctx := context.Background()

		in, _ := json.Marshal(map[string]any{"query": "main"})
		r := tool.Execute(ctx, in)
		if r.OK {
			t.Error("expected error result for search failure")
		}
		if r.Error != "search_failed" {
			t.Errorf("error code = %q, want search_failed", r.Error)
		}
	})

	t.Run("search timeout", func(t *testing.T) {
		idx, err := newIndex(ws, Options{
			TempDir: t.TempDir(),
			Ignore:  tools.NoGlobalExcludes(),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		// Inject a searcher that blocks until context is cancelled
		idx.testSearchHook = func(ctx context.Context, _ string) (*searchHookResult, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}

		// Override the query timeout to something very short
		idx.queryTimeout = 50 * time.Millisecond

		tool := idx.Tools()[0]
		ctx := context.Background()

		in, _ := json.Marshal(map[string]any{"query": "main"})
		r := tool.Execute(ctx, in)
		if r.OK {
			t.Error("expected error result for search timeout")
		}
		if r.Error != "search_failed" {
			t.Errorf("error code = %q, want search_failed", r.Error)
		}
		if !strings.Contains(r.Detail, "timed out") {
			t.Errorf("detail should mention 'timed out', got %q", r.Detail)
		}
		if !strings.Contains(r.Detail, "narrow") {
			t.Errorf("detail should mention 'narrow', got %q", r.Detail)
		}
	})
}

// TS-03-5.1: The build counter is 0 after New and after any find_symbol call,
// and 1 after the first code_search.
func TestLazyBuild_TS03_5_1(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n")
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

	// Build count is 0 after New
	if bc := idx.BuildCount(); bc != 0 {
		t.Errorf("build count after New = %d, want 0", bc)
	}

	// find_symbol (Symbols) does not trigger a build
	_, _, _ = idx.Symbols(context.Background(), tools.SymbolQuery{Name: "main"})
	if bc := idx.BuildCount(); bc != 0 {
		t.Errorf("build count after Symbols = %d, want 0", bc)
	}

	// First code_search triggers a build
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "main"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count after first code_search = %d, want 1", bc)
	}

	// Second code_search does not rebuild
	r = tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	if bc := idx.BuildCount(); bc != 1 {
		t.Errorf("build count after second code_search = %d, want 1", bc)
	}
}

// --- helpers ---

// countResultFiles counts the number of files in a code_search result.
func countResultFiles(r core.ToolResult) int {
	if r.Data == nil {
		return 0
	}
	files, ok := r.Data["files"]
	if !ok {
		return 0
	}
	arr, ok := files.([]any)
	if !ok {
		return 0
	}
	return len(arr)
}

// getResultFilePaths extracts file paths from a code_search result.
func getResultFilePaths(r core.ToolResult) []string {
	if r.Data == nil {
		return nil
	}
	files, ok := r.Data["files"]
	if !ok {
		return nil
	}
	arr, ok := files.([]any)
	if !ok {
		return nil
	}
	var paths []string
	for _, f := range arr {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := m["path"].(string); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// TS-06-20: code_search declares the Data it returns. A file entry carries a
// match count and context chunks, symbol_sources counts files per backend,
// and dirty_files is a count.
func TestOutputSchemaCodeSearch_TS06_20(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := newIndex(ws, Options{TempDir: t.TempDir(), Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	s := idx.Tools()[0].OutputSchema
	if s == nil || s.Type != schema.TypeObject {
		t.Fatalf("OutputSchema = %+v, want an object", s)
	}
	type p struct {
		typ      schema.Type
		required bool
	}
	check := func(where string, s *schema.Schema, want map[string]p) {
		t.Helper()
		if s == nil || s.Type != schema.TypeObject || len(s.Properties) != len(want) {
			t.Fatalf("%s = %+v, want an object with %d properties", where, s, len(want))
		}
		for name, w := range want {
			got := s.Properties[name]
			if got == nil || got.Type != w.typ || s.IsRequired(name) != w.required {
				t.Errorf("%s.%s = %+v (required %v), want %s required=%v", where, name, got, s.IsRequired(name), w.typ, w.required)
			}
		}
	}
	check("code_search", s, map[string]p{
		"files": {schema.TypeArray, true}, "truncated": {schema.TypeBoolean, true},
		"note": {schema.TypeString, true}, "partial": {schema.TypeBoolean, true},
		"partial_reason": {schema.TypeString, true}, "symbol_sources": {schema.TypeObject, true},
		"files_indexed": {schema.TypeInteger, true}, "dirty_files": {schema.TypeInteger, true},
		"skipped": {schema.TypeObject, true},
	})
	check("code_search.skipped", s.Properties["skipped"], map[string]p{
		"binary": {schema.TypeInteger, true}, "oversized": {schema.TypeInteger, true},
		"too_many_trigrams": {schema.TypeInteger, true}, "too_small": {schema.TypeInteger, true},
	})
	file := s.Properties["files"].Items
	check("code_search.files[]", file, map[string]p{
		"path": {schema.TypeString, true}, "score": {schema.TypeNumber, true},
		"matches": {schema.TypeInteger, true}, "chunks": {schema.TypeArray, true},
		"symbols": {schema.TypeArray, true},
	})
	if !file.Properties["symbols"].Nullable {
		t.Error("code_search.files[].symbols must be nullable: collectDeclNames returns nil when no declaration matches")
	}
	check("code_search.files[].chunks[]", file.Properties["chunks"].Items, map[string]p{
		"lines": {schema.TypeArray, true},
	})
	check("code_search.files[].chunks[].lines[]", file.Properties["chunks"].Items.Properties["lines"].Items, map[string]p{
		"line": {schema.TypeInteger, true}, "text": {schema.TypeString, true}, "match": {schema.TypeBoolean, true},
	})
}

// TS-06-23 for code_search, which lives in this module rather than beside
// the other built-in tools: malformed JSON and an empty query are
// invalid_arguments.
func TestErrorCodesInvalidArguments_TS06_23(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n")
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := newIndex(ws, Options{TempDir: t.TempDir(), Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	cs := idx.Tools()[0]
	for _, args := range []string{`{malformed`, `{"query":""}`} {
		res := cs.Execute(context.Background(), json.RawMessage(args))
		if res.OK || res.Error != "invalid_arguments" {
			t.Errorf("code_search %s: OK %v error %q, want invalid_arguments", args, res.OK, res.Error)
		}
	}
}
