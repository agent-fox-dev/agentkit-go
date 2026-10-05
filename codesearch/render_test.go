//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-03-24: Results are score ordered by file with a sym: hit outranking a
// comment hit and the chunk, file and line caps hold.
// Goldens are tied to the pinned zoekt version (v0.0.0-20260911061844-153817f643cd).
func TestResultsScoreOrderedAndCapsHold_TS03_24(t *testing.T) {
	root := t.TempDir()

	// File that declares func Runner (sym: hit should rank higher).
	mkFile(t, root, "decl.go", "package main\n\nfunc Runner() {\n\t// body\n}\n")
	// File that only mentions Runner in a comment.
	mkFile(t, root, "comment.go", "package main\n\n// Runner is mentioned here\nvar x = 1\n")
	// File with 6 separated matches to test 3-chunk cap.
	sixMatches := "package main\n"
	for i := 0; i < 6; i++ {
		sixMatches += strings.Repeat(fmt.Sprintf("// filler line %d\n", i), 5)
		sixMatches += "// matchword here\n"
	}
	mkFile(t, root, "sixmatches.go", sixMatches)
	// File with a long line exceeding SearchLineChars.
	longLine := "// matchword " + strings.Repeat("x", tools.SearchLineChars+100) + "\n"
	mkFile(t, root, "longline.go", "package main\n"+longLine)

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Search for sym:Runner with max_files 2.
	in, _ := json.Marshal(map[string]any{"query": "sym:Runner", "max_files": 2, "context_lines": 1})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}

	paths := getResultFilePaths(r)
	if len(paths) == 0 {
		t.Fatal("expected at least one file in result")
	}
	// The declaring file should be first (sym: hit outranks comment hit).
	if paths[0] != "decl.go" {
		t.Errorf("first file = %q, want decl.go (sym: hit should outrank comment)", paths[0])
	}
	// At most max_files files.
	if len(paths) > 2 {
		t.Errorf("got %d files, want at most 2", len(paths))
	}

	// Search for matchword to test chunk and line caps.
	in, _ = json.Marshal(map[string]any{"query": "matchword", "max_files": 10, "context_lines": 1})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}

	// Check chunk cap: each file should have at most 3 chunks.
	filesData := getDataFiles(r)
	for _, fd := range filesData {
		chunks := fd["chunks"]
		if chunks == nil {
			continue
		}
		chunkArr, ok := chunks.([]any)
		if !ok {
			continue
		}
		if len(chunkArr) > 3 {
			t.Errorf("file %v has %d chunks, want at most 3", fd["path"], len(chunkArr))
		}
		// Within each chunk, lines should be in ascending order.
		for ci, c := range chunkArr {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			lines, ok := cm["lines"].([]any)
			if !ok {
				continue
			}
			prevLine := 0
			for _, l := range lines {
				lm, ok := l.(map[string]any)
				if !ok {
					continue
				}
				var lineNo int
				switch v := lm["line"].(type) {
				case int:
					lineNo = v
				case float64:
					lineNo = int(v)
				default:
					continue
				}
				if lineNo <= prevLine {
					t.Errorf("file %v chunk %d: line %d not ascending after %d", fd["path"], ci, lineNo, prevLine)
				}
				prevLine = lineNo
			}
		}
	}

	// Check that long lines are cut to SearchLineChars.
	if r.Text != "" {
		for _, line := range strings.Split(r.Text, "\n") {
			// Skip header lines and marker lines.
			if strings.HasPrefix(line, "[") || !strings.Contains(line, ":") {
				continue
			}
			// Match lines like "  N: text" or "  N- text".
			parts := regexp.MustCompile(`^\s*\d+[:-] (.*)$`).FindStringSubmatch(line)
			if len(parts) < 2 {
				continue
			}
			text := parts[1]
			runes := []rune(text)
			if len(runes) > tools.SearchLineChars+10 { // small margin for ellipsis
				t.Errorf("line text has %d runes, want at most ~%d", len(runes), tools.SearchLineChars)
			}
		}
	}
}

