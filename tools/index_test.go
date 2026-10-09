package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/agentfox/agentkit-go/core"
)

// fakeIndex is a test double for tools.Index that records Invalidate calls.
type fakeIndex struct {
	mu    sync.Mutex
	calls []string
	tools []core.Tool

	// closed tracks whether Close has been called.
	closed bool
}

func (f *fakeIndex) Symbols(_ context.Context, _ SymbolQuery) (SymbolAnswer, bool, error) {
	return SymbolAnswer{}, false, nil
}

func (f *fakeIndex) Tools() []core.Tool {
	return f.tools
}

func (f *fakeIndex) Invalidate(rel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rel)
}

func (f *fakeIndex) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeIndex) getCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeIndex) resetCalls() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// TS-03-8: The seam types exist in tools, compile against a fake index and
// import only the standard library and root packages.
func TestSeamTypesCompile_TS03_8(t *testing.T) {
	// Verify the types compile and the zero value of Options.Index is nil.
	var o Options
	if o.Index != nil {
		t.Fatal("zero-value Options.Index must be nil")
	}

	// Verify a fake can be assigned to Options.Index.
	f := &fakeIndex{}
	var _ Index = f
	o.Index = f
	if o.Index == nil {
		t.Fatal("Options.Index should be set")
	}

	// Verify SymbolQuery fields.
	q := SymbolQuery{Name: "Hello", Kind: "func", Path: "pkg/", Exact: true}
	if q.Name != "Hello" || q.Kind != "func" || q.Path != "pkg/" || !q.Exact {
		t.Fatal("SymbolQuery fields not set correctly")
	}

	// Verify SymbolAnswer fields.
	a := SymbolAnswer{
		Matches:      []SymbolMatch{{Name: "Hello", Kind: "func"}},
		FilesIndexed: 42,
	}
	if len(a.Matches) != 1 || a.FilesIndexed != 42 {
		t.Fatal("SymbolAnswer fields not set correctly")
	}
}

// TS-03-9: CapMarker and ClampLimit return exactly what the unexported helpers
// return for the same arguments.
func TestCapMarkerAndClampLimit_TS03_9(t *testing.T) {
	// CapMarker tests: below, equal to and above max.
	capTests := []struct {
		noun, param string
		limit, max  int
		alt         string
	}{
		{"results", "limit", 10, 100, "narrow the query"},
		{"results", "limit", 100, 100, "narrow the query"},
		{"results", "limit", 200, 100, "narrow the query"},
		{"files", "max_files", 5, 25, "narrow the query or add a path"},
		{"entries", "limit", 50, 500, "list a subdirectory"},
		{"matches", "max_matches", 100, 100, "refine the pattern"},
	}
	for _, tc := range capTests {
		got := CapMarker(tc.noun, tc.param, tc.limit, tc.max, tc.alt)
		want := capMarker(tc.noun, tc.param, tc.limit, tc.max, tc.alt)
		if got != want {
			t.Errorf("CapMarker(%q,%q,%d,%d,%q):\n  got:  %s\n  want: %s",
				tc.noun, tc.param, tc.limit, tc.max, tc.alt, got, want)
		}
	}

	// At limit==max the at-the-cap form must be produced.
	atCap := CapMarker("results", "limit", 100, 100, "narrow the query")
	if !strings.Contains(atCap, "which is the maximum") {
		t.Errorf("at-the-cap form not produced: %s", atCap)
	}

	// ClampLimit tests: zero, negative, within range, above cap.
	clampTests := []struct {
		n, def, cap int
		want        int
	}{
		{0, 10, 100, 10},
		{-5, 10, 100, 10},
		{5, 10, 100, 5},
		{10, 10, 100, 10},
		{50, 10, 100, 50},
		{100, 10, 100, 100},
		{200, 10, 100, 100},
	}
	for _, tc := range clampTests {
		got := ClampLimit(tc.n, tc.def, tc.cap)
		want := clampLimit(tc.n, tc.def, tc.cap)
		if got != want {
			t.Errorf("ClampLimit(%d,%d,%d): got %d, want %d (from clampLimit: %d)",
				tc.n, tc.def, tc.cap, got, want, want)
		}
		if got != tc.want {
			t.Errorf("ClampLimit(%d,%d,%d): got %d, want %d",
				tc.n, tc.def, tc.cap, got, tc.want)
		}
	}
}

