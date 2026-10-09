package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// TS-02-9: file_outline header and declaration lines have the pinned format
func TestOutlineHeaderAndLines_TS02_9(t *testing.T) {
	root := t.TempDir()

	// A Go file with:
	// - a multi-line exported interface (lines 3-7)
	// - a multi-line exported method (lines 9-15)
	// - a one-line exported const (line 17)
	// - 2 unexported declarations (lines 19, 21)
	// Total: 5 declarations, 2 unexported
	src := `package x

type Observer interface {
	OnStart()
	OnEnd()
}

func (r *Runner) Run(ctx context.Context) (Result, error) {
	// body
	// body
	// body
	// body
	return Result{}, nil
}

const MaxRetries = 3

var helper = 1

func internal() {}
`
	mkOutlineFile(t, root, "x.go", src)

	exec := makeOutlineTool(t, root)
	r := exec(context.Background(), json.RawMessage(`{"path":"x.go"}`))
	if !r.OK {
		t.Fatalf("call failed: error=%s detail=%s", r.Error, r.Detail)
	}

	lines := strings.Split(r.Text, "\n")
	if len(lines) < 1 {
		t.Fatal("expected at least a header line")
	}

	// Header: x.go  (go/ast, 5 declarations, 2 unexported not listed)
	header := lines[0]
	if !strings.HasPrefix(header, "x.go  (go/ast, ") {
		t.Fatalf("header prefix wrong: %q", header)
	}
	if !strings.Contains(header, "declarations") {
		t.Fatalf("header should contain 'declarations': %q", header)
	}
	if !strings.Contains(header, "unexported not listed") {
		t.Fatalf("header should contain 'unexported not listed': %q", header)
	}

	// Multi-line declarations render L<start>-<end>
	foundMultiLine := false
	lineRangeRe := regexp.MustCompile(`^\s+L\d+-\d+\s+`)
	for _, l := range lines[1:] {
		if lineRangeRe.MatchString(l) {
			foundMultiLine = true
			break
		}
	}
	if !foundMultiLine {
		t.Fatalf("expected at least one multi-line declaration with L<start>-<end>; text:\n%s", r.Text)
	}

	// One-line declarations render L<start> alone (no dash)
	singleLineRe := regexp.MustCompile(`^\s+L\d+\s+`)
	singleLineDashRe := regexp.MustCompile(`^\s+L\d+-\d+`)
	foundSingleLine := false
	for _, l := range lines[1:] {
		if singleLineRe.MatchString(l) && !singleLineDashRe.MatchString(l) {
			foundSingleLine = true
			break
		}
	}
	if !foundSingleLine {
		t.Fatalf("expected at least one single-line declaration with L<start> (no dash); text:\n%s", r.Text)
	}

	// Declarations appear in source order: Observer before Run before MaxRetries
	observerIdx, runIdx, maxRetriesIdx := -1, -1, -1
	for i, l := range lines {
		if strings.Contains(l, "Observer") {
			observerIdx = i
		}
		if strings.Contains(l, "Run") {
			runIdx = i
		}
		if strings.Contains(l, "MaxRetries") {
			maxRetriesIdx = i
		}
	}
	if observerIdx < 0 || runIdx < 0 || maxRetriesIdx < 0 {
		t.Fatalf("expected Observer, Run and MaxRetries in output; text:\n%s", r.Text)
	}
	if !(observerIdx < runIdx && runIdx < maxRetriesIdx) {
		t.Fatalf("declarations not in source order: Observer@%d, Run@%d, MaxRetries@%d",
			observerIdx, runIdx, maxRetriesIdx)
	}

	// Only 3 exported declarations listed (Observer, Run, MaxRetries)
	declLines := 0
	for _, l := range lines[1:] {
		if strings.HasPrefix(strings.TrimSpace(l), "L") {
			declLines++
		}
	}
	if declLines != 3 {
		t.Fatalf("expected 3 listed declarations, got %d; text:\n%s", declLines, r.Text)
	}
}