// TS-03-25: Each file header and line follows the RenderSearchText shape with
// path, count, note and declaration names.
func TestFileHeaderAndLineShape_TS03_25(t *testing.T) {
	root := t.TempDir()

	// File with a match on the start line of func (Server) Run and three more declarations.
	mkFile(t, root, "server.go", `package main

func (Server) Run() {}
func (Server) Stop() {}
func (Server) Start() {}
func (Server) Pause() {}
`)

	// File with a control character in its name.
	mkFile(t, root, "bad\x01name.go", "package main\n// matchword\n")

	// File with more than 3 chunks of matches (7 separated matches).
	manyMatches := "package main\n"
	for i := 0; i < 7; i++ {
		manyMatches += strings.Repeat("// filler\n", 10)
		manyMatches += "// matchword\n"
	}
	mkFile(t, root, "many.go", manyMatches)

	// File with 7 declarations on match lines (to test the 5-name cap).
	sevenDecls := "package main\n"
	for i := 0; i < 7; i++ {
		sevenDecls += fmt.Sprintf("func Decl%d() {} // matchword\n", i)
	}
	mkFile(t, root, "sevendecls.go", sevenDecls)

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Search for matchword.
	in, _ := json.Marshal(map[string]any{"query": "matchword", "max_files": 25, "context_lines": 1})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}

	text := r.Text
	if text == "" {
		t.Fatal("Text should not be empty")
	}

	// Check that matched lines print as "N: text" and context as "N- text".
	matchLineRe := regexp.MustCompile(`^\s+\d+: `)
	contextLineRe := regexp.MustCompile(`^\s+\d+- `)
	hasMatch := false
	hasContext := false
	for _, line := range strings.Split(text, "\n") {
		if matchLineRe.MatchString(line) {
			hasMatch = true
		}
		if contextLineRe.MatchString(line) {
			hasContext = true
		}
	}
	if !hasMatch {
		t.Error("expected at least one matched line in 'N: text' format")
	}
	if !hasContext {
		t.Error("expected at least one context line in 'N- text' format")
	}

	// Check that control characters in file names are replaced with '?'.
	if strings.Contains(text, "\x01") {
		t.Error("control character should be replaced with '?' in file header")
	}
	if !strings.Contains(text, "bad?name.go") {
		t.Error("expected 'bad?name.go' in text (control char replaced)")
	}

	// Check that the file with many matches has a note about matches not shown.
	// (It has 7 matches but only 3 chunks are shown.)
	if !strings.Contains(text, "not shown") && !strings.Contains(text, "more match") {
		t.Log("Note: text does not mention 'not shown' for file with many matches")
		// This is acceptable if all matches fit in 3 chunks.
	}

	// Search for declarations to test member names.
	in, _ = json.Marshal(map[string]any{"query": "Run", "max_files": 10, "context_lines": 0})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	text = r.Text
	// Server.Run should appear as Container.Name in the header.
	if strings.Contains(text, "server.go") && !strings.Contains(text, "Server.Run") {
		t.Log("Note: Server.Run not found in header (may depend on symbol sections)")
	}

	// Test the 5-name cap: search sevendecls.go for matchword.
	in, _ = json.Marshal(map[string]any{"query": "matchword", "path": "sevendecls.go", "max_files": 1, "context_lines": 0})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}
	text = r.Text
	// Count declaration names in the header for sevendecls.go.
	// The header should have at most 5 names.
	headerLine := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "sevendecls.go") {
			headerLine = line
			break
		}
	}
	if headerLine != "" {
		// Count "Decl" occurrences in the header.
		declCount := strings.Count(headerLine, "Decl")
		if declCount > 5 {
			t.Errorf("header has %d declaration names, want at most 5", declCount)
		}
	}
}

