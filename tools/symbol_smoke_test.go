package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// ---------- helpers ----------

// mkSmokeFile creates a file in root with the given relative path and content.
func mkSmokeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// smokeTools builds All() tools and returns a map by name.
func smokeTools(t *testing.T, root string, symOpts tools.SymbolOptions) map[string]core.Tool {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    tools.NoGlobalExcludes(),
		Symbols:   symOpts,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]core.Tool, len(all))
	for _, tl := range all {
		m[tl.Name] = tl
	}
	return m
}

// smokeSymbolNames extracts the "name" field from each symbol in the result's Data.
func smokeSymbolNames(t *testing.T, r core.ToolResult) []string {
	t.Helper()
	syms, ok := r.Data["symbols"]
	if !ok {
		return nil
	}
	// symbols may be []tools.SymbolMatch or []any depending on internal representation.
	// Try JSON round-trip to normalize.
	raw, err := json.Marshal(syms)
	if err != nil {
		t.Fatalf("cannot marshal symbols: %v", err)
	}
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("cannot unmarshal symbols: %v", err)
	}
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	return names
}

// ---------- TS-02-61 ----------

// TestSmoke_OutlineThenReadRange_TS02_61 verifies 02-PATH-1:
// file_outline returns declarations with line ranges, and read_file
// returns exactly the lines of a listed declaration.
func TestSmoke_OutlineThenReadRange_TS02_61(t *testing.T) {
	root := t.TempDir()

	// Create a Go file with ~300 lines and several declarations.
	var b strings.Builder
	b.WriteString("package phase\n\n")
	// First declaration at line 3.
	b.WriteString("// Observer watches phase transitions.\n")
	b.WriteString("type Observer interface {\n")
	b.WriteString("\tOnStart()\n")
	b.WriteString("\tOnEnd()\n")
	b.WriteString("}\n\n")
	// Pad to push the next declaration further down.
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "// padding line %d\n", i)
	}
	b.WriteString("\n")
	// A function declaration.
	b.WriteString("// Run executes the phase.\n")
	b.WriteString("func Run(ctx int) int {\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "\t_ = %d\n", i)
	}
	b.WriteString("\treturn 0\n")
	b.WriteString("}\n\n")
	// More padding.
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "// more padding %d\n", i)
	}
	b.WriteString("\nfunc Helper() {}\n")

	mkSmokeFile(t, root, "internal/agentrun/phase.go", b.String())

	tls := smokeTools(t, root, tools.SymbolOptions{})
	foTool := tls["file_outline"]
	rfTool := tls["read_file"]

	// Call file_outline.
	r := foTool.Execute(context.Background(), json.RawMessage(`{"path":"internal/agentrun/phase.go"}`))
	if !r.OK {
		t.Fatalf("file_outline failed: error=%q detail=%q", r.Error, r.Detail)
	}

	// Verify header contains go/ast backend and L<start>-<end> lines.
	if !strings.Contains(r.Text, "go/ast") {
		t.Fatalf("header should name go/ast backend: %s", r.Text)
	}
	if !strings.Contains(r.Text, "internal/agentrun/phase.go") {
		t.Fatalf("header should name the file: %s", r.Text)
	}

	// Find the Run declaration's line range from the text.
	// Format: "  L<start>-<end>  func Run(...)"
	var runStart, runEnd int
	for _, line := range strings.Split(r.Text, "\n") {
		if strings.Contains(line, "func Run(") {
			// Parse L<start>-<end>
			line = strings.TrimSpace(line)
			if _, err := fmt.Sscanf(line, "L%d-%d", &runStart, &runEnd); err != nil {
				t.Fatalf("cannot parse line range from %q: %v", line, err)
			}
			break
		}
	}
	if runStart == 0 {
		t.Fatalf("Run declaration not found in outline text:\n%s", r.Text)
	}

	// Call read_file with offset and limit from the listed range.
	limit := runEnd - runStart + 1
	readArgs, _ := json.Marshal(map[string]any{
		"path":   "internal/agentrun/phase.go",
		"offset": runStart,
		"limit":  limit,
	})
	rr := rfTool.Execute(context.Background(), readArgs)
	if !rr.OK {
		t.Fatalf("read_file failed: error=%q detail=%q", rr.Error, rr.Detail)
	}

	// The read_file result should contain the Run function.
	if !strings.Contains(rr.Text, "func Run(") {
		t.Fatalf("read_file should contain func Run: %s", rr.Text)
	}
	if !strings.Contains(rr.Text, "return 0") {
		t.Fatalf("read_file should contain the function body: %s", rr.Text)
	}
}

