package tools

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
)

// TS-02-14: find_symbol rejects empty, blank and 257-byte names but accepts a 256-byte name, before any walk
func TestFindSymbolNameValidation_TS02_14(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	var walkCount int
	origWalkFn := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(string, os.DirEntry) error) error {
		walkCount++
		return origWalkFn(ctx, ws, root, opts, fn)
	}
	defer func() { walkFn = origWalkFn }()

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Empty name.
	walkCount = 0
	r := exec(context.Background(), json.RawMessage(`{"name":""}`))
	if r.OK {
		t.Fatal("empty name: expected OK=false")
	}
	if r.Error != "invalid_arguments" {
		t.Fatalf("empty name: error=%q, want invalid_arguments", r.Error)
	}
	if walkCount != 0 {
		t.Fatalf("empty name: walk was called %d times, want 0", walkCount)
	}

	// Blank name (spaces only).
	walkCount = 0
	r = exec(context.Background(), json.RawMessage(`{"name":"   "}`))
	if r.OK {
		t.Fatal("blank name: expected OK=false")
	}
	if r.Error != "invalid_arguments" {
		t.Fatalf("blank name: error=%q, want invalid_arguments", r.Error)
	}
	if walkCount != 0 {
		t.Fatalf("blank name: walk was called %d times, want 0", walkCount)
	}

	// 257-byte name.
	longName := strings.Repeat("a", 257)
	walkCount = 0
	args, _ := json.Marshal(map[string]any{"name": longName})
	r = exec(context.Background(), args)
	if r.OK {
		t.Fatal("257-byte name: expected OK=false")
	}
	if r.Error != "invalid_arguments" {
		t.Fatalf("257-byte name: error=%q, want invalid_arguments", r.Error)
	}
	if walkCount != 0 {
		t.Fatalf("257-byte name: walk was called %d times, want 0", walkCount)
	}

	// 256-byte name: should be accepted (OK=true).
	name256 := strings.Repeat("a", 256)
	walkCount = 0
	args, _ = json.Marshal(map[string]any{"name": name256})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("256-byte name: expected OK=true, got error=%q detail=%q", r.Error, r.Detail)
	}
}

// TS-02-15: find_symbol rejects an unknown kind, listing the eleven allowed values, and normalises case and spaces
func TestFindSymbolKindValidation_TS02_15(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	var walkCount int
	origWalkFn := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(string, os.DirEntry) error) error {
		walkCount++
		return origWalkFn(ctx, ws, root, opts, fn)
	}
	defer func() { walkFn = origWalkFn }()

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Unknown kind "function".
	walkCount = 0
	args, _ := json.Marshal(map[string]any{"name": "Hello", "kind": "function"})
	r := exec(context.Background(), args)
	if r.OK {
		t.Fatal("unknown kind 'function': expected OK=false")
	}
	if r.Error != "invalid_arguments" {
		t.Fatalf("unknown kind: error=%q, want invalid_arguments", r.Error)
	}
	// Detail should list all eleven allowed values.
	for _, k := range []string{"func", "method", "type", "class", "interface", "enum", "trait", "const", "var", "module", "macro"} {
		if !strings.Contains(r.Detail, k) {
			t.Errorf("detail should contain %q: %q", k, r.Detail)
		}
	}
	if walkCount != 0 {
		t.Fatalf("unknown kind: walk was called %d times, want 0", walkCount)
	}

	// Normalised kind "  FUNC " should be accepted as "func".
	walkCount = 0
	args, _ = json.Marshal(map[string]any{"name": "Hello", "kind": "  FUNC "})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("normalised kind '  FUNC ': expected OK=true, got error=%q detail=%q", r.Error, r.Detail)
	}

	// Verify the tool's kind list matches the outline package's Kind constants.
	outlineKinds := []string{
		string(outline.KindFunc),
		string(outline.KindMethod),
		string(outline.KindType),
		string(outline.KindClass),
		string(outline.KindInterface),
		string(outline.KindEnum),
		string(outline.KindTrait),
		string(outline.KindConst),
		string(outline.KindVar),
		string(outline.KindModule),
		string(outline.KindMacro),
	}
	sort.Strings(outlineKinds)
	toolKinds := make([]string, len(validKinds))
	copy(toolKinds, validKinds)
	sort.Strings(toolKinds)
	if len(outlineKinds) != len(toolKinds) {
		t.Fatalf("kind count mismatch: outline has %d, tool has %d", len(outlineKinds), len(toolKinds))
	}
	for i := range outlineKinds {
		if outlineKinds[i] != toolKinds[i] {
			t.Fatalf("kind mismatch at %d: outline=%q, tool=%q", i, outlineKinds[i], toolKinds[i])
		}
	}
}

