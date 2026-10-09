package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// TS-05-1 (unit): find_references is registered in tools.All() following find_symbol
// and in FileNavigationTools() with correct metadata.
// Verifies 05-REQ-1.1.
func TestFindReferencesTool_Registration_TS05_1(t *testing.T) {
	wsDir := t.TempDir()
	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()

	if fr.Name != "find_references" {
		t.Fatalf("expected name find_references, got %s", fr.Name)
	}
	if !fr.Builtin {
		t.Errorf("expected Builtin = true")
	}
	if fr.ExecutionMode != core.Parallel {
		t.Errorf("expected ExecutionMode = Parallel, got %v", fr.ExecutionMode)
	}
	if guard.IsShellTool(fr.Name) {
		t.Errorf("find_references must not be a shell tool")
	}
	for _, n := range guard.ShellToolNames {
		if n == fr.Name {
			t.Errorf("find_references must not be in guard.ShellToolNames")
		}
	}

	expectedGuideline := "Use find_symbol for where a name is declared, find_references for who uses it, and search_files for text."
	foundGuideline := false
	for _, g := range fr.PromptGuidelines {
		if strings.Contains(g, expectedGuideline) {
			foundGuideline = true
			break
		}
	}
	if !foundGuideline {
		t.Errorf("expected PromptGuidelines to contain %q, got %v", expectedGuideline, fr.PromptGuidelines)
	}

	// In Task 10, find_references is wired into All() and FileNavigationTools().
	// If already present in All(), verify the wiring criteria.
	all, _ := All(opts)
	idxFS := -1
	idxFR := -1
	for i, tool := range all {
		if tool.Name == "find_symbol" {
			idxFS = i
		}
		if tool.Name == "find_references" {
			idxFR = i
		}
	}
	if idxFR != -1 {
		if idxFS == -1 || idxFR != idxFS+1 {
			t.Errorf("find_references at index %d, find_symbol at %d; expected immediately following", idxFR, idxFS)
		}
		nav := FileNavigationTools()
		if len(nav) != 6 || !slices.Contains(nav, "find_references") {
			t.Errorf("expected FileNavigationTools() to have 6 items including find_references, got %v", nav)
		}
	}
}

// TS-05-2 (unit): find_references input schema specifies required name and optional path, kind, include_tests, and max_results.
// Verifies 05-REQ-1.2.
func TestFindReferencesTool_InputSchema_TS05_2(t *testing.T) {
	wsDir := t.TempDir()
	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()

	s := fr.InputSchema
	if s == nil {
		t.Fatal("expected non-nil InputSchema")
	}
	if s.Type != schema.TypeObject {
		t.Fatalf("expected TypeObject, got %v", s.Type)
	}

	props := s.Properties
	if !s.IsRequired("name") {
		t.Errorf("name must be required")
	}
	nameProp, ok := props["name"]
	if !ok || nameProp.Type != schema.TypeString {
		t.Errorf("expected name to be TypeString, got %v", nameProp)
	}

	if s.IsRequired("path") {
		t.Errorf("path must be optional")
	}
	pathProp, ok := props["path"]
	if !ok || pathProp.Type != schema.TypeString {
		t.Errorf("expected path to be TypeString, got %v", pathProp)
	}

	if s.IsRequired("kind") {
		t.Errorf("kind must be optional")
	}
	kindProp, ok := props["kind"]
	if !ok || kindProp.Type != schema.TypeString {
		t.Errorf("expected kind to be TypeString, got %v", kindProp)
	}

	if s.IsRequired("include_tests") {
		t.Errorf("include_tests must be optional")
	}
	incProp, ok := props["include_tests"]
	if !ok || incProp.Type != schema.TypeBoolean {
		t.Errorf("expected include_tests to be TypeBoolean, got %v", incProp)
	}

	if s.IsRequired("max_results") {
		t.Errorf("max_results must be optional")
	}
	maxProp, ok := props["max_results"]
	if !ok || maxProp.Type != schema.TypeInteger {
		t.Errorf("expected max_results to be TypeInteger, got %v", maxProp)
	}
}