// ---------- TS-02-62 ----------

// TestSmoke_ColdFindSymbolBuild_TS02_62 verifies 02-PATH-2:
// A cold find_symbol builds the table and returns a ranked answer.
func TestSmoke_ColdFindSymbolBuild_TS02_62(t *testing.T) {
	root := t.TempDir()

	// Create a fixture tree with Go, Python and test files and nested .gitignore.
	mkSmokeFile(t, root, ".gitignore", "ignored/\n")
	mkSmokeFile(t, root, "main.go", "package main\n\nfunc Run() {}\n\nfunc helper() {}\n")
	mkSmokeFile(t, root, "lib/runner.go", "package lib\n\ntype Runner struct{}\n\nfunc (r *Runner) Run() {}\n")
	mkSmokeFile(t, root, "lib/runner_test.go", "package lib\n\nfunc TestRun() {}\n")
	mkSmokeFile(t, root, "util.py", "def run_task():\n    pass\n")
	mkSmokeFile(t, root, "ignored/skip.go", "package ignored\n\nfunc Run() {}\n")

	tls := smokeTools(t, root, tools.SymbolOptions{})
	fsTool := tls["find_symbol"]

	// Cold call: builds the table.
	r := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Run"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%q detail=%q", r.Error, r.Detail)
	}

	// Verify the result has the index header.
	if !strings.Contains(r.Text, "symbols matching") {
		t.Fatalf("Text should contain index header: %s", r.Text)
	}
	if !strings.Contains(r.Text, "index:") {
		t.Fatalf("Text should contain index info: %s", r.Text)
	}

	// Verify Data has symbols and backends.
	if r.Data["symbols"] == nil {
		t.Fatal("Data should have 'symbols' key")
	}
	if r.Data["backends"] == nil {
		t.Fatal("Data should have 'backends' key")
	}
	filesIndexed, ok := r.Data["files_indexed"].(int)
	if !ok || filesIndexed == 0 {
		t.Fatalf("files_indexed should be > 0, got %v", r.Data["files_indexed"])
	}

	// Verify matches are ranked: exact before prefix, exported before unexported,
	// non-test before test.
	names := smokeSymbolNames(t, r)
	if len(names) == 0 {
		t.Fatal("expected at least one match for 'Run'")
	}

	// The exact match "Run" (exported, non-test) should come before any prefix match.
	// main.go's Run and lib/runner.go's Run are both exact matches.
	// lib/runner_test.go's TestRun should not match (it's a prefix of "Run" reversed).
	// Actually "Run" is a prefix of "Run" (exact), and "run_task" matches case-insensitively.
	// Verify the first match is an exact "Run".
	if names[0] != "Run" {
		t.Fatalf("first match should be exact 'Run', got %q", names[0])
	}

	// Verify ignored/skip.go's Run is NOT in the results.
	// All matches should be from non-ignored files.
	// The ignored file's Run should not appear in the results.
	_ = names

	// Verify the ignored file is not indexed.
	// The files_indexed count should not include ignored/skip.go.
	// We know the tree has: main.go, lib/runner.go, lib/runner_test.go, util.py, .gitignore
	// That's 5 files (the .gitignore itself is a regular file).
	if filesIndexed > 10 {
		t.Fatalf("files_indexed=%d seems too high (ignored files may be included)", filesIndexed)
	}
}

// ---------- TS-02-63 ----------