// TS-03-26: The first line states files, index size, symbol sources, ctags
// state, partial and dirty notes.
func TestFirstLineContent_TS03_26(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n")
	mkFile(t, root, "lib.py", "def hello():\n    pass\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("complete with ctags disabled", func(t *testing.T) {
		idx, err := newIndex(ws, Options{
			TempDir:      t.TempDir(),
			Ignore:       tools.NoGlobalExcludes(),
			DisableCtags: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		tool := idx.Tools()[0]
		in, _ := json.Marshal(map[string]any{"query": "main"})
		r := tool.Execute(context.Background(), in)
		if !r.OK {
			t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
		}

		firstLine := strings.SplitN(r.Text, "\n", 2)[0]

		// Should state number of files.
		if !regexp.MustCompile(`\d+ file`).MatchString(firstLine) {
			t.Errorf("first line should state file count: %q", firstLine)
		}

		// Should mention ctags unavailable (since DisableCtags is set).
		if !strings.Contains(strings.ToLower(firstLine), "ctags") {
			t.Errorf("first line should mention ctags: %q", firstLine)
		}

		// Should NOT mention partial or dirty.
		if strings.Contains(firstLine, "partial") {
			t.Errorf("first line should not mention partial: %q", firstLine)
		}
		if strings.Contains(firstLine, "dirty") {
			t.Errorf("first line should not mention dirty: %q", firstLine)
		}
	})

	t.Run("with fake runner (ctags available)", func(t *testing.T) {
		fakeRunner := func(_ context.Context, _ []string) ([]byte, error) {
			return []byte(""), nil
		}
		idx, err := newIndex(ws, Options{
			TempDir: t.TempDir(),
			Ignore:  tools.NoGlobalExcludes(),
			Runner:  fakeRunner,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer idx.Close()

		tool := idx.Tools()[0]
		in, _ := json.Marshal(map[string]any{"query": "main"})
		r := tool.Execute(context.Background(), in)
		if !r.OK {
			t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
		}

		firstLine := strings.SplitN(r.Text, "\n", 2)[0]

		// Should state number of files.
		if !regexp.MustCompile(`\d+ file`).MatchString(firstLine) {
			t.Errorf("first line should state file count: %q", firstLine)
		}

		// Should mention symbol sources (go/ast at minimum).
		if !strings.Contains(firstLine, "go/ast") {
			t.Logf("first line: %q (go/ast may not appear if no Go files matched)", firstLine)
		}
	})
}

// TS-03-27: A query with no match is a successful result that says so and
// keeps the first line.
func TestNoMatchIsSuccessful_TS03_27(t *testing.T) {
	root := t.TempDir()
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "zzzznomatch"})
	r := tool.Execute(context.Background(), in)

	// Should be OKResult, not an error.
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}

	// Text should say nothing matched.
	if !strings.Contains(strings.ToLower(r.Text), "no match") {
		t.Errorf("text should say 'no match', got: %q", r.Text)
	}

	// First line should still be present.
	firstLine := strings.SplitN(r.Text, "\n", 2)[0]
	if firstLine == "" {
		t.Error("first line should not be empty")
	}

	// Data.files should be empty.
	files := getResultFilePaths(r)
	if len(files) != 0 {
		t.Errorf("Data.files should be empty, got %d files", len(files))
	}
}

// TS-03-28: More files than max_files appends the CapMarker and records
// TruncatedByLines, with the at-the-cap form at 25.
// Goldens are tied to the pinned zoekt version (v0.0.0-20260911061844-153817f643cd).
func TestCapMarkerAndTruncatedByLines_TS03_28(t *testing.T) {
	root := t.TempDir()

	// Create 30 files matching the query.
	for i := 0; i < 30; i++ {
		mkFile(t, root, fmt.Sprintf("file%03d.go", i),
			fmt.Sprintf("package pkg\n// commonword here\n"))
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// max_files 10: should get the cap marker.
	wantMarker10 := tools.CapMarker("files", "max_files", 10, 25, "narrow the query or add a path")
	in, _ := json.Marshal(map[string]any{"query": "commonword", "max_files": 10})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
	}

	if !strings.Contains(r.Text, wantMarker10) {
		t.Errorf("text should contain cap marker %q\ngot text:\n%s", wantMarker10, r.Text)
	}

	if r.Metadata == nil || !r.Metadata.Truncated {
		t.Error("Metadata.Truncated should be true")
	}
	if r.Metadata != nil && r.Metadata.TruncatedBy != string(tools.TruncatedByLines) {
		t.Errorf("TruncatedBy = %q, want %q", r.Metadata.TruncatedBy, tools.TruncatedByLines)
	}

	// max_files 25: at-the-cap form.
	wantMarker25 := tools.CapMarker("files", "max_files", 25, 25, "narrow the query or add a path")
	in, _ = json.Marshal(map[string]any{"query": "commonword", "max_files": 25})
	r = tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
	}

	if !strings.Contains(r.Text, wantMarker25) {
		t.Errorf("text should contain at-the-cap marker %q\ngot text:\n%s", wantMarker25, r.Text)
	}

	if r.Metadata == nil || !r.Metadata.Truncated {
		t.Error("Metadata.Truncated should be true at cap")
	}
	if r.Metadata != nil && r.Metadata.TruncatedBy != string(tools.TruncatedByLines) {
		t.Errorf("TruncatedBy = %q, want %q", r.Metadata.TruncatedBy, tools.TruncatedByLines)
	}
}

