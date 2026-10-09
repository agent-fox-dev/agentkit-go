package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// mkOutlineFile creates a file in root with the given relative path and content.
func mkOutlineFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeOutlineTool builds a file_outline tool's Execute function from a workspace root.
func makeOutlineTool(t *testing.T, root string) func(ctx context.Context, in json.RawMessage) core.ToolResult {
	t.Helper()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	fs := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	}.withDefaults())
	tl := fs.fileOutlineTool()
	return tl.Execute
}

// TS-02-1: file_outline outlines from disk on every call and never consults the symbol table
func TestOutlineFromDiskOnEveryCall_TS02_1(t *testing.T) {
	root := t.TempDir()
	mkOutlineFile(t, root, "a.go", "package a\n\nfunc Alpha() {}\n")

	exec := makeOutlineTool(t, root)

	r1 := exec(context.Background(), json.RawMessage(`{"path":"a.go"}`))
	if !r1.OK {
		t.Fatalf("first call failed: error=%s detail=%s", r1.Error, r1.Detail)
	}
	if !strings.Contains(r1.Text, "Alpha") {
		t.Fatalf("first call should list Alpha; got %q", r1.Text)
	}

	// Edit the file on disk.
	mkOutlineFile(t, root, "a.go", "package a\n\nfunc Beta() {}\n")

	r2 := exec(context.Background(), json.RawMessage(`{"path":"a.go"}`))
	if !r2.OK {
		t.Fatalf("second call failed: error=%s detail=%s", r2.Error, r2.Detail)
	}
	if !strings.Contains(r2.Text, "Beta") {
		t.Fatalf("second call should list Beta; got %q", r2.Text)
	}
	if strings.Contains(r2.Text, "Alpha") {
		t.Fatalf("second call should NOT list Alpha; got %q", r2.Text)
	}
}