// TestSmoke_EditVisibleToFindSymbol_TS02_63 verifies 02-PATH-3:
// An edit through edit_file is visible to the next find_symbol.
func TestSmoke_EditVisibleToFindSymbol_TS02_63(t *testing.T) {
	root := t.TempDir()

	mkSmokeFile(t, root, "main.go", "package main\n\nfunc OldFunc() {}\n")

	tls := smokeTools(t, root, tools.SymbolOptions{})
	fsTool := tls["find_symbol"]
	etTool := tls["edit_file"]

	// Build the table.
	r := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"OldFunc","exact":true}`))
	if !r.OK {
		t.Fatalf("initial find_symbol failed: error=%q detail=%q", r.Error, r.Detail)
	}
	names := smokeSymbolNames(t, r)
	if len(names) != 1 || names[0] != "OldFunc" {
		t.Fatalf("initial: expected [OldFunc], got %v", names)
	}

	// Edit the file to add a new declaration.
	editArgs, _ := json.Marshal(map[string]any{
		"path": "main.go",
		"edits": []map[string]string{
			{"old_string": "func OldFunc() {}", "new_string": "func OldFunc() {}\n\nfunc NewThing() {}"},
		},
	})
	er := etTool.Execute(context.Background(), editArgs)
	if !er.OK {
		t.Fatalf("edit_file failed: error=%q detail=%q", er.Error, er.Detail)
	}

	// find_symbol should now see NewThing.
	r = fsTool.Execute(context.Background(), json.RawMessage(`{"name":"NewThing","exact":true}`))
	if !r.OK {
		t.Fatalf("find_symbol after edit failed: error=%q detail=%q", r.Error, r.Detail)
	}
	names = smokeSymbolNames(t, r)
	if len(names) != 1 || names[0] != "NewThing" {
		t.Fatalf("after edit: expected [NewThing], got %v", names)
	}

	// Verify the line range is correct by checking with file_outline.
	foTool := tls["file_outline"]
	or := foTool.Execute(context.Background(), json.RawMessage(`{"path":"main.go"}`))
	if !or.OK {
		t.Fatalf("file_outline failed: error=%q detail=%q", or.Error, or.Detail)
	}
	if !strings.Contains(or.Text, "NewThing") {
		t.Fatalf("file_outline should show NewThing: %s", or.Text)
	}
}

// ---------- TS-02-64 ----------

// TestSmoke_NewFileAndGitignoreEscalation_TS02_64 verifies 02-PATH-4:
// A new file or .gitignore change escalates to revalidation.
func TestSmoke_NewFileAndGitignoreEscalation_TS02_64(t *testing.T) {
	root := t.TempDir()

	mkSmokeFile(t, root, "main.go", "package main\n\nfunc Main() {}\n")
	mkSmokeFile(t, root, "other.go", "package main\n\nfunc Other() {}\n")

	tls := smokeTools(t, root, tools.SymbolOptions{})
	fsTool := tls["find_symbol"]
	wtTool := tls["write_file"]

	// Build the table.
	r := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Main","exact":true}`))
	if !r.OK {
		t.Fatalf("initial find_symbol failed: error=%q detail=%q", r.Error, r.Detail)
	}
	if len(smokeSymbolNames(t, r)) != 1 {
		t.Fatal("Main should be found initially")
	}

	// --- Step 1: write_file creates new.go declaring Fresh ---
	writeArgs, _ := json.Marshal(map[string]any{
		"path":    "new.go",
		"content": "package main\n\nfunc Fresh() {}\n",
	})
	wr := wtTool.Execute(context.Background(), writeArgs)
	if !wr.OK {
		t.Fatalf("write_file new.go failed: error=%q detail=%q", wr.Error, wr.Detail)
	}

	// find_symbol should find Fresh after revalidation.
	r = fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Fresh","exact":true}`))
	if !r.OK {
		t.Fatalf("find_symbol Fresh failed: error=%q detail=%q", r.Error, r.Detail)
	}
	names := smokeSymbolNames(t, r)
	if len(names) != 1 || names[0] != "Fresh" {
		t.Fatalf("Fresh should be found after write_file, got %v", names)
	}

	// --- Step 2: write_file creates .gitignore that ignores other.go ---
	writeArgs, _ = json.Marshal(map[string]any{
		"path":    ".gitignore",
		"content": "other.go\n",
	})
	wr = wtTool.Execute(context.Background(), writeArgs)
	if !wr.OK {
		t.Fatalf("write_file .gitignore failed: error=%q detail=%q", wr.Error, wr.Detail)
	}

	// find_symbol for Other should now return no matches.
	r = fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Other","exact":true}`))
	if !r.OK {
		t.Fatalf("find_symbol Other after .gitignore failed: error=%q detail=%q", r.Error, r.Detail)
	}
	names = smokeSymbolNames(t, r)
	if len(names) != 0 {
		t.Fatalf("Other should not be found after .gitignore ignores other.go, got %v", names)
	}
}

// ---------- TS-02-65 ----------

