//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// devFixture builds an index over the files in the map (path to content),
// with ctags disabled unless opts.Runner is set.
func devFixture(t *testing.T, files map[string]string, opts Options) (*Index, func(q string, extra map[string]any) core.ToolResult) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		mkFile(t, root, rel, content)
	}
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	opts.TempDir = t.TempDir()
	opts.Ignore = tools.NoGlobalExcludes()
	if opts.Runner == nil {
		opts.DisableCtags = true
	}
	idx, err := newIndex(ws, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	tool := idx.Tools()[0]
	return idx, func(q string, extra map[string]any) core.ToolResult {
		args := map[string]any{"query": q}
		for k, v := range extra {
			args[k] = v
		}
		in, _ := json.Marshal(args)
		return tool.Execute(context.Background(), in)
	}
}

func firstLineOf(r core.ToolResult) string { return strings.SplitN(r.Text, "\n", 2)[0] }

// 03-REQ-4.3 (1): the first line states the number of files in the result,
// not only the number indexed.
func TestFirstLineStatesTheMatchedFileCount(t *testing.T) {
	_, search := devFixture(t, map[string]string{
		"a.go": "package p\n// needle\n",
		"b.go": "package p\n// needle\n",
		"c.go": "package p\n// other\n",
	}, Options{})

	for _, tc := range []struct {
		name  string
		q     string
		extra map[string]any
		want  string
	}{
		{"two match", "needle", nil, "2 files matched"},
		{"none match", "absent", nil, "0 files matched"},
		{"capped by max_files", "needle", map[string]any{"max_files": 1}, "1 files matched"},
	} {
		r := search(tc.q, tc.extra)
		if !r.OK {
			t.Fatalf("%s: %s: %s", tc.name, r.Error, r.Detail)
		}
		line := firstLineOf(r)
		if !strings.Contains(line, tc.want) {
			t.Errorf("%s: first line %q does not state %q", tc.name, line, tc.want)
		}
		if !strings.Contains(line, "3 files indexed") {
			t.Errorf("%s: first line %q lost the indexed count", tc.name, line)
		}
	}
}

// 03-REQ-5.4 (2): ctags_available is what the runner says, not whether some
// indexed file happened to use the ctags backend.
func TestCtagsAvailableIsWhatTheRunnerReports(t *testing.T) {
	goOnly := map[string]string{"a.go": "package p\n// needle\nfunc A() {}\n"}

	t.Run("a Go-only tree with ctags installed", func(t *testing.T) {
		var calls atomic.Int32
		_, search := devFixture(t, goOnly, Options{
			Runner: func(_ context.Context, _ []string) ([]byte, error) {
				calls.Add(1)
				return nil, nil // ctags runs fine
			},
		})
		r := search("needle", nil)
		if !r.OK {
			t.Fatalf("%s: %s", r.Error, r.Detail)
		}
		if got := r.Data["ctags_available"]; got != true {
			t.Errorf("ctags_available = %v, want true: the runner works, no file needed it", got)
		}
		if line := firstLineOf(r); strings.Contains(line, "ctags unavailable") {
			t.Errorf("first line %q claims ctags is unavailable", line)
		}
	})

	t.Run("a Go-only tree without ctags", func(t *testing.T) {
		_, search := devFixture(t, goOnly, Options{
			Runner: func(_ context.Context, _ []string) ([]byte, error) {
				return nil, tools.ErrCtagsUnavailable
			},
		})
		r := search("needle", nil)
		if !r.OK {
			t.Fatalf("%s: %s", r.Error, r.Detail)
		}
		if got := r.Data["ctags_available"]; got != false {
			t.Errorf("ctags_available = %v, want false", got)
		}
		if line := firstLineOf(r); !strings.Contains(line, "ctags unavailable") {
			t.Errorf("first line %q should say ctags is unavailable", line)
		}
	})

	t.Run("ctags disabled", func(t *testing.T) {
		_, search := devFixture(t, goOnly, Options{})
		r := search("needle", nil)
		if got := r.Data["ctags_available"]; got != false {
			t.Errorf("ctags_available = %v, want false when disabled", got)
		}
	})
}

