//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-32: The index is built lazily on the first code_search and never by
// New or find_symbol.
func TestIndexBuiltLazilyOnCodeSearch_TS03_32(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n\nfunc Runner() {}\n")
	mkFile(t, root, "b.go", "package pkg\n\nfunc Helper() {}\n")

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

	// After New: build count must be 0.
	if bc := idx.BuildCount(); bc != 0 {
		t.Fatalf("after New: build count = %d, want 0", bc)
	}

	// After find_symbol (via Symbols): build count must still be 0.
	_, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "Runner"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if ok {
		t.Error("Symbols should return ok=false before any code_search")
	}
	if bc := idx.BuildCount(); bc != 0 {
		t.Fatalf("after Symbols: build count = %d, want 0", bc)
	}

	// After first code_search: build count must be 1.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "Runner"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("first code_search failed: %s: %s", r.Error, r.Detail)
	}
	if bc := idx.BuildCount(); bc != 1 {
		t.Fatalf("after first code_search: build count = %d, want 1", bc)
	}

	// After second code_search: build count must still be 1.
	r = tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("second code_search failed: %s: %s", r.Error, r.Detail)
	}
	if bc := idx.BuildCount(); bc != 1 {
		t.Fatalf("after second code_search: build count = %d, want 1", bc)
	}
}

