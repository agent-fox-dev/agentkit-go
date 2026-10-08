package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
)

// TS-05-45 (unit): Workspace exports ReferenceOptions, ReferenceSite, ReferenceResult types and References method
// Verifies: 05-REQ-9.1
func TestWorkspaceReferences_Exports_TS_05_45(t *testing.T) {
	// Compile-time check of (*Workspace).References signature.
	var _ func(context.Context, outline.Decl, ReferenceOptions) (ReferenceResult, error) = (*Workspace)(nil).References
	var _ func(*Workspace, context.Context, outline.Decl, ReferenceOptions) (ReferenceResult, error) = (*Workspace).References

	// Verify ReferenceOptions fields.
	optsType := reflect.TypeOf(ReferenceOptions{})
	expectedOptsFields := []string{"Path", "IncludeTests", "MaxResults"}
	for _, f := range expectedOptsFields {
		if _, ok := optsType.FieldByName(f); !ok {
			t.Fatalf("ReferenceOptions missing field %q", f)
		}
	}

	// Verify ReferenceSite fields.
	siteType := reflect.TypeOf(ReferenceSite{})
	expectedSiteFields := []string{"Path", "Line", "Column", "Confidence", "Enclosing", "Source"}
	for _, f := range expectedSiteFields {
		if _, ok := siteType.FieldByName(f); !ok {
			t.Fatalf("ReferenceSite missing field %q", f)
		}
	}

	// Verify ReferenceResult fields.
	resultType := reflect.TypeOf(ReferenceResult{})
	expectedResultFields := []string{"Target", "Sites", "Backend", "Partial", "PackagesChecked", "Errors", "Truncated"}
	for _, f := range expectedResultFields {
		if _, ok := resultType.FieldByName(f); !ok {
			t.Fatalf("ReferenceResult missing field %q", f)
		}
	}

	// Test calling References on a valid workspace.
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	target := outline.Decl{
		Kind: outline.KindFunc,
		Name: "Run",
	}
	res, err := ws.References(context.Background(), target, ReferenceOptions{})
	if err != nil {
		t.Fatalf("ws.References returned unexpected error: %v", err)
	}
	if res.Target.Name != "Run" {
		t.Errorf("expected res.Target.Name == 'Run', got %q", res.Target.Name)
	}
}

// TS-05-47 (unit): Workspace.References returns ErrPathNotAllowed and empty ReferenceResult when path escapes workspace
// Verifies: 05-REQ-9.3
func TestWorkspaceReferences_PathEscape_TS_05_47(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	target := outline.Decl{
		Kind: outline.KindFunc,
		Name: "Run",
	}
	res, err := ws.References(context.Background(), target, ReferenceOptions{Path: "../escape"})
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("expected ErrPathNotAllowed, got %v", err)
	}
	if len(res.Sites) != 0 {
		t.Fatalf("expected empty Sites slice, got %d sites", len(res.Sites))
	}
}

// TS-05-48 (unit): Workspace.References returns non-nil error when path does not exist or is not a directory
// Verifies: 05-REQ-9.4
func TestWorkspaceReferences_PathNotExistOrNotDir_TS_05_48(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// Create a regular file 'main.go'.
	mainFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(mainFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	target := outline.Decl{
		Kind: outline.KindFunc,
		Name: "Run",
	}

	// Case 1: non-existent directory.
	res1, err1 := ws.References(context.Background(), target, ReferenceOptions{Path: "no_such_dir"})
	if err1 == nil {
		t.Fatalf("expected error for non-existent path, got nil")
	}
	if len(res1.Sites) != 0 {
		t.Fatalf("expected empty Sites slice for non-existent path, got %d sites", len(res1.Sites))
	}

	// Case 2: existing path is a regular file, not a directory.
	res2, err2 := ws.References(context.Background(), target, ReferenceOptions{Path: "main.go"})
	if err2 == nil {
		t.Fatalf("expected error for file path, got nil")
	}
	if len(res2.Sites) != 0 {
		t.Fatalf("expected empty Sites slice for file path, got %d sites", len(res2.Sites))
	}
}

// TS-05-49 (unit): Workspace.References returns ctx.Err() and empty ReferenceResult when context is cancelled
// Verifies: 05-REQ-9.5
func TestWorkspaceReferences_ContextCancelled_TS_05_49(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	target := outline.Decl{
		Kind: outline.KindFunc,
		Name: "Run",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := ws.References(ctx, target, ReferenceOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if len(res.Sites) != 0 {
		t.Fatalf("expected empty Sites slice, got %d sites", len(res.Sites))
	}
}

// TS-05-34 (property): find_references clamps max_results between 1 and 100 with default 30
// TS-05-50 (property): Workspace.References clamps MaxResults the same way (it shares clampMaxResults)
// Verifies: 05-REQ-6.5, 05-REQ-9.6
func TestWorkspaceReferences_ClampMaxResults_TS_05_34(t *testing.T) {
	genInts := []int{-500, -10, -1, 0, 1, 5, 25, 30, 35, 95, 100, 101, 500, 1000}
	for _, val := range genInts {
		clamped := clampMaxResults(val)
		if val <= 0 {
			if clamped != 30 {
				t.Errorf("clampMaxResults(%d) = %d, expected 30", val, clamped)
			}
		} else if val > 100 {
			if clamped != 100 {
				t.Errorf("clampMaxResults(%d) = %d, expected 100", val, clamped)
			}
		} else {
			if clamped != val {
				t.Errorf("clampMaxResults(%d) = %d, expected %d", val, clamped, val)
			}
		}
	}
}