// 03-REQ-5.7 (4): a partial index and a truncated result both put their note
// in Data.note.
func TestPartialNoteSurvivesATruncatedResult(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 12; i++ {
		files[fmt.Sprintf("f%02d.go", i)] = "package p\n// needle\n"
	}
	_, search := devFixture(t, files, Options{MaxFiles: 8})

	r := search("needle", map[string]any{"max_files": 2})
	if !r.OK {
		t.Fatalf("%s: %s", r.Error, r.Detail)
	}
	if r.Data["partial"] != true || r.Data["truncated"] != true {
		t.Fatalf("fixture: partial=%v truncated=%v, want both true", r.Data["partial"], r.Data["truncated"])
	}
	note, _ := r.Data["note"].(string)
	if !strings.Contains(note, "search_files") {
		t.Errorf("note = %q, want the partial note telling the model to narrow path or use search_files", note)
	}
	if !strings.Contains(note, "max_files") {
		t.Errorf("note = %q, want the cap marker too", note)
	}
}

// 03-REQ-5.7 (5): a tree with exactly MaxFiles eligible files is complete; the
// bound is hit by the first file past it.
func TestMaxFilesBoundIsTheFirstFilePastTheLimit(t *testing.T) {
	mk := func(n int) map[string]string {
		files := map[string]string{}
		for i := 0; i < n; i++ {
			files[fmt.Sprintf("f%02d.go", i)] = "package p\n// needle\n"
		}
		return files
	}

	_, search := devFixture(t, mk(5), Options{MaxFiles: 5})
	r := search("needle", nil)
	if !r.OK {
		t.Fatalf("%s: %s", r.Error, r.Detail)
	}
	if r.Data["partial"] != false {
		t.Errorf("exactly MaxFiles files: partial = %v, want false (reason %v)", r.Data["partial"], r.Data["partial_reason"])
	}
	if got := r.Data["files_indexed"]; got != 5 {
		t.Errorf("files_indexed = %v, want 5", got)
	}

	_, search = devFixture(t, mk(6), Options{MaxFiles: 5})
	r = search("needle", nil)
	if r.Data["partial"] != true || r.Data["partial_reason"] != "files" {
		t.Errorf("MaxFiles+1 files: partial = %v reason = %v, want true / files", r.Data["partial"], r.Data["partial_reason"])
	}
	if got := r.Data["files_indexed"]; got != 5 {
		t.Errorf("files_indexed = %v, want 5 (what fits)", got)
	}
}

// expiringCtx reports a deadline exceeded after n calls to Err. The walk and
// every later stage of a build ask it, so it stands for a deadline that
// passes partway through the walk.
type expiringCtx struct {
	context.Context
	left atomic.Int32
}

func newExpiringCtx(n int32) *expiringCtx {
	c := &expiringCtx{Context: context.Background()}
	c.left.Store(n)
	return c
}

func (c *expiringCtx) Err() error {
	if c.left.Add(-1) < 0 {
		return context.DeadlineExceeded
	}
	return nil
}

// 03-REQ-5.7 (6), revised for issue #90: a deadline hit in the walk keeps
// the files the walk found — indexed without symbols — for a grace window of
// a quarter of MaxBuildTime, and no longer. Discarding them all made a slow
// walk a permanently empty index; adding them all without limit would make
// the bound no bound.
func TestDeadlineHitInTheWalkKeepsWhatItFoundWithinAGraceWindow(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 20; i++ {
		files[fmt.Sprintf("f%02d.go", i)] = "package p\n// needle\n"
	}
	for _, c := range []struct {
		name     string
		maxBuild time.Duration
		keep     bool
	}{
		{"a grace window keeps the walk", time.Minute, true},
		{"no grace window, no files after the deadline", time.Nanosecond, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx, _ := devFixture(t, files, Options{MaxBuildTime: c.maxBuild})
			runDir := filepath.Join(t.TempDir(), "run")
			if err := os.Mkdir(runDir, 0o700); err != nil {
				t.Fatal(err)
			}
			// The deadline passes a few entries into the walk.
			out, err := idx.doBuild(newExpiringCtx(5), context.Background(), runDir)
			if err != nil {
				t.Fatal(err)
			}
			if out.partialReason != "time" {
				t.Fatalf("partialReason = %q, want time", out.partialReason)
			}
			n := len(out.indexedFiles)
			if c.keep && n == 0 {
				t.Error("the files the walk found before the deadline were discarded")
			}
			if !c.keep && n != 0 {
				t.Errorf("%d files were added after the deadline with no grace window, want 0", n)
			}
		})
	}
}