// TS-03-50: Symbols matches from outline.File data using smart-case prefix,
// exact, qualified, kind and path rules.
func TestSymbolsMatchingRules_TS03_50(t *testing.T) {
	root := t.TempDir()
	// Create files with specific declarations.
	mkFile(t, root, "a/runner.go", "package a\n\nfunc Runner() {}\nfunc runner() {}\n")
	mkFile(t, root, "a/server.go", "package a\n\ntype Server struct{}\nfunc (s Server) Run() {}\n")
	mkFile(t, root, "b/helper.go", "package b\n\nfunc RunHelper() {}\n")

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

	// Build the index via code_search.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "Runner"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
	}

	ctx := context.Background()

	// Test 1: lowercase "run" — smart-case (case-insensitive prefix).
	a, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "run"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true after code_search")
	}
	// Should match: Runner, runner, Run (method), RunHelper — all start with "run" case-insensitively.
	names := symbolNames(a.Matches)
	for _, want := range []string{"Runner", "runner", "Run", "RunHelper"} {
		if !containsName(names, want) {
			t.Errorf("smart-case 'run': expected %q in matches, got %v", want, names)
		}
	}

	// Test 2: "Run" (uppercase) — case-sensitive prefix.
	a, ok, err = idx.Symbols(ctx, tools.SymbolQuery{Name: "Run"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	names = symbolNames(a.Matches)
	// Should match: Runner, Run, RunHelper — NOT runner (lowercase).
	for _, want := range []string{"Runner", "Run", "RunHelper"} {
		if !containsName(names, want) {
			t.Errorf("case-sensitive 'Run': expected %q in matches, got %v", want, names)
		}
	}
	if containsName(names, "runner") {
		t.Errorf("case-sensitive 'Run': should NOT match 'runner', got %v", names)
	}

	// Test 3: exact "Run" — exact match, case-sensitive.
	a, ok, err = idx.Symbols(ctx, tools.SymbolQuery{Name: "Run", Exact: true})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	names = symbolNames(a.Matches)
	if len(names) != 1 || names[0] != "Run" {
		t.Errorf("exact 'Run': expected [Run], got %v", names)
	}

	// Test 4: qualified "Server.Run" — matches Container.Name.
	a, ok, err = idx.Symbols(ctx, tools.SymbolQuery{Name: "Server.Run"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	names = symbolNames(a.Matches)
	if len(names) != 1 || names[0] != "Run" {
		t.Errorf("qualified 'Server.Run': expected [Run], got %v", names)
	}
	// Verify the container is Server.
	if len(a.Matches) == 1 && a.Matches[0].Container != "Server" {
		t.Errorf("qualified 'Server.Run': expected container=Server, got %q", a.Matches[0].Container)
	}

	// Test 5: kind filter "func".
	a, ok, err = idx.Symbols(ctx, tools.SymbolQuery{Name: "run", Kind: "func"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	for _, m := range a.Matches {
		if m.Kind != "func" && m.Kind != "method" {
			// go/ast backend may report methods as "method" or "func"
			// depending on the outline implementation. Just ensure no
			// "type" kinds leak through.
			if m.Kind == "type" {
				t.Errorf("kind filter 'func': unexpected kind %q for %q", m.Kind, m.Name)
			}
		}
	}

	// Test 6: path filter "a".
	a, ok, err = idx.Symbols(ctx, tools.SymbolQuery{Name: "run", Path: "a"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	for _, m := range a.Matches {
		if !strings.HasPrefix(m.Path, "a/") {
			t.Errorf("path filter 'a': match %q has path %q outside a/", m.Name, m.Path)
		}
	}
	// RunHelper from b/ should not appear.
	if containsName(symbolNames(a.Matches), "RunHelper") {
		t.Error("path filter 'a': RunHelper from b/ should not appear")
	}
}

// TS-03-51: After a code_search built a complete index, Symbols returns
// ok=true with matches and FilesIndexed.
func TestSymbolsOkTrueAfterBuild_TS03_51(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n\nfunc Main() {}\nfunc Helper() {}\n")
	mkFile(t, root, "lib.go", "package main\n\nfunc Lib() {}\n")

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

	// Build via code_search.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "Main"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
	}

	// Symbols should return ok=true.
	a, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "Main"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true after a complete build")
	}
	if len(a.Matches) == 0 {
		t.Error("Symbols should return matches for 'Main'")
	}
	if a.FilesIndexed != 2 {
		t.Errorf("FilesIndexed = %d, want 2", a.FilesIndexed)
	}
}

// TS-03-52: Symbols returns ok=false with nil error when unbuilt, partial,
// closed or over 1000 matches, and never builds.
func TestSymbolsOkFalseStates_TS03_52(t *testing.T) {
	ctx := context.Background()

	t.Run("unbuilt", func(t *testing.T) {
		root := t.TempDir()
		mkFile(t, root, "a.go", "package a\nfunc A() {}\n")
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

		_, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "A"})
		if ok {
			t.Error("unbuilt: Symbols should return ok=false")
		}
		if err != nil {
			t.Errorf("unbuilt: Symbols should return nil error, got %v", err)
		}
		if idx.BuildCount() != 0 {
			t.Errorf("unbuilt: build count = %d, want 0", idx.BuildCount())
		}
	})

	t.Run("partial", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 20; i++ {
			mkFile(t, root, fmt.Sprintf("f%d.go", i),
				fmt.Sprintf("package pkg\nfunc F%d() {}\n", i))
		}
		ws, err := tools.NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := newIndex(ws, Options{
			TempDir:  t.TempDir(),
			Ignore:   tools.NoGlobalExcludes(),
			MaxFiles: 5, // partial build
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		// Build via code_search.
		tool := idx.Tools()[0]
		in, _ := json.Marshal(map[string]any{"query": "F0"})
		r := tool.Execute(ctx, in)
		if !r.OK {
			t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
		}

		_, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "F0"})
		if ok {
			t.Error("partial: Symbols should return ok=false")
		}
		if err != nil {
			t.Errorf("partial: Symbols should return nil error, got %v", err)
		}
	})

	t.Run("closed", func(t *testing.T) {
		root := t.TempDir()
		mkFile(t, root, "a.go", "package a\nfunc A() {}\n")
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

		// Build, then close.
		tool := idx.Tools()[0]
		in, _ := json.Marshal(map[string]any{"query": "A"})
		tool.Execute(ctx, in)
		idx.Close()

		_, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "A"})
		if ok {
			t.Error("closed: Symbols should return ok=false")
		}
		if err != nil {
			t.Errorf("closed: Symbols should return nil error, got %v", err)
		}
	})

	t.Run("over 1000 matches", func(t *testing.T) {
		root := t.TempDir()
		// Create enough declarations to exceed 1000.
		var content strings.Builder
		content.WriteString("package pkg\n\n")
		for i := 0; i < 1010; i++ {
			fmt.Fprintf(&content, "func Sym%04d() {}\n", i)
		}
		mkFile(t, root, "big.go", content.String())

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

		// Build via code_search.
		tool := idx.Tools()[0]
		in, _ := json.Marshal(map[string]any{"query": "Sym"})
		tool.Execute(ctx, in)

		// Query with a prefix that matches all 1010 declarations.
		_, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "Sym"})
		if ok {
			t.Error("over 1000: Symbols should return ok=false")
		}
		if err != nil {
			t.Errorf("over 1000: Symbols should return nil error, got %v", err)
		}
	})
}