// TS-02-10: include_private lists unexported declarations and drops the unexported clause
func TestOutlineIncludePrivate_TS02_10(t *testing.T) {
	root := t.TempDir()

	// 3 exported, 2 unexported = 5 total
	src := `package x

func Alpha() {}
func Beta() {}
func Gamma() {}
func delta() {}
func epsilon() {}
`
	mkOutlineFile(t, root, "x.go", src)

	exec := makeOutlineTool(t, root)

	// Case 1: include_private false (explicit)
	r1 := exec(context.Background(), json.RawMessage(`{"path":"x.go","include_private":false}`))
	if !r1.OK {
		t.Fatalf("call failed: %s %s", r1.Error, r1.Detail)
	}
	lines1 := strings.Split(r1.Text, "\n")
	// Header should say "2 unexported not listed"
	if !strings.Contains(lines1[0], "2 unexported not listed") {
		t.Fatalf("false: header should say '2 unexported not listed': %q", lines1[0])
	}
	// Should list 3 lines
	declCount1 := countDeclLines(lines1[1:])
	if declCount1 != 3 {
		t.Fatalf("false: expected 3 listed, got %d; text:\n%s", declCount1, r1.Text)
	}
	// Total in header should be 5
	if !strings.Contains(lines1[0], "5 declarations") {
		t.Fatalf("false: header should say '5 declarations': %q", lines1[0])
	}

	// Case 2: include_private omitted (default false)
	r2 := exec(context.Background(), json.RawMessage(`{"path":"x.go"}`))
	if !r2.OK {
		t.Fatalf("call failed: %s %s", r2.Error, r2.Detail)
	}
	lines2 := strings.Split(r2.Text, "\n")
	if !strings.Contains(lines2[0], "2 unexported not listed") {
		t.Fatalf("omitted: header should say '2 unexported not listed': %q", lines2[0])
	}
	declCount2 := countDeclLines(lines2[1:])
	if declCount2 != 3 {
		t.Fatalf("omitted: expected 3 listed, got %d; text:\n%s", declCount2, r2.Text)
	}

	// Case 3: include_private true
	r3 := exec(context.Background(), json.RawMessage(`{"path":"x.go","include_private":true}`))
	if !r3.OK {
		t.Fatalf("call failed: %s %s", r3.Error, r3.Detail)
	}
	lines3 := strings.Split(r3.Text, "\n")
	// Header should NOT say "unexported not listed"
	if strings.Contains(lines3[0], "unexported not listed") {
		t.Fatalf("true: header should NOT say 'unexported not listed': %q", lines3[0])
	}
	// Should list 5 lines
	declCount3 := countDeclLines(lines3[1:])
	if declCount3 != 5 {
		t.Fatalf("true: expected 5 listed, got %d; text:\n%s", declCount3, r3.Text)
	}
	// Total in header should be 5
	if !strings.Contains(lines3[0], "5 declarations") {
		t.Fatalf("true: header should say '5 declarations': %q", lines3[0])
	}
}

// TS-02-11: A declaration under a container is indented unless its signature shows the container
func TestOutlineContainerIndentation_TS02_11(t *testing.T) {
	root := t.TempDir()

	// Python class with a method — the method's signature does NOT contain the class name,
	// so it should be indented further.
	// We use a fake runner to produce controlled outline output.
	pySrc := `class MyClass:
    def my_method(self):
        pass

def top_level():
    pass
`
	mkOutlineFile(t, root, "mod.py", pySrc)

	// Go method — the receiver in the signature contains the type name,
	// so it should NOT be indented further.
	goSrc := `package x

type Runner struct{}

func (r *Runner) Run() {}

func TopLevel() {}
`
	mkOutlineFile(t, root, "x.go", goSrc)

	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Test Go file: go/ast backend sets Container for methods
	goFS := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
	}.withDefaults())
	goTool := goFS.fileOutlineTool()
	goR := goTool.Execute(context.Background(), json.RawMessage(`{"path":"x.go"}`))
	if !goR.OK {
		t.Fatalf("Go outline failed: %s %s", goR.Error, goR.Detail)
	}

	goLines := strings.Split(goR.Text, "\n")
	// Find the Run method line — its signature contains "Runner" (the container),
	// so it should NOT be indented further (just 2 spaces)
	for _, l := range goLines[1:] {
		if strings.Contains(l, "Run") {
			// Should start with exactly "  L" (2 spaces), not "    L" (4 spaces)
			if strings.HasPrefix(l, "    ") {
				t.Fatalf("Go method with receiver in signature should NOT be extra-indented: %q", l)
			}
			if !strings.HasPrefix(l, "  L") {
				t.Fatalf("Go method should start with '  L': %q", l)
			}
			break
		}
	}

	// For Python, we test with a direct call to renderOutlineResult using a
	// crafted outline.File that has Container set but signature without the container name.
	pyFile := outline.File{
		Path:    "mod.py",
		Lang:    "Python",
		Backend: outline.BackendTreeSitter,
		Decls: []outline.Decl{
			{Kind: outline.KindClass, Name: "MyClass", Signature: "class MyClass", Exported: true, StartLine: 1, EndLine: 6},
			{Kind: outline.KindMethod, Name: "my_method", Container: "MyClass", Signature: "def my_method(self)", Exported: true, StartLine: 2, EndLine: 3},
			{Kind: outline.KindFunc, Name: "top_level", Signature: "def top_level()", Exported: true, StartLine: 5, EndLine: 6},
		},
	}
	pyAbs := filepath.Join(root, "mod.py")
	pyR := renderOutlineResult(ws, pyAbs, pyFile, false, nil, 0)

	pyLines := strings.Split(pyR.Text, "\n")
	// Find the my_method line — its signature "def my_method(self)" does NOT contain "MyClass",
	// so it should be indented 4 spaces
	foundMethod := false
	for _, l := range pyLines[1:] {
		if strings.Contains(l, "my_method") {
			foundMethod = true
			if !strings.HasPrefix(l, "    L") {
				t.Fatalf("Python method without container in signature should be extra-indented (4 spaces): %q", l)
			}
			break
		}
	}
	if !foundMethod {
		t.Fatalf("expected my_method in output; text:\n%s", pyR.Text)
	}

	// top_level should be at normal indent (2 spaces)
	for _, l := range pyLines[1:] {
		if strings.Contains(l, "top_level") {
			if strings.HasPrefix(l, "    ") {
				t.Fatalf("top-level function should NOT be extra-indented: %q", l)
			}
			if !strings.HasPrefix(l, "  L") {
				t.Fatalf("top-level function should start with '  L': %q", l)
			}
			break
		}
	}
}