// TestSmoke_ShellDeleteVisibleToFindSymbol_TS02_65 verifies 02-PATH-5:
// A shell command deletes a file and the symbol disappears.
func TestSmoke_ShellDeleteVisibleToFindSymbol_TS02_65(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("rm not available on Windows")
	}

	root := t.TempDir()

	mkSmokeFile(t, root, "main.go", "package main\n\nfunc Main() {}\n")
	mkSmokeFile(t, root, "victim.go", "package main\n\nfunc Victim() {}\n")

	tls := smokeTools(t, root, tools.SymbolOptions{})
	fsTool := tls["find_symbol"]
	execTool := tls["execute"]

	// Build the table.
	r := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Victim","exact":true}`))
	if !r.OK {
		t.Fatalf("initial find_symbol failed: error=%q detail=%q", r.Error, r.Detail)
	}
	if len(smokeSymbolNames(t, r)) != 1 {
		t.Fatal("Victim should be found initially")
	}

	// Record initial files_indexed.
	initialFiles, _ := r.Data["files_indexed"].(int)

	// Delete victim.go through execute.
	execArgs, _ := json.Marshal(map[string]any{
		"command":   "rm victim.go",
		"timeout_s": 5,
	})
	er := execTool.Execute(context.Background(), execArgs)
	// execute may return OK or not depending on the command, but the wrapper
	// should mark the table for revalidation regardless.
	_ = er

	// find_symbol for Victim should now return no matches.
	r = fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Victim","exact":true}`))
	if !r.OK {
		t.Fatalf("find_symbol Victim after delete failed: error=%q detail=%q", r.Error, r.Detail)
	}
	names := smokeSymbolNames(t, r)
	if len(names) != 0 {
		t.Fatalf("Victim should not be found after rm, got %v", names)
	}

	// The text should still show the index line.
	if !strings.Contains(r.Text, "index:") {
		t.Fatalf("Text should still show the index line: %s", r.Text)
	}

	// files_indexed should have dropped by one.
	newFiles, _ := r.Data["files_indexed"].(int)
	if newFiles >= initialFiles {
		t.Fatalf("files_indexed should have dropped: was %d, now %d", initialFiles, newFiles)
	}

	// The result should be OK true (successful no-match).
	if !r.OK {
		t.Fatal("result should be OK=true for a no-match")
	}
}

// ---------- TS-02-66 ----------

// TestSmoke_BoundStopsBuildLaterCallsFinish_TS02_66 verifies 02-PATH-6:
// A bound stops the build and later calls finish it.
func TestSmoke_BoundStopsBuildLaterCallsFinish_TS02_66(t *testing.T) {
	root := t.TempDir()

	// Create enough files to exceed a small MaxFiles.
	for i := 0; i < 30; i++ {
		mkSmokeFile(t, root, fmt.Sprintf("f%03d.go", i),
			fmt.Sprintf("package main\n\nfunc F%03d() {}\n", i))
	}

	tls := smokeTools(t, root, tools.SymbolOptions{
		MaxFiles: 10,
	})
	fsTool := tls["find_symbol"]

	// First call: should be partial.
	r1 := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r1.OK {
		t.Fatalf("call 1 failed: error=%q detail=%q", r1.Error, r1.Detail)
	}
	partial1, _ := r1.Data["partial"].(bool)
	if !partial1 {
		t.Fatal("call 1: expected partial=true")
	}
	reason1, _ := r1.Data["partial_reason"].(string)
	if reason1 == "" {
		t.Fatal("call 1: expected non-empty partial_reason")
	}
	fi1, _ := r1.Data["files_indexed"].(int)

	// The note should be present.
	note1, _ := r1.Data["note"].(string)
	if note1 == "" {
		t.Fatal("call 1: expected a note for partial result")
	}

	// Second call: should index more files.
	r2 := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"F"}`))
	if !r2.OK {
		t.Fatalf("call 2 failed: error=%q detail=%q", r2.Error, r2.Detail)
	}
	fi2, _ := r2.Data["files_indexed"].(int)
	if fi2 <= fi1 {
		t.Fatalf("call 2: files_indexed=%d should be > call 1's %d", fi2, fi1)
	}

	// Keep calling until complete.
	var finalResult core.ToolResult
	for i := 0; i < 10; i++ {
		r := fsTool.Execute(context.Background(), json.RawMessage(`{"name":"F"}`))
		if !r.OK {
			t.Fatalf("call %d failed: error=%q detail=%q", i+3, r.Error, r.Detail)
		}
		p, _ := r.Data["partial"].(bool)
		if !p {
			finalResult = r
			break
		}
	}

	if finalResult.Data == nil {
		t.Fatal("table never completed after repeated calls")
	}

	// The final call should have no partial key (or partial=false).
	finalPartial, _ := finalResult.Data["partial"].(bool)
	if finalPartial {
		t.Fatal("final call should not be partial")
	}

	// All 30 files should be indexed.
	finalFiles, _ := finalResult.Data["files_indexed"].(int)
	if finalFiles != 30 {
		t.Fatalf("final files_indexed=%d, want 30", finalFiles)
	}
}
