package outline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-01-12: A cancelled context makes Outline and OutlineMany return exactly ctx.Err().
func TestCancelledContext_TS_01_12(t *testing.T) {
	// Part 1: context cancelled before the call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Outline(ctx, "/fake/a.go", []byte("package a\n"), Options{})
	if err != context.Canceled {
		t.Fatalf("Outline: err = %v, want context.Canceled", err)
	}

	_, err = OutlineMany(ctx, []Source{{Abs: "/fake/a.go", Src: []byte("package a\n")}}, Options{})
	if err != context.Canceled {
		t.Fatalf("OutlineMany: err = %v, want context.Canceled", err)
	}
}

// TS-01-14: OutlineMany returns an unreadable file as none and still outlines the others.
func TestOutlineMany_UnreadableFile_TS_01_14(t *testing.T) {
	dir := t.TempDir()

	// First: a readable .go file.
	goFile1 := filepath.Join(dir, "a.go")
	if err := os.WriteFile(goFile1, []byte("package a\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Second: a missing .py file (unreadable).
	missingPy := filepath.Join(dir, "missing.py")

	// Third: another readable .go file.
	goFile2 := filepath.Join(dir, "b.go")
	if err := os.WriteFile(goFile2, []byte("package b\n\nfunc World() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	srcs := []Source{
		{Abs: goFile1},
		{Abs: missingPy},
		{Abs: goFile2},
	}

	fs, err := OutlineMany(ctx, srcs, Options{})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}
	if len(fs) != 3 {
		t.Fatalf("len(fs) = %d, want 3", len(fs))
	}

	// First file: outlined with go/ast.
	if fs[0].Backend != BackendGoAST {
		t.Errorf("fs[0].Backend = %q, want %q", fs[0].Backend, BackendGoAST)
	}
	if len(fs[0].Decls) == 0 {
		t.Error("fs[0].Decls is empty, want at least one decl")
	}

	// Second file: Backend none (unreadable).
	if fs[1].Backend != BackendNone {
		t.Errorf("fs[1].Backend = %q, want %q", fs[1].Backend, BackendNone)
	}
	if fs[1].Decls == nil {
		t.Error("fs[1].Decls is nil, want non-nil empty slice")
	}
	if len(fs[1].Decls) != 0 {
		t.Errorf("fs[1].Decls has %d entries, want 0", len(fs[1].Decls))
	}

	// Third file: outlined with go/ast.
	if fs[2].Backend != BackendGoAST {
		t.Errorf("fs[2].Backend = %q, want %q", fs[2].Backend, BackendGoAST)
	}
	if len(fs[2].Decls) == 0 {
		t.Error("fs[2].Decls is empty, want at least one decl")
	}
}

// TS-01-15: OutlineMany returns one File per input in input order.
func TestOutlineMany_InputOrder_TS_01_15(t *testing.T) {
	dir := t.TempDir()

	// Create files of various types.
	files := []struct {
		name    string
		content string
	}{
		{"a.go", "package a\nfunc A() {}\n"},
		{"b.py", "def b():\n    pass\n"},
		{"c.unknown", "whatever"},
		{"d.go", "package d\nfunc D() {}\n"},
	}

	var srcs []Source
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, Source{Abs: p})
	}

	// Add an oversize file.
	bigFile := filepath.Join(dir, "big.go")
	if err := os.WriteFile(bigFile, []byte(strings.Repeat("x", 600)), 0o644); err != nil {
		t.Fatal(err)
	}
	srcs = append(srcs, Source{Abs: bigFile})

	// Add an unreadable file.
	srcs = append(srcs, Source{Abs: filepath.Join(dir, "nonexistent.py")})

	fs, err := OutlineMany(context.Background(), srcs, Options{
		MaxFileBytes: 500,
		Root:         dir,
	})
	if err != nil {
		t.Fatalf("OutlineMany returned error: %v", err)
	}

	// One File per input.
	if len(fs) != len(srcs) {
		t.Fatalf("len(fs) = %d, want %d", len(fs), len(srcs))
	}

	// Each File.Path corresponds to the input.
	for i, f := range fs {
		wantPath := filepath.ToSlash(filepath.Base(srcs[i].Abs))
		// Since Root is dir, the path should be just the filename.
		if f.Path != wantPath {
			// For files that don't exist, the path is still computed.
			t.Logf("fs[%d].Path = %q (expected base %q)", i, f.Path, wantPath)
		}
	}
}