// TS-05-3 (unit): find_references rejects malformed JSON, empty name, whitespace name, or name exceeding 256 bytes.
// Verifies 05-REQ-1.3.
func TestFindReferencesTool_InvalidName_TS05_3(t *testing.T) {
	wsDir := t.TempDir()
	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()
	ctx := context.Background()

	inputs := []string{
		"{bad-json",
		"{}",
		`{"name":""}`,
		`{"name":"   "}`,
		`{"name":"` + strings.Repeat("a", 257) + `"}`,
	}

	for _, in := range inputs {
		res := fr.Execute(ctx, []byte(in))
		if res.OK {
			t.Errorf("expected OK=false for input %q", in)
		}
		if res.Error != "invalid_arguments" {
			t.Errorf("expected error invalid_arguments for input %q, got %q", in, res.Error)
		}
		if res.Text == "" {
			t.Errorf("expected non-empty error message for input %q", in)
		}
	}
}

// TS-05-4 (unit): find_references rejects invalid kind parameters with a list of valid kinds.
// Verifies 05-REQ-1.4.
func TestFindReferencesTool_InvalidKind_TS05_4(t *testing.T) {
	wsDir := t.TempDir()
	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()
	ctx := context.Background()

	badKinds := []string{"struct", "unknown", "class_method"}
	expectedKinds := []string{
		"func", "method", "type", "class", "interface",
		"enum", "trait", "const", "var", "module", "macro",
	}

	for _, k := range badKinds {
		in := `{"name":"Foo","kind":"` + k + `"}`
		res := fr.Execute(ctx, []byte(in))
		if res.OK {
			t.Errorf("expected OK=false for kind %q", k)
		}
		if res.Error != "invalid_arguments" {
			t.Errorf("expected error invalid_arguments for kind %q, got %q", k, res.Error)
		}
		for _, ek := range expectedKinds {
			if !strings.Contains(res.Text, ek) {
				t.Errorf("expected error message to contain valid kind %q for bad kind %q, got %q", ek, k, res.Text)
			}
		}
	}
}

// TS-05-5 (unit): find_references refuses a path resolving outside workspace root.
// Verifies 05-REQ-1.5.
func TestFindReferencesTool_PathOutsideWorkspace_TS05_5(t *testing.T) {
	wsDir := t.TempDir()
	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()
	ctx := context.Background()

	res := fr.Execute(ctx, []byte(`{"name":"Foo","path":"../outside"}`))
	if res.OK {
		t.Errorf("expected OK=false for path outside workspace")
	}
	if res.Error != "path_not_allowed" {
		t.Errorf("expected error path_not_allowed, got %q", res.Error)
	}
}