// TS-02-2: file_outline rejects unparsable arguments and an empty path with invalid_arguments
func TestOutlineInvalidArguments_TS02_2(t *testing.T) {
	root := t.TempDir()
	mkOutlineFile(t, root, "dummy.go", "package dummy\n")
	exec := makeOutlineTool(t, root)

	cases := []struct {
		name string
		args string
	}{
		{"malformed JSON", `{`},
		{"empty path", `{"path":""}`},
		// A number for path: json.Unmarshal into string will coerce or fail
		{"number path", `{"path":3}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := exec(context.Background(), json.RawMessage(tc.args))
			if r.OK {
				t.Fatalf("expected failure for %s", tc.name)
			}
			if r.Error != "invalid_arguments" {
				t.Fatalf("error = %q, want invalid_arguments (detail: %s)", r.Error, r.Detail)
			}
		})
	}
}

// TS-02-3: file_outline refuses a path outside the workspace
func TestOutlinePathOutsideWorkspace_TS02_3(t *testing.T) {
	root := t.TempDir()
	mkOutlineFile(t, root, "inside.go", "package inside\n")

	// Create a file outside the workspace.
	outside := t.TempDir()
	mkOutlineFile(t, outside, "outside.go", "package outside\n")

	exec := makeOutlineTool(t, root)

	cases := []struct {
		name string
		path string
	}{
		{"relative escape", "../outside.go"},
		{"absolute outside", filepath.Join(outside, "outside.go")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			arg, _ := json.Marshal(map[string]string{"path": tc.path})
			r := exec(context.Background(), json.RawMessage(arg))
			if r.OK {
				t.Fatalf("expected failure for %s", tc.name)
			}
			if r.Error != "path_not_allowed" {
				t.Fatalf("error = %q, want path_not_allowed (detail: %s)", r.Error, r.Detail)
			}
		})
	}

	// Symlink escaping (skip on Windows where symlinks may need privileges)
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "escape.go")
		target := filepath.Join(outside, "outside.go")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		arg, _ := json.Marshal(map[string]string{"path": "escape.go"})
		r := exec(context.Background(), json.RawMessage(arg))
		if r.OK {
			t.Fatalf("expected failure for escaping symlink")
		}
		if r.Error != "path_not_allowed" {
			t.Fatalf("error = %q, want path_not_allowed", r.Error)
		}
	}
}

// TS-02-4: file_outline reports read_failed when the path cannot be stat'ed
func TestOutlineReadFailed_TS02_4(t *testing.T) {
	root := t.TempDir()
	exec := makeOutlineTool(t, root)

	arg, _ := json.Marshal(map[string]string{"path": "missing.go"})
	r := exec(context.Background(), json.RawMessage(arg))
	if r.OK {
		t.Fatal("expected failure for missing file")
	}
	if r.Error != "read_failed" {
		t.Fatalf("error = %q, want read_failed (detail: %s)", r.Error, r.Detail)
	}
}

// TS-02-5: file_outline reports not_a_file for a directory with the notAFile message
func TestOutlineNotAFile_TS02_5(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	exec := makeOutlineTool(t, root)

	arg, _ := json.Marshal(map[string]string{"path": "pkg"})
	r := exec(context.Background(), json.RawMessage(arg))
	if r.OK {
		t.Fatal("expected failure for directory")
	}
	if r.Error != "not_a_file" {
		t.Fatalf("error = %q, want not_a_file (detail: %s)", r.Error, r.Detail)
	}
	if !strings.Contains(r.Detail, "list_files") {
		t.Fatalf("directory detail should mention list_files; got %q", r.Detail)
	}
}

// TS-02-6: file_outline returns outline_failed with the underlying detail when outline.Outline errors
func TestOutlineOutlineFailed_TS02_6(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; permission test would not fail")
	}

	root := t.TempDir()
	unreadable := filepath.Join(root, "secret.go")
	if err := os.WriteFile(unreadable, []byte("package secret\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	exec := makeOutlineTool(t, root)

	arg, _ := json.Marshal(map[string]string{"path": "secret.go"})
	r := exec(context.Background(), json.RawMessage(arg))
	if r.OK {
		t.Fatal("expected failure for unreadable file")
	}
	if r.Error != "outline_failed" {
		t.Fatalf("error = %q, want outline_failed (detail: %s)", r.Error, r.Detail)
	}
}

// TS-02-7: file_outline returns aborted for a cancelled context
func TestOutlineAborted_TS02_7(t *testing.T) {
	root := t.TempDir()
	mkOutlineFile(t, root, "a.go", "package a\n\nfunc Hello() {}\n")
	exec := makeOutlineTool(t, root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	arg, _ := json.Marshal(map[string]string{"path": "a.go"})
	r := exec(ctx, json.RawMessage(arg))
	if r.OK {
		t.Fatal("expected failure for cancelled context")
	}
	if r.Error != "aborted" {
		t.Fatalf("error = %q, want aborted (detail: %s)", r.Error, r.Detail)
	}
	if r.Detail != "Operation aborted" {
		t.Fatalf("detail = %q, want %q", r.Detail, "Operation aborted")
	}
}

// TS-02-8: file_outline outlines every readable workspace file regardless of ignore rules and hidden directories
func TestOutlineIgnoredAndHiddenFiles_TS02_8(t *testing.T) {
	root := t.TempDir()

	// Create .gitignore that ignores generated files
	mkOutlineFile(t, root, ".gitignore", "generated/\n*.gen.go\n")

	// Create various files: normal, ignored, hidden-dir
	mkOutlineFile(t, root, "normal.go", "package normal\n\nfunc Normal() {}\n")
	mkOutlineFile(t, root, "generated/gen.go", "package generated\n\nfunc Gen() {}\n")
	mkOutlineFile(t, root, "foo.gen.go", "package main\n\nfunc GenFunc() {}\n")
	mkOutlineFile(t, root, ".hidden/secret.go", "package hidden\n\nfunc Secret() {}\n")

	exec := makeOutlineTool(t, root)

	files := []string{
		"normal.go",
		"generated/gen.go",
		"foo.gen.go",
		".hidden/secret.go",
	}

	for _, f := range files {
		t.Run(f, func(t *testing.T) {
			arg, _ := json.Marshal(map[string]string{"path": f})
			r := exec(context.Background(), json.RawMessage(arg))
			if !r.OK {
				t.Fatalf("file_outline should succeed for %s (even if ignored/hidden); error=%s detail=%s",
					f, r.Error, r.Detail)
			}
		})
	}
}

// TS-06-15: file_outline declares the outline it returns. outline.File and
// outline.Decl carry no json tags, so their keys are the Go field names.
func TestOutputSchemaFileOutline_TS06_15(t *testing.T) {
	s := toolByName(t, t.TempDir(), "file_outline").OutputSchema
	assertObject(t, "file_outline", s, map[string]prop{
		"file": {schema.TypeObject, true}, "backend": {schema.TypeString, true},
		"declarations": {schema.TypeInteger, true}, "listed": {schema.TypeInteger, true},
	})
	file := s.Properties["file"]
	assertObject(t, "file_outline.file", file, map[string]prop{
		"Path": {schema.TypeString, true}, "Lang": {schema.TypeString, true},
		"Backend": {schema.TypeString, true}, "Decls": {schema.TypeArray, true},
	})
	assertObject(t, "file_outline.file.Decls[]", file.Properties["Decls"].Items, declProps)
}