// TS-02-12: A file without declarations renders one explanatory line, with a neutral reason when the backend is none
func TestOutlineNoDeclarations_TS02_12(t *testing.T) {
	root := t.TempDir()

	// 1. Empty Go file — go/ast backend, no declarations
	mkOutlineFile(t, root, "empty.go", "package empty\n")

	// 2. Unknown extension file — none backend, "language not recognised"
	mkOutlineFile(t, root, "data.xyz", "some data\n")

	// 3. Binary file — none backend, "binary"
	binContent := make([]byte, 100)
	binContent[10] = 0 // NUL byte makes it binary
	copy(binContent, []byte("package x\n"))
	binContent[10] = 0
	mkOutlineFile(t, root, "binary.go", string(binContent))

	// 4. Oversized file — none backend, "file too large"
	// We need to create a file larger than outline's default MaxFileBytes (1 MiB).
	// Instead, we test via renderOutlineResult with a crafted File.

	exec := makeOutlineTool(t, root)

	// Test 1: Empty Go file
	t.Run("empty_go", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"path":"empty.go"}`))
		if !r.OK {
			t.Fatalf("call failed: %s %s", r.Error, r.Detail)
		}
		lines := strings.Split(r.Text, "\n")
		if len(lines) < 2 {
			t.Fatalf("expected header + explanatory line; got:\n%s", r.Text)
		}
		// Header should be present
		if !strings.Contains(lines[0], "go/ast") {
			t.Fatalf("header should mention go/ast: %q", lines[0])
		}
		// Second line should say "No declarations found"
		if !strings.Contains(r.Text, "No declarations found") {
			t.Fatalf("should say 'No declarations found'; got:\n%s", r.Text)
		}
		// Should NOT suggest read_file/search_files (backend is go/ast, not none)
		if strings.Contains(lines[1], "read_file") || strings.Contains(lines[1], "search_files") {
			t.Fatalf("go/ast backend should NOT suggest read_file/search_files: %q", lines[1])
		}
		// Should NOT be an error
		if !r.OK {
			t.Fatalf("empty Go file should not be an error")
		}
	})

	// Test 2: Unknown extension
	t.Run("unknown_ext", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"path":"data.xyz"}`))
		if !r.OK {
			t.Fatalf("call failed: %s %s", r.Error, r.Detail)
		}
		if !strings.Contains(r.Text, "language not recognised") {
			t.Fatalf("should say 'language not recognised'; got:\n%s", r.Text)
		}
		if !strings.Contains(r.Text, "read_file") || !strings.Contains(r.Text, "search_files") {
			t.Fatalf("none-backend should suggest read_file or search_files; got:\n%s", r.Text)
		}
	})

	// Test 3: Binary file
	t.Run("binary", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"path":"binary.go"}`))
		if !r.OK {
			t.Fatalf("call failed: %s %s", r.Error, r.Detail)
		}
		if !strings.Contains(r.Text, "binary") {
			t.Fatalf("should mention 'binary'; got:\n%s", r.Text)
		}
		if !strings.Contains(r.Text, "read_file") || !strings.Contains(r.Text, "search_files") {
			t.Fatalf("none-backend should suggest read_file or search_files; got:\n%s", r.Text)
		}
	})

	// Test 4: Oversized file — test via renderOutlineResult with crafted File
	t.Run("oversized", func(t *testing.T) {
		ws, err := NewWorkspace(root)
		if err != nil {
			t.Fatal(err)
		}
		// Create a file that outline considers "too large"
		// We simulate this by creating a file and calling renderOutlineResult
		// with a none-backend File that has Lang set (meaning it was recognised
		// but too large or binary).
		oversizedPath := filepath.Join(root, "big.go")
		// Write a small file but pretend outline returned none with Lang set
		if err := os.WriteFile(oversizedPath, []byte("package big\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ofile := outline.File{
			Path:    "big.go",
			Lang:    "Go",
			Backend: outline.BackendNone,
			Decls:   []outline.Decl{},
		}
		// Pass a large source to trigger "file too large" detection
		bigSrc := bytes.Repeat([]byte("x"), 2*1024*1024) // 2 MiB
		r := renderOutlineResult(ws, oversizedPath, ofile, false, bigSrc, int64(len(bigSrc)))
		if !r.OK {
			t.Fatalf("oversized file should not be an error; error=%s detail=%s", r.Error, r.Detail)
		}
		if !strings.Contains(r.Text, "file too large") {
			t.Fatalf("should say 'file too large'; got:\n%s", r.Text)
		}
		if !strings.Contains(r.Text, "read_file") || !strings.Contains(r.Text, "search_files") {
			t.Fatalf("none-backend should suggest read_file or search_files; got:\n%s", r.Text)
		}
	})
}

// TS-02-13: file_outline Data carries file, backend, declarations and listed
func TestOutlineData_TS02_13(t *testing.T) {
	root := t.TempDir()

	// 4 declarations total, 1 unexported = 3 listed by default
	src := `package x