// TS-02-16: find_symbol refuses a path outside the workspace without calling Walk
func TestFindSymbolPathOutsideWorkspace_TS02_16(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	var walkCount int
	origWalkFn := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(string, os.DirEntry) error) error {
		walkCount++
		return origWalkFn(ctx, ws, root, opts, fn)
	}
	defer func() { walkFn = origWalkFn }()

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Relative path outside workspace.
	walkCount = 0
	args, _ := json.Marshal(map[string]any{"name": "Hello", "path": "../"})
	r := exec(context.Background(), args)
	if r.OK {
		t.Fatal("path '../': expected OK=false")
	}
	if r.Error != "path_not_allowed" {
		t.Fatalf("path '../': error=%q, want path_not_allowed", r.Error)
	}
	if walkCount != 0 {
		t.Fatalf("path '../': walk was called %d times, want 0", walkCount)
	}

	// Absolute path outside workspace.
	walkCount = 0
	outsideDir := t.TempDir() // a different temp dir
	args, _ = json.Marshal(map[string]any{"name": "Hello", "path": outsideDir})
	r = exec(context.Background(), args)
	if r.OK {
		t.Fatal("absolute outside path: expected OK=false")
	}
	if r.Error != "path_not_allowed" {
		t.Fatalf("absolute outside path: error=%q, want path_not_allowed", r.Error)
	}
	if walkCount != 0 {
		t.Fatalf("absolute outside path: walk was called %d times, want 0", walkCount)
	}
}

// TS-02-17: find_symbol returns read_failed for a nonexistent path before any walk
func TestFindSymbolPathNotExist_TS02_17(t *testing.T) {
	root := t.TempDir()
	// No "nope" directory exists.

	var walkCount int
	origWalkFn := walkFn
	walkFn = func(ctx context.Context, ws *Workspace, root string, opts WalkOptions, fn func(string, os.DirEntry) error) error {
		walkCount++
		return origWalkFn(ctx, ws, root, opts, fn)
	}
	defer func() { walkFn = origWalkFn }()

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	walkCount = 0
	args, _ := json.Marshal(map[string]any{"name": "Run", "path": "nope"})
	r := exec(context.Background(), args)
	if r.OK {
		t.Fatal("nonexistent path: expected OK=false")
	}
	if r.Error != "read_failed" {
		t.Fatalf("nonexistent path: error=%q, want read_failed", r.Error)
	}
	if walkCount != 0 {
		t.Fatalf("nonexistent path: walk was called %d times, want 0", walkCount)
	}
}

// TS-02-18: find_symbol rejects a regular file as path and points to file_outline
func TestFindSymbolPathIsFile_TS02_18(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	args, _ := json.Marshal(map[string]any{"name": "Hello", "path": "a.go"})
	r := exec(context.Background(), args)
	if r.OK {
		t.Fatal("file path: expected OK=false")
	}
	if r.Error != "invalid_arguments" {
		t.Fatalf("file path: error=%q, want invalid_arguments", r.Error)
	}
	if !strings.Contains(r.Detail, "file_outline") {
		t.Fatalf("file path: detail should mention file_outline: %q", r.Detail)
	}
}