// TS-03-10: With an index set All appends the index tools after the built-ins
// and FileNavigationTools is unchanged.
func TestAllAppendsIndexTools_TS03_10(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	csTool := core.Tool{Name: "code_search", Description: "test", Builtin: true}
	idx := &fakeIndex{tools: []core.Tool{csTool}}

	withIdx, err := All(Options{
		Workspace: ws,
		Index:     idx,
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}

	baseTools, err := All(Options{
		Workspace: ws,
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The first len(baseTools) tools must match the built-in list.
	if len(withIdx) != len(baseTools)+1 {
		t.Fatalf("expected %d tools, got %d", len(baseTools)+1, len(withIdx))
	}
	for i, bt := range baseTools {
		if withIdx[i].Name != bt.Name {
			t.Errorf("tool %d: got %q, want %q", i, withIdx[i].Name, bt.Name)
		}
	}

	// The last tool must be code_search.
	last := withIdx[len(withIdx)-1]
	if last.Name != "code_search" {
		t.Errorf("last tool: got %q, want %q", last.Name, "code_search")
	}

	// FileNavigationTools must not contain code_search.
	for _, name := range FileNavigationTools() {
		if name == "code_search" {
			t.Error("code_search must not be in FileNavigationTools()")
		}
	}
}

// TS-03-11: With Options.Index nil All returns the tool list pinned by spec 02.
func TestAllNilIndexPinnedList_TS03_11(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	all, err := All(Options{
		Workspace: ws,
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The pinned order from spec 02 / 05.
	wantNames := []string{
		"read_file", "write_file", "edit_file",
		"list_files", "find_files", "search_files",
		"file_outline", "find_symbol", "find_references",
		"execute", "run_command",
	}
	gotNames := make([]string, len(all))
	for i, tl := range all {
		gotNames[i] = tl.Name
	}
	if strings.Join(gotNames, ",") != strings.Join(wantNames, ",") {
		t.Fatalf("All() with nil Index:\ngot:  %v\nwant: %v", gotNames, wantNames)
	}
}

// TS-03-12: An index tool named like a built-in makes All return an error
// naming it.
func TestAllDuplicateToolNameError_TS03_12(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	dup := core.Tool{Name: "search_files", Description: "dup"}
	idx := &fakeIndex{tools: []core.Tool{dup}}

	ts, err := All(Options{
		Workspace: ws,
		Index:     idx,
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	})
	if ts != nil {
		t.Fatal("expected nil tool list on duplicate")
	}
	if err == nil {
		t.Fatal("expected error on duplicate tool name")
	}
	if !strings.Contains(err.Error(), "search_files") {
		t.Errorf("error should name the duplicate tool: %v", err)
	}
}

// TS-03-13: write_file and edit_file call Invalidate exactly once per attempt
// with the relative slash path, on success and failure.
func TestWriteAndEditCallInvalidate_TS03_13(t *testing.T) {
	root := t.TempDir()
	// Create a file for editing.
	mkSymFile(t, root, "sub/dir/a.txt", "hello world\n")

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx := &fakeIndex{}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
		Index:     idx,
	}.withDefaults())

	// --- write_file success ---
	wt := ft.writeFile()
	args, _ := json.Marshal(map[string]any{
		"path":    "sub/dir/a.txt",
		"content": "new content\n",
	})
	r := wt.Execute(context.Background(), args)
	if !r.OK {
		t.Fatalf("write_file success: error=%s detail=%s", r.Error, r.Detail)
	}
	calls := idx.getCalls()
	if len(calls) != 1 || calls[0] != "sub/dir/a.txt" {
		t.Fatalf("write_file success: Invalidate calls = %v, want [sub/dir/a.txt]", calls)
	}
	idx.resetCalls()

	// --- edit_file success ---
	et := ft.editFile()
	args, _ = json.Marshal(map[string]any{
		"path": "sub/dir/a.txt",
		"edits": []map[string]string{
			{"old_string": "new content", "new_string": "edited content"},
		},
	})
	r = et.Execute(context.Background(), args)
	if !r.OK {
		t.Fatalf("edit_file success: error=%s detail=%s", r.Error, r.Detail)
	}
	calls = idx.getCalls()
	if len(calls) != 1 || calls[0] != "sub/dir/a.txt" {
		t.Fatalf("edit_file success: Invalidate calls = %v, want [sub/dir/a.txt]", calls)
	}
	idx.resetCalls()

	// --- edit_file failure (old_string absent) ---
	args, _ = json.Marshal(map[string]any{
		"path": "sub/dir/a.txt",
		"edits": []map[string]string{
			{"old_string": "NONEXISTENT_TEXT_12345", "new_string": "replacement"},
		},
	})
	r = et.Execute(context.Background(), args)
	if r.OK {
		t.Fatal("edit_file with non-matching text: expected failure")
	}
	calls = idx.getCalls()
	if len(calls) != 1 || calls[0] != "sub/dir/a.txt" {
		t.Fatalf("edit_file failure: Invalidate calls = %v, want [sub/dir/a.txt]", calls)
	}
	idx.resetCalls()

	// --- write_file failure (read-only directory) ---
	roDir := filepath.Join(root, "readonly")
	if err := os.MkdirAll(roDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roDir, "existing.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Skip("cannot set read-only directory permissions")
	}
	defer os.Chmod(roDir, 0o755) //nolint:errcheck

	args, _ = json.Marshal(map[string]any{
		"path":    "readonly/newfile.txt",
		"content": "fail\n",
	})
	r = wt.Execute(context.Background(), args)
	// Whether it fails depends on OS, but Invalidate must be called.
	calls = idx.getCalls()
	if len(calls) != 1 || calls[0] != "readonly/newfile.txt" {
		t.Fatalf("write_file failure: Invalidate calls = %v, want [readonly/newfile.txt]", calls)
	}
}

// TS-03-14: execute, run_command and powershell call Invalidate with the empty
// string once per invocation whatever the outcome.
func TestShellToolsCallInvalidate_TS03_14(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx := &fakeIndex{}

	// Use All() to get the real wired tools, so we test the actual wiring.
	all, err := All(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
		Index:     idx,
	})
	if err != nil {
		t.Fatal(err)
	}

	toolMap := make(map[string]core.Tool)
	for _, tl := range all {
		toolMap[tl.Name] = tl
	}

	execTool := toolMap["execute"]
	rcTool := toolMap["run_command"]

	// --- execute success ---
	args, _ := json.Marshal(map[string]any{
		"command":   "echo hello",
		"timeout_s": 5,
	})
	execTool.Execute(context.Background(), args)
	calls := idx.getCalls()
	if len(calls) != 1 || calls[0] != "" {
		t.Fatalf("execute success: Invalidate calls = %v, want [\"\"]", calls)
	}
	idx.resetCalls()

	// --- execute failure ---
	args, _ = json.Marshal(map[string]any{
		"command":   "false",
		"timeout_s": 5,
	})
	execTool.Execute(context.Background(), args)
	calls = idx.getCalls()
	if len(calls) != 1 || calls[0] != "" {
		t.Fatalf("execute failure: Invalidate calls = %v, want [\"\"]", calls)
	}
	idx.resetCalls()

	// --- run_command success ---
	args, _ = json.Marshal(map[string]any{
		"argv":      []string{"echo", "hello"},
		"timeout_s": 5,
	})
	rcTool.Execute(context.Background(), args)
	calls = idx.getCalls()
	if len(calls) != 1 || calls[0] != "" {
		t.Fatalf("run_command success: Invalidate calls = %v, want [\"\"]", calls)
	}
	idx.resetCalls()

	// --- run_command failure ---
	args, _ = json.Marshal(map[string]any{
		"argv":      []string{"false"},
		"timeout_s": 5,
	})
	rcTool.Execute(context.Background(), args)
	calls = idx.getCalls()
	if len(calls) != 1 || calls[0] != "" {
		t.Fatalf("run_command failure: Invalidate calls = %v, want [\"\"]", calls)
	}
}