func Alpha() {}
func Beta() {}
func Gamma() {}
func delta() {}
`
	mkOutlineFile(t, root, "x.go", src)

	exec := makeOutlineTool(t, root)
	r := exec(context.Background(), json.RawMessage(`{"path":"x.go"}`))
	if !r.OK {
		t.Fatalf("call failed: %s %s", r.Error, r.Detail)
	}

	d := r.Data
	if d == nil {
		t.Fatal("Data should not be nil")
	}

	// Check declarations count
	decls, ok := d["declarations"]
	if !ok {
		t.Fatal("Data should have 'declarations' key")
	}
	if decls != 4 {
		t.Fatalf("declarations = %v, want 4", decls)
	}

	// Check listed count
	listed, ok := d["listed"]
	if !ok {
		t.Fatal("Data should have 'listed' key")
	}
	if listed != 3 {
		t.Fatalf("listed = %v, want 3", listed)
	}

	// Check backend
	backend, ok := d["backend"]
	if !ok {
		t.Fatal("Data should have 'backend' key")
	}
	if backend != "go/ast" {
		t.Fatalf("backend = %v, want 'go/ast'", backend)
	}

	// Check file
	f, ok := d["file"]
	if !ok {
		t.Fatal("Data should have 'file' key")
	}
	ofile, ok := f.(outline.File)
	if !ok {
		t.Fatalf("file should be outline.File, got %T", f)
	}
	if len(ofile.Decls) != 4 {
		t.Fatalf("file.Decls should have 4 entries, got %d", len(ofile.Decls))
	}
}

// countDeclLines counts lines that start with whitespace followed by L<number>
func countDeclLines(lines []string) int {
	count := 0
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "L") && len(trimmed) > 1 && trimmed[1] >= '0' && trimmed[1] <= '9' {
			count++
		}
	}
	return count
}

// TestANoneOutlineSaysThereIsNoBackend (issue #89): a recognised, readable,
// text file with no outline is a language with no backend in this build —
// any language but Go without cgo — and the line says so rather than
// leaving the model to guess (spec 02 §1: the line "says why").
func TestANoneOutlineSaysThereIsNoBackend(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	src := []byte("<?php\nfunction hello() {}\n")
	f := outline.File{Path: "a.php", Lang: outline.LangPHP, Backend: outline.BackendNone, Decls: []outline.Decl{}}
	r := renderOutlineResult(ws, filepath.Join(root, "a.php"), f, false, src, int64(len(src)))
	if !strings.Contains(r.Text, "no outline backend for PHP in this build") {
		t.Fatalf("none line does not say why:\n%s", r.Text)
	}
}