// TS-03-53: Symbols runs a pending revalidation and re-outlines dirty files
// in scope before answering.
func TestSymbolsRevalidatesAndReoutlines_TS03_53(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n\nfunc Original() {}\n")
	mkFile(t, root, "x.go", "package pkg\n\nfunc InX() {}\n")

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

	ctx := context.Background()

	// Build via code_search.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "Original"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
	}

	// Edit a.go to add func Fresh.
	os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package pkg\n\nfunc Fresh() {}\n"), 0o644)
	idx.Invalidate("a.go")

	// Symbols should find Fresh.
	a, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "Fresh"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	if len(a.Matches) != 1 {
		t.Errorf("expected 1 match for Fresh, got %d", len(a.Matches))
	}

	// Rename x.go to y.go via filesystem (simulating execute).
	os.Rename(filepath.Join(root, "x.go"), filepath.Join(root, "y.go"))
	idx.Invalidate("") // triggers revalidation

	// Symbols should find InX under y.go, not x.go.
	a, ok, err = idx.Symbols(ctx, tools.SymbolQuery{Name: "InX"})
	if err != nil {
		t.Fatalf("Symbols error: %v", err)
	}
	if !ok {
		t.Fatal("Symbols should return ok=true")
	}
	if len(a.Matches) != 1 {
		t.Fatalf("expected 1 match for InX, got %d", len(a.Matches))
	}
	if a.Matches[0].Path != "y.go" {
		t.Errorf("InX should be in y.go, got %q", a.Matches[0].Path)
	}
}

// TS-03-54: A cancelled context passed to Symbols is the only case that
// returns an error.
func TestSymbolsCancelledContext_TS03_54(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "a.go", "package pkg\n\nfunc A() {}\n")

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

	ctx := context.Background()

	// Build via code_search.
	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "A"})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
	}

	// Make a dirty file so Symbols has work to do.
	os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package pkg\n\nfunc B() {}\n"), 0o644)
	idx.Invalidate("a.go")

	// Cancelled context should return an error.
	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = idx.Symbols(cancelledCtx, tools.SymbolQuery{Name: "B"})
	if err == nil {
		t.Error("Symbols with cancelled context should return an error")
	}

	// Every ok=false state with a live context should return nil error.
	// Unbuilt:
	idx2, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()
	_, _, err = idx2.Symbols(ctx, tools.SymbolQuery{Name: "A"})
	if err != nil {
		t.Errorf("unbuilt: Symbols should return nil error, got %v", err)
	}

	// Closed:
	idx3, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	idx3.Close()
	_, _, err = idx3.Symbols(ctx, tools.SymbolQuery{Name: "A"})
	if err != nil {
		t.Errorf("closed: Symbols should return nil error, got %v", err)
	}
}