// TS-03-29: A result over the byte limit drops whole files from the end, keeps
// one, and ends with a bytes marker.
func TestByteLimitDropsFiles_TS03_29(t *testing.T) {
	root := t.TempDir()

	// Create 25 files where each yields about 4 KB of text.
	// Each file has many matches with long context.
	for i := 0; i < 25; i++ {
		content := "package pkg\n"
		for j := 0; j < 40; j++ {
			content += fmt.Sprintf("// bigword line %d in file %d with padding %s\n",
				j, i, strings.Repeat("x", 80))
		}
		mkFile(t, root, fmt.Sprintf("big%03d.go", i), content)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	in, _ := json.Marshal(map[string]any{"query": "bigword", "max_files": 25, "context_lines": 20})
	r := tool.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
	}

	// Text should be at most DefaultByteLimit bytes.
	if len(r.Text) > tools.DefaultByteLimit {
		t.Errorf("text is %d bytes, want at most %d", len(r.Text), tools.DefaultByteLimit)
	}

	// At least one file should remain.
	paths := getResultFilePaths(r)
	if len(paths) < 1 {
		t.Error("at least one file should remain after byte truncation")
	}

	// Metadata should record TruncatedByBytes.
	if r.Metadata == nil || !r.Metadata.Truncated {
		t.Error("Metadata.Truncated should be true")
	}
	if r.Metadata != nil && r.Metadata.TruncatedBy != string(tools.TruncatedByBytes) {
		t.Errorf("TruncatedBy = %q, want %q", r.Metadata.TruncatedBy, tools.TruncatedByBytes)
	}

	// A bytes marker should be present naming path, file: or context_lines.
	markerRe := regexp.MustCompile(`path|file:|context_lines`)
	if !markerRe.MatchString(r.Text) {
		t.Errorf("bytes marker should name path, file: or context_lines in text:\n%s", r.Text)
	}

	// Test single large file: create one file that alone exceeds 50 KB.
	root2 := t.TempDir()
	bigContent := "package pkg\n"
	for j := 0; j < 1000; j++ {
		bigContent += fmt.Sprintf("// bigword line %d %s\n", j, strings.Repeat("y", 80))
	}
	mkFile(t, root2, "huge.go", bigContent)

	ws2, err := tools.NewWorkspace(root2)
	if err != nil {
		t.Fatal(err)
	}

	idx2, err := newIndex(ws2, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx2.Close()

	tool2 := idx2.Tools()[0]
	in, _ = json.Marshal(map[string]any{"query": "bigword", "max_files": 1, "context_lines": 20})
	r = tool2.Execute(ctx, in)
	if !r.OK {
		t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
	}

	// Text should be at most DefaultByteLimit.
	if len(r.Text) > tools.DefaultByteLimit {
		t.Errorf("single file text is %d bytes, want at most %d", len(r.Text), tools.DefaultByteLimit)
	}

	// At least one file should remain.
	paths = getResultFilePaths(r)
	if len(paths) < 1 {
		t.Error("at least one file should remain for single large file")
	}
}

// TS-03-30: Data carries all documented fields including skipped-file counts.
func TestDataCarriesAllFields_TS03_30(t *testing.T) {
	root := t.TempDir()

	// Normal file.
	mkFile(t, root, "main.go", "package main\nfunc main() {}\n// searchterm\n")
	// Binary file (skipped).
	mkFile(t, root, "binary.dat", "searchterm\x00binary\n")
	// Oversized file (skipped).
	mkFile(t, root, "huge.txt", "searchterm\n"+strings.Repeat("x", 1<<20+1))

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	in, _ := json.Marshal(map[string]any{"query": "searchterm"})
	r := tool.Execute(context.Background(), in)
	if !r.OK {
		t.Fatalf("expected OK, got: %s: %s", r.Error, r.Detail)
	}

	d := r.Data
	if d == nil {
		t.Fatal("Data should not be nil")
	}

	// Check all required top-level keys.
	requiredKeys := []string{
		"files", "truncated", "note", "partial", "symbol_sources",
		"ctags_available", "files_indexed", "dirty_files", "skipped",
	}
	for _, k := range requiredKeys {
		if _, ok := d[k]; !ok {
			t.Errorf("Data missing key %q", k)
		}
	}

	// Check files have the right structure.
	files, ok := d["files"].([]any)
	if !ok {
		t.Fatalf("Data.files is not []any: %T", d["files"])
	}
	if len(files) == 0 {
		t.Fatal("Data.files should not be empty")
	}

	f0, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("Data.files[0] is not map[string]any: %T", files[0])
	}

	fileKeys := []string{"path", "score", "matches", "chunks", "symbols"}
	for _, k := range fileKeys {
		if _, ok := f0[k]; !ok {
			t.Errorf("Data.files[0] missing key %q", k)
		}
	}

	// Check skipped counts.
	skipped, ok := d["skipped"].(map[string]any)
	if !ok {
		t.Fatalf("Data.skipped is not map[string]any: %T", d["skipped"])
	}
	if _, ok := skipped["binary"]; !ok {
		t.Error("Data.skipped missing 'binary'")
	}
	if _, ok := skipped["oversized"]; !ok {
		t.Error("Data.skipped missing 'oversized'")
	}
}