// TS-02-19: Default matching is prefix with smart-case
func TestFindSymbolPrefixSmartCase_TS02_19(t *testing.T) {
	root := t.TempDir()
	// Create a Go file with multiple declarations to test matching.
	mkSymFile(t, root, "main.go", `package main

func Run() {}
func RunAll() {}
func Runtime() {}
func Other() {}
`)
	mkSymFile(t, root, "extra.go", `package main

func runner() {}
func rUN() {}
`)

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// "run" (all lowercase) → case-insensitive prefix match.
	args, _ := json.Marshal(map[string]any{"name": "run"})
	r := exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("prefix 'run': error=%q detail=%q", r.Error, r.Detail)
	}
	names := extractSymbolNames(t, r)
	// Should match: Run, RunAll, Runtime, runner, rUN (case-insensitive prefix).
	// Should NOT match: Other.
	expected := map[string]bool{"Run": true, "RunAll": true, "Runtime": true, "runner": true, "rUN": true}
	for _, n := range names {
		if !expected[n] {
			t.Errorf("'run' matched unexpected name %q", n)
		}
		delete(expected, n)
	}
	for n := range expected {
		t.Errorf("'run' did not match expected name %q", n)
	}

	// "Run" (has uppercase) → case-sensitive prefix match.
	args, _ = json.Marshal(map[string]any{"name": "Run"})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("prefix 'Run': error=%q detail=%q", r.Error, r.Detail)
	}
	names = extractSymbolNames(t, r)
	// Should match: Run, RunAll, Runtime (case-sensitive prefix).
	// Should NOT match: runner, rUN, Other.
	expected = map[string]bool{"Run": true, "RunAll": true, "Runtime": true}
	for _, n := range names {
		if !expected[n] {
			t.Errorf("'Run' matched unexpected name %q", n)
		}
		delete(expected, n)
	}
	for n := range expected {
		t.Errorf("'Run' did not match expected name %q", n)
	}
}

// TS-02-20: exact true matches only identical names case-sensitively
func TestFindSymbolExactMatch_TS02_20(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "main.go", `package main

func Run() {}
func RunAll() {}
`)
	mkSymFile(t, root, "extra.go", `package main

func run() {}
`)

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// exact=true, name="Run" → only Run.
	args, _ := json.Marshal(map[string]any{"name": "Run", "exact": true})
	r := exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("exact 'Run': error=%q detail=%q", r.Error, r.Detail)
	}
	names := extractSymbolNames(t, r)
	if len(names) != 1 || names[0] != "Run" {
		t.Fatalf("exact 'Run': got %v, want [Run]", names)
	}

	// exact=true, name="run" → only run.
	args, _ = json.Marshal(map[string]any{"name": "run", "exact": true})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("exact 'run': error=%q detail=%q", r.Error, r.Detail)
	}
	names = extractSymbolNames(t, r)
	if len(names) != 1 || names[0] != "run" {
		t.Fatalf("exact 'run': got %v, want [run]", names)
	}
}

