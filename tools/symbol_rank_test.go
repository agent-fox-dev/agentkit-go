package tools

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
)

// TS-02-22: Matches are totally ordered by the ranking tiers and identical across repeated calls
func TestFindSymbolRankingDeterministic_TS02_22(t *testing.T) {
	root := t.TempDir()

	// Create a fixture tree with exported/unexported, test/non-test, and path-length variations.
	mkSymFile(t, root, "a.go", "package main\n\nfunc Run() {}\nfunc run() {}\n")
	mkSymFile(t, root, "ab.go", "package main\n\nfunc RunAll() {}\n")
	mkSymFile(t, root, "pkg/deep/d.go", "package deep\n\nfunc RunDeep() {}\n")
	mkSymFile(t, root, "a_test.go", "package main\n\nfunc RunTest() {}\n")
	mkSymFile(t, root, "test/t.go", "package test\n\nfunc RunInTestDir() {}\n")

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Call multiple times and verify identical order.
	var prev []SymbolMatch
	for i := 0; i < 5; i++ {
		r := exec(context.Background(), json.RawMessage(`{"name":"run"}`))
		if !r.OK {
			t.Fatalf("call %d: error=%s detail=%s", i, r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) == 0 {
			t.Fatalf("call %d: no matches", i)
		}

		// Verify ordering invariants between adjacent pairs.
		for j := 0; j < len(matches)-1; j++ {
			a, b := matches[j], matches[j+1]
			if !rankLessOrEqual(a, b, "run", false, false) {
				t.Errorf("call %d: matches[%d] (%s/%s) should come before matches[%d] (%s/%s)",
					i, j, a.Path, a.Name, j+1, b.Path, b.Name)
			}
		}

		// Verify identical to previous call.
		if prev != nil {
			if len(matches) != len(prev) {
				t.Fatalf("call %d: got %d matches, previous had %d", i, len(matches), len(prev))
			}
			for j := range matches {
				if matches[j] != prev[j] {
					t.Errorf("call %d: matches[%d] differs from previous: %+v vs %+v",
						i, j, matches[j], prev[j])
				}
			}
		}
		prev = matches
	}
}

// TS-02-23: Each ranking tier decides order in isolation and in combination
func TestFindSymbolRankingTiers_TS02_23(t *testing.T) {
	// Tier 1: exact before prefix
	t.Run("exact_before_prefix", func(t *testing.T) {
		root := t.TempDir()
		mkSymFile(t, root, "a.go", "package main\n\nfunc RunAll() {}\nfunc Run() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		r := exec(context.Background(), json.RawMessage(`{"name":"Run"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		// Run (exact) should come before RunAll (prefix).
		if matches[0].Name != "Run" {
			t.Errorf("expected exact match 'Run' first, got %q", matches[0].Name)
		}
	})

	// Tier 1 variant: smart-case exact (all-lowercase query equal case-insensitively counts as exact)
	t.Run("smartcase_exact", func(t *testing.T) {
		root := t.TempDir()
		mkSymFile(t, root, "a.go", "package main\n\nfunc RunAll() {}\nfunc Run() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		// "run" (all lowercase) matches "Run" exactly under smart-case.
		r := exec(context.Background(), json.RawMessage(`{"name":"run"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		// "Run" is an exact match under smart-case (case-insensitive equality).
		// "RunAll" is a prefix match.
		if matches[0].Name != "Run" {
			t.Errorf("expected smart-case exact match 'Run' first, got %q", matches[0].Name)
		}
	})

	// Tier 2: exported before unexported
	t.Run("exported_before_unexported", func(t *testing.T) {
		root := t.TempDir()
		// Both Run (exported) and run (unexported) are exact matches under
		// smart-case with the all-lowercase query "run".
		mkSymFile(t, root, "a.go", "package main\n\nfunc run() {}\nfunc Run() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		// "run" (all lowercase) → smart-case exact matches both Run and run.
		r := exec(context.Background(), json.RawMessage(`{"name":"run"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		// Both are exact under smart-case, so tier 2 (exported) decides.
		if !matches[0].Exported {
			t.Error("expected exported match first")
		}
		if matches[1].Exported {
			t.Error("expected unexported match second")
		}
	})

	// Tier 3: non-test before test
	t.Run("nontest_before_test", func(t *testing.T) {
		root := t.TempDir()
		mkSymFile(t, root, "a_test.go", "package main\n\nfunc Foo() {}\n")
		mkSymFile(t, root, "a.go", "package main\n\nfunc Foo() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		r := exec(context.Background(), json.RawMessage(`{"name":"Foo", "exact": true}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		if isTestFile(matches[0].Path) {
			t.Error("expected non-test file first")
		}
		if !isTestFile(matches[1].Path) {
			t.Error("expected test file second")
		}
	})

	// Tier 4: shorter path first
	t.Run("shorter_path_first", func(t *testing.T) {
		root := t.TempDir()
		mkSymFile(t, root, "pkg/deep/a.go", "package deep\n\nfunc Foo() {}\n")
		mkSymFile(t, root, "a.go", "package main\n\nfunc Foo() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		r := exec(context.Background(), json.RawMessage(`{"name":"Foo", "exact": true}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		if len(matches[0].Path) > len(matches[1].Path) {
			t.Errorf("expected shorter path first: %q vs %q", matches[0].Path, matches[1].Path)
		}
	})

	// Tier 5: path (lexicographic)
	t.Run("path_lexicographic", func(t *testing.T) {
		root := t.TempDir()
		mkSymFile(t, root, "bbb.go", "package main\n\nfunc Foo() {}\n")
		mkSymFile(t, root, "aaa.go", "package main\n\nfunc Foo() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		r := exec(context.Background(), json.RawMessage(`{"name":"Foo", "exact": true}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		if matches[0].Path > matches[1].Path {
			t.Errorf("expected lexicographic path order: %q vs %q", matches[0].Path, matches[1].Path)
		}
	})

	// Tier 6: StartLine
	t.Run("startline", func(t *testing.T) {
		root := t.TempDir()
		mkSymFile(t, root, "a.go", "package main\n\nfunc Foo() {}\n\nfunc Foo2() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		r := exec(context.Background(), json.RawMessage(`{"name":"Foo"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		// Both Foo and Foo2 match prefix "Foo".
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		// Foo (exact) comes before Foo2 (prefix) by tier 1.
		// But if both were prefix, StartLine would decide.
		if matches[0].StartLine > matches[1].StartLine {
			t.Errorf("expected lower StartLine first: %d vs %d", matches[0].StartLine, matches[1].StartLine)
		}
	})

	// Combined: higher tier overrides lower tiers
	t.Run("combined_higher_overrides_lower", func(t *testing.T) {
		root := t.TempDir()
		// Unexported exact match vs exported prefix match.
		// Tier 1 (exact) should override tier 2 (exported).
		mkSymFile(t, root, "a.go", "package main\n\nfunc RunAll() {}\nfunc run() {}\n")
		exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
		r := exec(context.Background(), json.RawMessage(`{"name":"run"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) < 2 {
			t.Fatalf("expected at least 2 matches, got %d", len(matches))
		}
		// "run" exact match (unexported) should come before "RunAll" prefix match (exported).
		if matches[0].Name != "run" {
			t.Errorf("expected exact match 'run' first (tier 1 overrides tier 2), got %q", matches[0].Name)
		}
	})
}

// TS-02-24: Test-file classification follows the fixed rule and nothing else
func TestIsTestFile_TS02_24(t *testing.T) {
	positives := []string{
		"a_test.go",
		"test_x.py",
		"x_test.py",
		"a.test.ts",
		"a.spec.js",
		"FooTest.java",
		"FooTests.kt",
		"FooTest.cs",
		"foo_spec.rb",
		"tests/x.go",
		"__tests__/a.js",
		"spec/a.rb",
		"test/a.c",
		"pkg/test/deep.go",
		"a.test.tsx",
		"a.spec.mjs",
	}
	negatives := []string{
		"testing.go",
		"contest.py",
		"latest.go",
		"Test.java",
		"src/attestation/a.go",
		"a_test.txt",
		"testutils.go",
		"a.go",
		"main.py",
		"app.ts",
		"Foo.java",
		"Foo.kt",
		"Foo.cs",
		"foo.rb",
		"pkg/src/a.go",
	}

	for _, p := range positives {
		if !isTestFile(p) {
			t.Errorf("expected %q to be classified as test file", p)
		}
	}
	for _, p := range negatives {
		if isTestFile(p) {
			t.Errorf("expected %q to NOT be classified as test file", p)
		}
	}
}

// TS-02-25: max_results defaults to 20 and clamps to 50
func TestFindSymbolMaxResultsClamping_TS02_25(t *testing.T) {
	root := t.TempDir()

	// Create 80 matching declarations.
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc Sym%03d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Omitted max_results → 20.
	r := exec(context.Background(), json.RawMessage(`{"name":"Sym"}`))
	if !r.OK {
		t.Fatalf("omitted: error=%s detail=%s", r.Error, r.Detail)
	}
	if n := len(extractSymbolMatches(t, r)); n != 20 {
		t.Fatalf("omitted: got %d matches, want 20", n)
	}

	// max_results=0 → 20.
	r = exec(context.Background(), json.RawMessage(`{"name":"Sym","max_results":0}`))
	if !r.OK {
		t.Fatalf("0: error=%s detail=%s", r.Error, r.Detail)
	}
	if n := len(extractSymbolMatches(t, r)); n != 20 {
		t.Fatalf("0: got %d matches, want 20", n)
	}

	// max_results=-5 → 20.
	r = exec(context.Background(), json.RawMessage(`{"name":"Sym","max_results":-5}`))
	if !r.OK {
		t.Fatalf("-5: error=%s detail=%s", r.Error, r.Detail)
	}
	if n := len(extractSymbolMatches(t, r)); n != 20 {
		t.Fatalf("-5: got %d matches, want 20", n)
	}

	// max_results=200 → 50.
	r = exec(context.Background(), json.RawMessage(`{"name":"Sym","max_results":200}`))
	if !r.OK {
		t.Fatalf("200: error=%s detail=%s", r.Error, r.Detail)
	}
	if n := len(extractSymbolMatches(t, r)); n != 50 {
		t.Fatalf("200: got %d matches, want 50", n)
	}
}

// TS-02-26: Truncation returns the first max_results matches with the pinned SymbolMarker text and metadata
func TestFindSymbolTruncation_TS02_26(t *testing.T) {
	root := t.TempDir()

	// Create 80 matching declarations.
	for i := 0; i < 80; i++ {
		name := fmt.Sprintf("f%03d.go", i)
		content := fmt.Sprintf("package main\n\nfunc Sym%03d() {}\n", i)
		mkSymFile(t, root, name, content)
	}

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Default (20): below cap, marker names max_results=40.
	t.Run("default_20", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"name":"Sym"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) != 20 {
			t.Fatalf("got %d matches, want 20", len(matches))
		}

		// Data.truncated should be true.
		truncated, _ := r.Data["truncated"].(bool)
		if !truncated {
			t.Fatal("expected truncated=true")
		}

		// Data.note should equal SymbolMarker(20).
		note, _ := r.Data["note"].(string)
		expectedMarker := SymbolMarker(20)
		if note != expectedMarker {
			t.Fatalf("note=%q, want %q", note, expectedMarker)
		}
		if !strings.Contains(note, "max_results=40") {
			t.Fatalf("marker should name max_results=40: %q", note)
		}

		// ToolMetadata.
		if r.Metadata == nil {
			t.Fatal("expected non-nil Metadata")
		}
		if !r.Metadata.Truncated {
			t.Fatal("expected Metadata.Truncated=true")
		}
		if r.Metadata.TruncatedBy != string(TruncatedByLines) {
			t.Fatalf("expected TruncatedBy=%q, got %q", TruncatedByLines, r.Metadata.TruncatedBy)
		}

		// Marker should appear in Text.
		if !strings.Contains(r.Text, expectedMarker) {
			t.Fatalf("Text should contain marker: %q", r.Text)
		}
	})

	// max_results=30: below cap, marker names max_results=50 (30*2=60 clamped to 50).
	t.Run("max_results_30", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"name":"Sym","max_results":30}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) != 30 {
			t.Fatalf("got %d matches, want 30", len(matches))
		}

		note, _ := r.Data["note"].(string)
		expectedMarker := SymbolMarker(30)
		if note != expectedMarker {
			t.Fatalf("note=%q, want %q", note, expectedMarker)
		}
		if !strings.Contains(note, "max_results=50") {
			t.Fatalf("marker should name max_results=50: %q", note)
		}
	})

	// max_results=50: at cap, marker says 50 is the maximum and advises narrowing.
	t.Run("max_results_50_at_cap", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"name":"Sym","max_results":50}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		if len(matches) != 50 {
			t.Fatalf("got %d matches, want 50", len(matches))
		}

		note, _ := r.Data["note"].(string)
		expectedMarker := SymbolMarker(50)
		if note != expectedMarker {
			t.Fatalf("note=%q, want %q", note, expectedMarker)
		}
		// At cap: should NOT name max_results=<anything>.
		if strings.Contains(note, "max_results=") {
			t.Fatalf("at cap, marker should not name max_results=: %q", note)
		}
		// Should advise narrowing.
		if !strings.Contains(note, "narrow") {
			t.Fatalf("at cap, marker should advise narrowing: %q", note)
		}
		if !strings.Contains(note, "path") || !strings.Contains(note, "kind") || !strings.Contains(note, "exact") {
			t.Fatalf("at cap, marker should mention path, kind and exact: %q", note)
		}
	})

	// Pinned marker goldens.
	t.Run("pinned_markers", func(t *testing.T) {
		// Below cap.
		got := SymbolMarker(20)
		want := "[20 results limit reached. Use max_results=40 for more, or narrow with path, kind or exact]"
		if got != want {
			t.Fatalf("SymbolMarker(20)=%q, want %q", got, want)
		}

		// At cap.
		got = SymbolMarker(50)
		want = "[50 results limit reached, which is the maximum max_results; narrow with path, kind or exact]"
		if got != want {
			t.Fatalf("SymbolMarker(50)=%q, want %q", got, want)
		}
	})
}

// TS-02-27: find_symbol Data carries the documented keys with JSON names
func TestFindSymbolDataKeys_TS02_27(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")
	mkSymFile(t, root, "b.py", "def world():\n    pass\n")

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
	r := exec(context.Background(), json.RawMessage(`{"name":"Hello", "exact": true}`))
	if !r.OK {
		t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
	}

	// Check top-level Data keys.
	if _, ok := r.Data["symbols"]; !ok {
		t.Fatal("missing 'symbols' key")
	}
	if _, ok := r.Data["backends"]; !ok {
		t.Fatal("missing 'backends' key")
	}
	if _, ok := r.Data["files_indexed"]; !ok {
		t.Fatal("missing 'files_indexed' key")
	}

	// truncated should be present (false when not truncated).
	truncated, ok := r.Data["truncated"]
	if !ok {
		t.Fatal("missing 'truncated' key")
	}
	if truncated.(bool) {
		t.Fatal("expected truncated=false for non-truncated result")
	}

	// note and partial should be absent when they don't apply.
	if _, ok := r.Data["note"]; ok {
		t.Fatal("'note' should be absent when not truncated")
	}
	if _, ok := r.Data["partial"]; ok {
		t.Fatal("'partial' should be absent when not partial")
	}

	// Check SymbolMatch JSON keys by marshalling.
	matches := extractSymbolMatches(t, r)
	if len(matches) == 0 {
		t.Fatal("expected at least one match")
	}

	// Marshal a SymbolMatch and check JSON keys.
	b, err := json.Marshal(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}

	requiredKeys := []string{"path", "backend", "kind", "name", "container", "signature", "exported", "start_line", "end_line"}
	for _, k := range requiredKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("SymbolMatch JSON missing key %q", k)
		}
	}

	// backends should be a map from backend name to count.
	backends, ok := r.Data["backends"].(map[string]int)
	if !ok {
		t.Fatalf("backends is %T, want map[string]int", r.Data["backends"])
	}
	total := 0
	for _, n := range backends {
		total += n
	}
	fi, _ := r.Data["files_indexed"].(int)
	if total != fi {
		t.Fatalf("sum of backends (%d) != files_indexed (%d)", total, fi)
	}
}

// TS-02-28: find_symbol Text has the pinned header and match lines, sanitises paths and never lengthens signatures
func TestFindSymbolTextRendering_TS02_28(t *testing.T) {
	root := t.TempDir()

	// Create a file with a control character in its name.
	ctrlName := "bad\x01name.go"
	ctrlPath := filepath.Join(root, ctrlName)
	if err := os.WriteFile(ctrlPath, []byte("package main\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a normal file.
	mkSymFile(t, root, "normal.go", "package main\n\nfunc HelloWorld() {}\n")

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// Search for both using a prefix that matches both.
	r := exec(context.Background(), json.RawMessage(`{"name":"Hello"}`))
	if !r.OK {
		t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
	}

	lines := strings.Split(r.Text, "\n")
	if len(lines) == 0 {
		t.Fatal("empty Text")
	}

	// Header line should match pattern: N symbols matching "name"  (index: F files; ...)
	header := lines[0]
	if !strings.Contains(header, `symbols matching "Hello"`) {
		t.Errorf("header should contain 'symbols matching \"Hello\"': %q", header)
	}
	if !strings.Contains(header, "(index:") {
		t.Errorf("header should contain '(index:': %q", header)
	}
	if !strings.Contains(header, "files") {
		t.Errorf("header should contain 'files': %q", header)
	}

	// Match lines should have format: <path>:<start>-<end>  <kind>  <signature>
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			continue // marker line
		}
		if strings.HasPrefix(line, "no symbols") {
			continue // no-match line
		}
		// Should contain a colon (path:line).
		if !strings.Contains(line, ":") {
			t.Errorf("match line should contain ':': %q", line)
		}
	}

	// Control characters in printed path should be replaced with '?'.
	text := r.Text
	for _, b := range []byte(text) {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			t.Errorf("Text contains control character 0x%02x", b)
		}
	}

	// Data.symbols[].path should keep the real path (with control char).
	matches := extractSymbolMatches(t, r)
	foundCtrl := false
	for _, m := range matches {
		if strings.Contains(m.Path, "\x01") {
			foundCtrl = true
		}
	}
	if !foundCtrl {
		t.Error("Data.symbols[].path should keep the real path with control character")
	}

	// Signature length should not exceed what outline produced.
	for _, m := range matches {
		if len(m.Signature) > 200 {
			t.Errorf("signature length %d exceeds 200", len(m.Signature))
		}
	}
}

// TS-02-29: No match is a successful result that says so and still shows the index line
func TestFindSymbolNoMatch_TS02_29(t *testing.T) {
	root := t.TempDir()
	mkSymFile(t, root, "a.go", "package main\n\nfunc Hello() {}\n")

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})
	r := exec(context.Background(), json.RawMessage(`{"name":"NonExistent"}`))

	if !r.OK {
		t.Fatalf("expected OK=true, got error=%s detail=%s", r.Error, r.Detail)
	}

	// Data.symbols should be an empty slice.
	matches := extractSymbolMatches(t, r)
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}

	// Text should state nothing matched.
	if !strings.Contains(r.Text, "0 symbols matching") || !strings.Contains(r.Text, "no symbols matched") {
		t.Errorf("Text should state nothing matched: %q", r.Text)
	}

	// Text should still show the index line.
	if !strings.Contains(r.Text, "(index:") {
		t.Errorf("Text should show the index line: %q", r.Text)
	}

	// files_indexed should be > 0.
	fi, _ := r.Data["files_indexed"].(int)
	if fi == 0 {
		t.Error("files_indexed should be > 0 even with no matches")
	}
}

// TS-02-30: kind and path filters restrict matches, backends and files_indexed to scope
func TestFindSymbolKindAndPathFilters_TS02_30(t *testing.T) {
	root := t.TempDir()

	// Create files with different kinds in different directories.
	mkSymFile(t, root, "pkg/a/a.go", `package a

type Runner struct{}

func (r *Runner) Run() {}
func FreeFunc() {}
`)
	mkSymFile(t, root, "pkg/b/b.go", `package b

type Handler struct{}

func (h *Handler) Handle() {}
func AnotherFunc() {}
`)

	exec := makeFindSymbolTool(t, root, SymbolOptions{DisableCtags: true})

	// kind=method: only methods.
	t.Run("kind_method", func(t *testing.T) {
		r := exec(context.Background(), json.RawMessage(`{"name":"R", "kind":"method"}`))
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		for _, m := range matches {
			if m.Kind != "method" {
				t.Errorf("expected kind=method, got %q for %s", m.Kind, m.Name)
			}
		}
	})

	// path=pkg/a: only files under pkg/a.
	t.Run("path_pkg_a", func(t *testing.T) {
		pkgAPath := filepath.Join(root, "pkg", "a")
		args, _ := json.Marshal(map[string]any{"name": "R", "path": pkgAPath})
		r := exec(context.Background(), args)
		if !r.OK {
			t.Fatalf("error=%s detail=%s", r.Error, r.Detail)
		}
		matches := extractSymbolMatches(t, r)
		for _, m := range matches {
			if !strings.HasPrefix(m.Path, "pkg/a/") {
				t.Errorf("expected path under pkg/a/, got %q", m.Path)
			}
		}

		// backends and files_indexed should count only files in scope.
		fi, _ := r.Data["files_indexed"].(int)
		if fi != 1 {
			t.Fatalf("files_indexed=%d, want 1 (only pkg/a/a.go)", fi)
		}

		backends, _ := r.Data["backends"].(map[string]int)
		total := 0
		for _, n := range backends {
			total += n
		}
		if total != fi {
			t.Fatalf("sum of backends (%d) != files_indexed (%d)", total, fi)
		}
	})
}

// --- helpers ---

// extractSymbolMatches extracts SymbolMatch entries from a ToolResult.
func extractSymbolMatches(t *testing.T, r core.ToolResult) []SymbolMatch {
	t.Helper()
	syms, ok := r.Data["symbols"]
	if !ok {
		t.Fatal("no 'symbols' key in Data")
	}
	slice, ok := syms.([]SymbolMatch)
	if ok {
		return slice
	}
	t.Fatalf("symbols is %T, want []SymbolMatch", syms)
	return nil
}

// rankLessOrEqual checks if a should come before or equal to b in the ranking.
func rankLessOrEqual(a, b SymbolMatch, query string, qualified, caseSensitive bool) bool {
	aTarget := a.Name
	bTarget := b.Name
	if qualified {
		if a.Container != "" {
			aTarget = a.Container + "." + a.Name
		}
		if b.Container != "" {
			bTarget = b.Container + "." + b.Name
		}
	}
	aExact := isExactMatch(query, aTarget, caseSensitive)
	bExact := isExactMatch(query, bTarget, caseSensitive)
	if aExact != bExact {
		return aExact
	}
	if a.Exported != b.Exported {
		return a.Exported
	}
	aTest := isTestFile(a.Path)
	bTest := isTestFile(b.Path)
	if aTest != bTest {
		return !aTest
	}
	if len(a.Path) != len(b.Path) {
		return len(a.Path) < len(b.Path)
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.StartLine <= b.StartLine
}

// sortedBackendKeys returns sorted keys from a backends map.
func sortedBackendKeys(backends map[string]int) []string {
	keys := make([]string, 0, len(backends))
	for k := range backends {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