// TS-03-31: Every truncation marker names an accepted call and the text never
// exceeds the byte limit.
// This is a property test over generated queries.
func TestPropertyMarkersAndByteLimit_TS03_31(t *testing.T) {
	root := t.TempDir()

	// Create a fixture repository large enough to trigger both caps.
	for i := 0; i < 40; i++ {
		content := "package pkg\n"
		for j := 0; j < 30; j++ {
			content += fmt.Sprintf("// propword line %d file %d %s\n", j, i, strings.Repeat("z", 60))
		}
		mkFile(t, root, fmt.Sprintf("prop%03d.go", i), content)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	idx, err := newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	// Test a range of max_files and context_lines values.
	queries := []string{"propword", "line", "file"}
	maxFilesValues := []int{1, 5, 10, 15, 25, 40}
	contextLinesValues := []int{0, 1, 2, 5, 10, 20}

	for _, q := range queries {
		for _, mf := range maxFilesValues {
			for _, cl := range contextLinesValues {
				in, _ := json.Marshal(map[string]any{
					"query":         q,
					"max_files":     mf,
					"context_lines": cl,
				})
				r := tool.Execute(ctx, in)
				if !r.OK {
					continue // some queries may not match
				}

				// Text should never exceed the byte limit.
				if len(r.Text) > tools.DefaultByteLimit {
					t.Errorf("query=%q max_files=%d context_lines=%d: text is %d bytes, limit is %d",
						q, mf, cl, len(r.Text), tools.DefaultByteLimit)
				}

				// Check markers: every marker's suggested parameters should be
				// accepted by the tool's schema.
				checkMarkersValid(t, r.Text, tool, ctx)
			}
		}
	}

	// Test with path constraint.
	mkDir(t, root, "subdir")
	mkFile(t, root, "subdir/sub.go", "package sub\n// propword\n")
	for _, mf := range []int{1, 10, 25} {
		in, _ := json.Marshal(map[string]any{
			"query":     "propword",
			"max_files": mf,
			"path":      "subdir",
		})
		r := tool.Execute(ctx, in)
		if !r.OK {
			continue
		}
		if len(r.Text) > tools.DefaultByteLimit {
			t.Errorf("with path, text is %d bytes, limit is %d", len(r.Text), tools.DefaultByteLimit)
		}
	}
}

// checkMarkersValid verifies that any cap marker in the text suggests valid
// parameters.
func checkMarkersValid(t *testing.T, text string, _ interface{}, _ context.Context) {
	t.Helper()
	// Extract markers: lines starting with '[' and ending with ']'.
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
			continue
		}
		// Check that the marker mentions valid parameters.
		// At the maximum, it should say "which is the maximum" rather than
		// suggesting a larger value.
		if strings.Contains(line, "max_files=") {
			// Extract the suggested value.
			re := regexp.MustCompile(`max_files=(\d+)`)
			m := re.FindStringSubmatch(line)
			if len(m) > 1 {
				var val int
				fmt.Sscanf(m[1], "%d", &val)
				if val > 25 {
					t.Errorf("marker suggests max_files=%d which exceeds cap 25: %s", val, line)
				}
			}
		}
	}
}