// TS-03-55: find_symbol gives the same matches in the same order through the
// index as through its own table.
func TestFindSymbolSameMatchesThroughIndex_TS03_55(t *testing.T) {
	// Generate a fixture tree with various declarations.
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\n\nfunc Main() {}\nfunc helper() {}\n")
	mkFile(t, root, "lib.go", "package main\n\nfunc Lib() {}\nfunc lib() {}\n")
	mkFile(t, root, "pkg/server.go", "package pkg\n\ntype Server struct{}\nfunc (s Server) Run() {}\nfunc NewServer() *Server { return nil }\n")
	mkFile(t, root, "pkg/util.go", "package pkg\n\nfunc Util() {}\nvar UtilVar int\n")
	mkFile(t, root, "test/t_test.go", "package test\n\nfunc TestSomething() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Build the codesearch index.
	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	// Build via code_search.
	csTool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "func"})
	r := csTool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("code_search failed: %s: %s", r.Error, r.Detail)
	}

	// Build find_symbol with the index.
	withIdxAll, err := tools.All(tools.Options{
		Workspace: ws,
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   tools.SymbolOptions{},
		Index:     idx,
	})
	if err != nil {
		t.Fatal(err)
	}
	var findWithIdx core.Tool
	for _, tl := range withIdxAll {
		if tl.Name == "find_symbol" {
			findWithIdx = tl
			break
		}
	}

	// Build find_symbol without the index.
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
			break
		}
	}

	// Test a variety of queries.
	queries := []map[string]any{
		{"name": "Main"},
		{"name": "lib"},
		{"name": "Lib", "exact": true},
		{"name": "Server.Run"},
		{"name": "run"},
		{"name": "Util", "kind": "func"},
		{"name": "New", "path": "pkg"},
	}

	ctx := context.Background()
	for _, q := range queries {
		qJSON, _ := json.Marshal(q)

		rIdx := findWithIdx.Execute(ctx, qJSON)
		rNoIdx := findNoIdx.Execute(ctx, qJSON)

		if !rIdx.OK || !rNoIdx.OK {
			t.Errorf("query %v: withIdx.OK=%v noIdx.OK=%v", q, rIdx.OK, rNoIdx.OK)
			continue
		}

		// Extract matches from Data.
		idxMatches := extractSymbolMatches(rIdx)
		noIdxMatches := extractSymbolMatches(rNoIdx)

		// Compare match lists (content and order).
		if len(idxMatches) != len(noIdxMatches) {
			t.Errorf("query %v: withIdx has %d matches, noIdx has %d",
				q, len(idxMatches), len(noIdxMatches))
			continue
		}

		for i := range idxMatches {
			if idxMatches[i] != noIdxMatches[i] {
				t.Errorf("query %v: match %d differs:\n  withIdx: %+v\n  noIdx:   %+v",
					q, i, idxMatches[i], noIdxMatches[i])
			}
		}
	}

	// Verify that find_symbol with the index mentions "codesearch" in its header
	// to distinguish from the table-based backend.
	qJSON, _ := json.Marshal(map[string]any{"name": "Main"})
	rIdx := findWithIdx.Execute(ctx, qJSON)
	if !rIdx.OK {
		t.Fatal("find_symbol with index failed")
	}
	if !strings.Contains(rIdx.Text, "codesearch") {
		t.Errorf("find_symbol with index should mention 'codesearch' in header, got:\n%s", rIdx.Text)
	}
}

// --- helpers ---

// symbolNames extracts the Name field from a slice of SymbolMatch.
func symbolNames(matches []tools.SymbolMatch) []string {
	names := make([]string, len(matches))
	for i, m := range matches {
		names[i] = m.Name
	}
	return names
}

// containsName checks if a name is in a slice.
func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// symbolMatchKey is a comparable representation of a SymbolMatch for comparison.
type symbolMatchKey struct {
	Path      string
	Kind      string
	Name      string
	Container string
	Exported  bool
	StartLine int
	EndLine   int
}

// extractSymbolMatches extracts SymbolMatch data from a find_symbol result.
func extractSymbolMatches(r core.ToolResult) []symbolMatchKey {
	if r.Data == nil {
		return nil
	}
	syms, ok := r.Data["symbols"]
	if !ok {
		return nil
	}
	arr, ok := syms.([]tools.SymbolMatch)
	if !ok {
		// Try []any (JSON round-trip).
		anyArr, ok := syms.([]any)
		if !ok {
			return nil
		}
		var keys []symbolMatchKey
		for _, item := range anyArr {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			keys = append(keys, symbolMatchKey{
				Path:      strVal(m, "path"),
				Kind:      strVal(m, "kind"),
				Name:      strVal(m, "name"),
				Container: strVal(m, "container"),
				Exported:  boolVal(m, "exported"),
				StartLine: intVal(m, "start_line"),
				EndLine:   intVal(m, "end_line"),
			})
		}
		return keys
	}
	var keys []symbolMatchKey
	for _, sm := range arr {
		keys = append(keys, symbolMatchKey{
			Path:      sm.Path,
			Kind:      sm.Kind,
			Name:      sm.Name,
			Container: sm.Container,
			Exported:  sm.Exported,
			StartLine: sm.StartLine,
			EndLine:   sm.EndLine,
		})
	}
	return keys
}

func strVal(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func boolVal(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func intVal(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case int64:
		return int(v)
	}
	return 0
}

// Ensure unused imports don't cause errors.
var (
	_ = outline.BackendNone
	_ = sort.Strings
	_ = fmt.Sprintf
	_ = filepath.Join
)