// TS-05-6 (unit): find_references rejects non-existent paths or paths pointing to regular files.
// Verifies 05-REQ-1.6.
func TestFindReferencesTool_InvalidPath_TS05_6(t *testing.T) {
	wsDir := t.TempDir()
	mainGo := filepath.Join(wsDir, "main.go")
	if err := os.WriteFile(mainGo, []byte("package main\nfunc Foo() {}\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()
	ctx := context.Background()

	res1 := fr.Execute(ctx, []byte(`{"name":"Foo","path":"missing_dir"}`))
	if res1.OK {
		t.Errorf("expected OK=false for non-existent path")
	}
	if res1.Error != "invalid_arguments" {
		t.Errorf("expected error invalid_arguments, got %q", res1.Error)
	}
	if !strings.Contains(strings.ToLower(res1.Text), "directory") {
		t.Errorf("expected error message to indicate directory not found, got %q", res1.Text)
	}

	res2 := fr.Execute(ctx, []byte(`{"name":"Foo","path":"main.go"}`))
	if res2.OK {
		t.Errorf("expected OK=false for file path")
	}
	if res2.Error != "invalid_arguments" {
		t.Errorf("expected error invalid_arguments, got %q", res2.Error)
	}
	if !strings.Contains(strings.ToLower(res2.Text), "directory") {
		t.Errorf("expected error message to indicate path is not a directory, got %q", res2.Text)
	}
}

// TS-05-7 (unit): find_references terminates immediately with aborted error when context is cancelled.
// Verifies 05-REQ-1.7.
func TestFindReferencesTool_ContextCancelled_TS05_7(t *testing.T) {
	wsDir := t.TempDir()
	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := fr.Execute(ctx, []byte(`{"name":"Foo"}`))
	if res.OK {
		t.Errorf("expected OK=false for cancelled context")
	}
	if res.Error != "aborted" {
		t.Errorf("expected error aborted, got %q", res.Error)
	}
	if res.Text != "Operation aborted" {
		t.Errorf("expected text 'Operation aborted', got %q", res.Text)
	}
}

// TS-05-46 (integration): Workspace.References returns ReferenceResult matching find_references tool output with deterministic ranking.
// Verifies 05-REQ-9.2.
func TestFindReferencesTool_WorkspaceReferencesParity_TS05_46(t *testing.T) {
	wsDir := t.TempDir()
	goContent := `package main

func TargetFunc() int {
	return 42
}

func CallGo() int {
	return TargetFunc()
}
`
	pyContent := `def call_python():
    # TargetFunc used in python
    TargetFunc()
`
	if err := os.WriteFile(filepath.Join(wsDir, "main.go"), []byte(goContent), 0644); err != nil {
		t.Fatalf("WriteFile main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "script.py"), []byte(pyContent), 0644); err != nil {
		t.Fatalf("WriteFile script.py: %v", err)
	}

	ws, err := NewWorkspace(wsDir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	opts := Options{Workspace: ws}
	ft := newFileTools(opts)
	fr := ft.findReferencesTool()
	ctx := context.Background()

	toolRes := fr.Execute(ctx, []byte(`{"name":"TargetFunc","include_tests":true,"max_results":30}`))
	if !toolRes.OK {
		t.Fatalf("tool execution failed: %s (%s)", toolRes.Error, toolRes.Text)
	}

	targetDecl := outline.Decl{
		Kind:      outline.KindFunc,
		Name:      "TargetFunc",
		StartLine: 3,
	}

	wsRes, err := ws.References(ctx, targetDecl, ReferenceOptions{IncludeTests: true, MaxResults: 30})
	if err != nil {
		t.Fatalf("ws.References failed: %v", err)
	}

	var toolResult ReferenceResult
	if r, ok := toolRes.Data["result"].(ReferenceResult); ok {
		toolResult = r
	} else {
		t.Fatalf("toolRes.Data[\"result\"] is not ReferenceResult: %T", toolRes.Data["result"])
	}

	if !reflect.DeepEqual(toolResult.Sites, wsRes.Sites) {
		t.Errorf("Sites mismatch:\ntool: %+v\nws:   %+v", toolResult.Sites, wsRes.Sites)
	}
	if toolResult.Backend != wsRes.Backend {
		t.Errorf("Backend mismatch: tool=%q, ws=%q", toolResult.Backend, wsRes.Backend)
	}
	if toolResult.Truncated != wsRes.Truncated {
		t.Errorf("Truncated mismatch: tool=%v, ws=%v", toolResult.Truncated, wsRes.Truncated)
	}
	if toolResult.Partial != wsRes.Partial {
		t.Errorf("Partial mismatch: tool=%v, ws=%v", toolResult.Partial, wsRes.Partial)
	}
}

// TS-06-17: find_references declares its result. target is the resolved
// declaration, errors a count, and the structs marshal with Go field names.
func TestOutputSchemaFindReferences_TS06_17(t *testing.T) {
	s := toolByName(t, t.TempDir(), "find_references").OutputSchema
	assertObject(t, "find_references", s, map[string]prop{
		"target": {schema.TypeObject, true}, "sites": {schema.TypeArray, true},
		"backend": {schema.TypeString, true}, "partial": {schema.TypeBoolean, true},
		"truncated": {schema.TypeBoolean, true}, "packages_checked": {schema.TypeInteger, true},
		"errors": {schema.TypeInteger, true}, "result": {schema.TypeObject, true},
	})
	assertObject(t, "find_references.target", s.Properties["target"], declProps)
	site := s.Properties["sites"].Items
	assertObject(t, "find_references.sites[]", site, map[string]prop{
		"Path": {schema.TypeString, true}, "Line": {schema.TypeInteger, true},
		"Column": {schema.TypeInteger, true}, "Confidence": {schema.TypeString, true},
		"Enclosing": {schema.TypeObject, true}, "Source": {schema.TypeString, true},
	})
	assertObject(t, "find_references.sites[].Enclosing", site.Properties["Enclosing"], declProps)
	assertObject(t, "find_references.result", s.Properties["result"], map[string]prop{
		"Target": {schema.TypeObject, true}, "Sites": {schema.TypeArray, true},
		"Backend": {schema.TypeString, true}, "Partial": {schema.TypeBoolean, true},
		"PackagesChecked": {schema.TypeInteger, true}, "Errors": {schema.TypeInteger, true},
		"Truncated": {schema.TypeBoolean, true},
	})
}