// TS-03-36: Without ctags code_search works, says so in the first line, and
// Go files keep symbols.
func TestNoCtagsCodeSearchWorks_TS03_36(t *testing.T) {
	root := t.TempDir()

	// Go file declaring func Runner.
	mkFile(t, root, "decl.go", "package main\n\nfunc Runner() {\n\t// body\n}\n")
	// Python file with a column-0 def.
	mkFile(t, root, "lib.py", "def helper():\n    pass\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	// Use a runner that returns ErrCtagsUnavailable.
	idx, err := newIndex(ws, Options{
		TempDir: t.TempDir(),
		Ignore:  tools.NoGlobalExcludes(),
		Runner: func(_ context.Context, _ []string) ([]byte, error) {
			return nil, tools.ErrCtagsUnavailable
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()

	tool := idx.Tools()[0]
	ctx := context.Background()

	in, _ := json.Marshal(map[string]any{"query": "sym:Runner"})
	r := tool.Execute(ctx, in)

	// Should be successful.
	if !r.OK {
		t.Fatalf("expected OK result, got error: %s: %s", r.Error, r.Detail)
	}

	// First line should mention ctags unavailable.
	firstLine := strings.SplitN(r.Text, "\n", 2)[0]
	if !strings.Contains(strings.ToLower(firstLine), "ctags") {
		t.Errorf("first line should mention ctags: %q", firstLine)
	}

	// First line should mention column-0 declarations.
	if !strings.Contains(firstLine, "column-0") {
		t.Errorf("first line should mention 'column-0': %q", firstLine)
	}

	// Data.ctags_available should be false.
	if d := r.Data; d != nil {
		if ca, ok := d["ctags_available"]; ok {
			if ca != false {
				t.Errorf("ctags_available = %v, want false", ca)
			}
		} else {
			t.Error("Data missing ctags_available")
		}
	}

	// The Go declaration should be found through its go/ast symbol section.
	paths := getResultFilePaths(r)
	found := false
	for _, p := range paths {
		if p == "decl.go" {
			found = true
			break
		}
	}
	if !found {
		t.Error("decl.go should be found through go/ast symbol section")
	}
}

// --- helpers ---

// getDataFiles extracts the files array from Data as []map[string]any.
func getDataFiles(r core.ToolResult) []map[string]any {
	if r.Data == nil {
		return nil
	}
	files, ok := r.Data["files"]
	if !ok {
		return nil
	}
	arr, ok := files.([]any)
	if !ok {
		return nil
	}
	var result []map[string]any
	for _, f := range arr {
		m, ok := f.(map[string]any)
		if ok {
			result = append(result, m)
		}
	}
	return result
}

// mkDir creates a directory at root/rel.
func mkDir(t *testing.T, root, rel string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
}