// 03-REQ-5.2 and the steering rule to reuse (7): the extension table is
// outline's, so a language outline knows is a language the index knows.
func TestLangForExtAgreesWithOutline(t *testing.T) {
	for ext, want := range map[string]string{
		".go": "Go", ".jsx": "JavaScript", ".tsx": "TypeScript", ".mjs": "JavaScript",
		".kts": "Kotlin", ".pyw": "Python", ".mts": "TypeScript", ".zsh": "Shell",
		".hxx": "C++", ".JSX": "JavaScript",
		// Languages outline does not outline but zoekt can name.
		".md": "Markdown", ".json": "JSON", ".yml": "YAML", ".html": "HTML", ".sql": "SQL",
	} {
		if got := langForExt(ext); got != want {
			t.Errorf("langForExt(%q) = %q, want %q", ext, got, want)
		}
	}
	if got := langForExt(".txt"); got != "" {
		t.Errorf("langForExt(.txt) = %q, want none", got)
	}
}

// 03-REQ-4.6 (8): when a cap marker is appended to a result that fits the
// limit by a few bytes, the result is still within the limit, lines are whole,
// no rune is split, and the marker survives.
func TestResultAtTheByteLimitIsNotCutMidLine(t *testing.T) {
	idx, _ := devFixture(t, map[string]string{"a.go": "package p\n"}, Options{})
	// The same info buildResult renders its first line from.
	info := idx.gatherResultInfo(idx.BuildStats())

	// One file whose lines hold two-byte runes, sized to land just under the
	// limit before any marker is added.
	mkFiles := func(pad int) []searchResultFile {
		var lines []resultLine
		for ln := 1; ln <= 100; ln++ {
			lines = append(lines, resultLine{lineNo: ln, text: strings.Repeat("é", 200), match: true})
		}
		lines = append(lines, resultLine{lineNo: 101, text: strings.Repeat("é", pad), match: true})
		return []searchResultFile{{path: "a.go", score: 1, matchCount: 101, chunks: []resultChunk{{lines: lines}}}}
	}
	pad := 0
	for len(renderResult(mkFiles(pad), info)) < tools.DefaultByteLimit-60 {
		pad++
	}
	files := mkFiles(pad)
	full := renderResult(files, info)
	if len(full) > tools.DefaultByteLimit {
		t.Fatalf("fixture: rendered %d bytes, want at most %d", len(full), tools.DefaultByteLimit)
	}

	// totalFiles above maxFiles: the cap marker is appended.
	r := idx.buildResult(files, 1, 2, "")

	if len(r.Text) > tools.DefaultByteLimit {
		t.Errorf("text is %d bytes, over the %d limit", len(r.Text), tools.DefaultByteLimit)
	}
	if !utf8.ValidString(r.Text) {
		t.Error("text is not valid UTF-8: a rune was split")
	}
	if !strings.Contains(r.Text, "max_files") && !strings.Contains(r.Text, "limit reached") {
		t.Errorf("neither the cap marker nor the bytes marker survived; text ends %q", tail(r.Text, 120))
	}
	whole := map[string]bool{}
	for _, l := range strings.Split(full, "\n") {
		whole[l] = true
	}
	// The first line and the file header legitimately change when lines are
	// dropped (the header notes the matches not shown), and the last line is
	// the marker. Every line of content must be a whole line of the result.
	lines := strings.Split(r.Text, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, "  ") {
			continue
		}
		if !whole[l] {
			t.Errorf("line %d of the text is not a whole line of the result: %q", i, tail(l, 80))
			break
		}
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "[") || !strings.HasSuffix(last, "]") {
		t.Errorf("the text does not end with a whole marker line: %q", tail(last, 100))
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// cutLines is the last-resort cut: whole lines only, whatever the bytes.
func TestCutLinesNeverSplitsALineOrARune(t *testing.T) {
	text := "alpha\nbéta\ngamma ünï\ndelta"
	for max := 0; max <= len(text); max++ {
		got := cutLines(text, max)
		if len(got) > max {
			t.Fatalf("cutLines(%d) = %q, longer than max", max, got)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("cutLines(%d) = %q, invalid UTF-8", max, got)
		}
		for _, l := range strings.Split(got, "\n") {
			if l != "" && !strings.Contains(text, l+"\n") && !strings.HasSuffix(text, l) {
				t.Fatalf("cutLines(%d) = %q holds a partial line %q", max, got, l)
			}
		}
	}
}

// 03-REQ-7.2 and 02-REQ-4.5 (3): through the index, files_indexed counts only
// the files within the queried scope, as the table-based find_symbol does.
func TestSymbolsCountsFilesWithinTheQueriedScope(t *testing.T) {
	idx, search := devFixture(t, map[string]string{
		"a/one.go":   "package a\nfunc One() {}\n",
		"a/two.go":   "package a\nfunc Two() {}\n",
		"b/three.go": "package b\nfunc Three() {}\n",
		"b/four.py":  "def four():\n    pass\n",
		"b/five.go":  "package b\nfunc Five() {}\n",
	}, Options{})
	if r := search("One", nil); !r.OK { // builds the index
		t.Fatalf("%s: %s", r.Error, r.Detail)
	}

	ans, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "T", Path: "a"})
	if err != nil || !ok {
		t.Fatalf("Symbols = ok:%v err:%v", ok, err)
	}
	if ans.FilesIndexed != 2 {
		t.Errorf("FilesIndexed for path a = %d, want 2 (the files under a/)", ans.FilesIndexed)
	}

	if got := ans.Backends; len(got) != 1 || got["go/ast"] != 2 {
		t.Errorf("Backends for path a = %v, want {go/ast:2}", got)
	}

	ans, _, _ = idx.Symbols(context.Background(), tools.SymbolQuery{Name: "T", Path: "b"})
	if ans.FilesIndexed != 3 {
		t.Errorf("FilesIndexed for path b = %d, want 3", ans.FilesIndexed)
	}
	if got := ans.Backends; len(got) != 2 || got["go/ast"] != 2 || got["heuristic"] != 1 {
		t.Errorf("Backends for path b = %v, want {go/ast:2 heuristic:1}", got)
	}

	ans, _, _ = idx.Symbols(context.Background(), tools.SymbolQuery{Name: "T"})
	if ans.FilesIndexed != 5 {
		t.Errorf("FilesIndexed for the whole workspace = %d, want 5", ans.FilesIndexed)
	}
}

// Issue #73 §5: a `.h` header holding C++ is indexed as C++, so `lang:c++`
// finds a C++ project's headers; a C header stays C.
func TestLangForSniffsHeaders(t *testing.T) {
	if got := langFor("/r/w.h", []byte("namespace ui {\nclass Widget {};\n}\n")); got != "C++" {
		t.Errorf("langFor(C++ header) = %q, want C++", got)
	}
	if got := langFor("/r/c.h", []byte("struct point { int x; };\n")); got != "C" {
		t.Errorf("langFor(C header) = %q, want C", got)
	}
	if got := langFor("/r/README.md", []byte("# x")); got != "Markdown" {
		t.Errorf("langFor(README.md) = %q, want Markdown", got)
	}
}