// TS-02-21: A dotted name is compared against Container.Name and an undotted name against Name alone
func TestFindSymbolQualifiedName_TS02_21(t *testing.T) {
	root := t.TempDir()
	// Create Go files with methods on different receivers and a top-level func.
	mkSymFile(t, root, "runner.go", `package main

type Runner struct{}

func (r *Runner) Run() {}
func (r *Runner) Stop() {}
`)
	mkSymFile(t, root, "other.go", `package main

type Other struct{}

func (o *Other) Run() {}
`)
	mkSymFile(t, root, "funcs.go", `package main

func Run() {}
`)

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// "Runner.Run" (dotted, exact) → only Runner's Run method.
	args, _ := json.Marshal(map[string]any{"name": "Runner.Run", "exact": true})
	r := exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("dotted 'Runner.Run' exact: error=%q detail=%q", r.Error, r.Detail)
	}
	names := extractSymbolNames(t, r)
	if len(names) != 1 || names[0] != "Run" {
		t.Fatalf("dotted 'Runner.Run' exact: got %v, want [Run]", names)
	}
	// Verify it's the one on Runner, not Other.
	containers := extractSymbolContainers(t, r)
	if len(containers) != 1 || containers[0] != "Runner" {
		t.Fatalf("dotted 'Runner.Run' exact: containers=%v, want [Runner]", containers)
	}

	// "runner.ru" (dotted, lowercase prefix) → case-insensitive prefix on qualified string.
	args, _ = json.Marshal(map[string]any{"name": "runner.ru"})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("dotted 'runner.ru': error=%q detail=%q", r.Error, r.Detail)
	}
	names = extractSymbolNames(t, r)
	// Should find Runner.Run (case-insensitive prefix on "Runner.Run").
	found := false
	for i, n := range names {
		cs := extractSymbolContainers(t, r)
		if n == "Run" && cs[i] == "Runner" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dotted 'runner.ru': did not find Runner.Run, got names=%v", names)
	}
	// A dotted name should never match the container-less func Run.
	for i, n := range names {
		cs := extractSymbolContainers(t, r)
		if n == "Run" && cs[i] == "" {
			t.Fatal("dotted 'runner.ru': should not match container-less func Run")
		}
	}

	// "Runner.Run" with exact=true via dotted name.
	args, _ = json.Marshal(map[string]any{"name": "Runner.Run", "exact": true})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("dotted 'Runner.Run' exact: error=%q detail=%q", r.Error, r.Detail)
	}
	names = extractSymbolNames(t, r)
	containers = extractSymbolContainers(t, r)
	if len(names) != 1 {
		t.Fatalf("dotted 'Runner.Run' exact: got %d matches, want 1", len(names))
	}
	if names[0] != "Run" || containers[0] != "Runner" {
		t.Fatalf("dotted 'Runner.Run' exact: got %s.%s, want Runner.Run", containers[0], names[0])
	}

	// "Run" (undotted) → matches methods Run on both receivers AND the func.
	args, _ = json.Marshal(map[string]any{"name": "Run", "exact": true})
	r = exec(context.Background(), args)
	if !r.OK {
		t.Fatalf("undotted 'Run' exact: error=%q detail=%q", r.Error, r.Detail)
	}
	names = extractSymbolNames(t, r)
	// Should find 3 Runs: Runner.Run, Other.Run, and func Run.
	if len(names) != 3 {
		t.Fatalf("undotted 'Run' exact: got %d matches, want 3; names=%v", len(names), names)
	}
	for _, n := range names {
		if n != "Run" {
			t.Fatalf("undotted 'Run' exact: unexpected name %q", n)
		}
	}
}

// extractSymbolNames extracts the "name" field from each symbol in the result's Data.
func extractSymbolNames(t *testing.T, r core.ToolResult) []string {
	t.Helper()
	syms, ok := r.Data["symbols"]
	if !ok {
		t.Fatal("no 'symbols' key in Data")
	}
	slice, ok := syms.([]SymbolMatch)
	if ok {
		var names []string
		for _, s := range slice {
			names = append(names, s.Name)
		}
		return names
	}
	// Try as []any (from JSON round-trip).
	anySlice, ok := syms.([]any)
	if ok {
		var names []string
		for _, item := range anySlice {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := m["name"].(string); ok {
				names = append(names, n)
			}
		}
		return names
	}
	t.Fatalf("symbols is neither []SymbolMatch nor []any: %T", syms)
	return nil
}

// extractSymbolContainers extracts the "container" field from each symbol in the result's Data.
func extractSymbolContainers(t *testing.T, r core.ToolResult) []string {
	t.Helper()
	syms, ok := r.Data["symbols"]
	if !ok {
		t.Fatal("no 'symbols' key in Data")
	}
	slice, ok := syms.([]SymbolMatch)
	if ok {
		var containers []string
		for _, s := range slice {
			containers = append(containers, s.Container)
		}
		return containers
	}
	anySlice, ok := syms.([]any)
	if ok {
		var containers []string
		for _, item := range anySlice {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			c, _ := m["container"].(string)
			containers = append(containers, c)
		}
		return containers
	}
	t.Fatalf("symbols is neither []SymbolMatch nor []any: %T", syms)
	return nil
}

