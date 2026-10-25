//go:build !windows

package codesearch

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/tools"
	zoekt "github.com/sourcegraph/zoekt"
)

// zoektChunk is a chunk whose lines first..last all match.
func zoektChunk(first, last int) zoekt.ChunkMatch {
	var content strings.Builder
	for ln := first; ln <= last; ln++ {
		fmt.Fprintf(&content, "line %d\n", ln)
	}
	return zoekt.ChunkMatch{
		Content:      []byte(content.String()),
		ContentStart: zoekt.Location{LineNumber: uint32(first), Column: 1},
		Ranges: []zoekt.Range{{
			Start: zoekt.Location{LineNumber: uint32(first)},
			End:   zoekt.Location{LineNumber: uint32(last)},
		}},
	}
}

// A file shows at most three chunks: zoekt's best, in zoekt's order. A chunk
// touching a better one folds into its slot, one bridging several merges them
// into the best, and the lines of a merged chunk are ascending.
func TestConvertFileMatchesKeepsZoektsBestChunks(t *testing.T) {
	for _, c := range []struct {
		name   string
		chunks []zoekt.ChunkMatch
		want   [][]int
	}{
		{"best three in zoekt order",
			[]zoekt.ChunkMatch{zoektChunk(100, 100), zoektChunk(10, 10), zoektChunk(30, 30), zoektChunk(50, 50), zoektChunk(70, 70)},
			[][]int{{100}, {10}, {30}}},
		{"an overlapping chunk folds into the better slot",
			[]zoekt.ChunkMatch{zoektChunk(50, 50), zoektChunk(10, 10), zoektChunk(51, 51), zoektChunk(90, 90)},
			[][]int{{50, 51}, {10}, {90}}},
		{"a bridging chunk merges into the best slot",
			[]zoekt.ChunkMatch{zoektChunk(10, 11), zoektChunk(14, 15), zoektChunk(12, 13), zoektChunk(90, 90)},
			[][]int{{10, 11, 12, 13, 14, 15}, {90}}},
		{"merged lines are ascending",
			[]zoekt.ChunkMatch{zoektChunk(20, 21), zoektChunk(18, 19), zoektChunk(22, 22)},
			[][]int{{18, 19, 20, 21, 22}}},
	} {
		files := convertFileMatches([]zoekt.FileMatch{{FileName: "f.go", ChunkMatches: c.chunks}}, 10, 0, nil)
		var got [][]int
		for _, ch := range files[0].chunks {
			var lines []int
			for _, l := range ch.lines {
				lines = append(lines, l.lineNo)
			}
			got = append(got, lines)
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: shown %v, want %v", c.name, got, c.want)
		}
	}
}

// A declaration at the bottom of a file outranks the comment mentions above
// it, so its chunk is shown, and first.
func TestCodeSearchShowsTheBestChunksNotTheEarliest(t *testing.T) {
	src := "package pkg\n" + strings.Repeat(strings.Repeat("// filler\n", 10)+"// the needle is mentioned here\n", 5) +
		strings.Repeat("// filler\n", 10) + "func needle() {}\n"
	idx := newFixture(t, map[string]string{"many.go": src}, Options{})
	r := find(t, idx, "needle", "context_lines", 1)
	chunks := r.Data["files"].([]any)[0].(map[string]any)["chunks"].([]any)
	var first []int
	for _, l := range chunks[0].(map[string]any)["lines"].([]any) {
		first = append(first, l.(map[string]any)["line"].(int))
	}
	if declLine := 1 + 5*11 + 10 + 1; len(chunks) > maxChunksPerFile || !slices.Contains(first, declLine) {
		t.Errorf("%d chunks, the first showing lines %v; want the declaration at line %d first", len(chunks), first, declLine)
	}
}

// A sym: hit outranks a comment hit, and each file shows at most three chunks
// of ascending lines, each line cut to SearchLineChars runes.
func TestResultsAreRankedAndCapped(t *testing.T) {
	six := "package main\n" + strings.Repeat(strings.Repeat("// filler\n", 5)+"// matchword here\n", 6)
	long := "// matchword " + strings.Repeat("é", tools.SearchLineChars+100)
	idx := newFixture(t, map[string]string{
		"decl.go":       "package main\n\nfunc Runner() {\n\t// body\n}\n",
		"comment.go":    "package main\n\n// Runner is mentioned here\nvar x = 1\n",
		"sixmatches.go": six,
		"longline.go":   "package main\n" + long + "\n",
	}, Options{})

	if got := paths(find(t, idx, "sym:Runner", "max_files", 2)); len(got) == 0 || got[0] != "decl.go" {
		t.Errorf("sym:Runner ranked %v, want decl.go first", got)
	}

	r := find(t, idx, "matchword", "context_lines", 1)
	if !strings.Contains(r.Text, "sixmatches.go (6 matches) [3 matches not shown]") {
		t.Errorf("the six-match file's header is missing its note:\n%s", r.Text)
	}
	for _, f := range r.Data["files"].([]any) {
		chunks := f.(map[string]any)["chunks"].([]any)
		if len(chunks) > maxChunksPerFile {
			t.Errorf("%v shows %d chunks", f.(map[string]any)["path"], len(chunks))
		}
		for _, c := range chunks {
			prev := 0
			for _, l := range c.(map[string]any)["lines"].([]any) {
				line := l.(map[string]any)
				if n := line["line"].(int); n <= prev {
					t.Errorf("line %d after %d", n, prev)
				} else {
					prev = n
				}
				if text := line["text"].(string); utf8.RuneCountInString(text) > tools.SearchLineChars+1 {
					t.Errorf("a line of %d runes was not cut", utf8.RuneCountInString(text))
				}
			}
		}
	}
	if want := "  2: " + string([]rune(long)[:tools.SearchLineChars]) + "…"; !strings.Contains(r.Text, want) {
		t.Error("the long line is not cut to SearchLineChars runes with an ellipsis")
	}
}

// A header names the file, its match count, the matches not shown and the
// declarations starting on matched lines, at most five, members as
// Container.Name; a control character in a path prints as '?'.
func TestFileHeadersNameTheDeclarationsOnMatchedLines(t *testing.T) {
	seven := "package main\n"
	for i := range 7 {
		seven += fmt.Sprintf("func Decl%d() {} // matchword\n", i)
	}
	idx := newFixture(t, map[string]string{
		"server.go":      "package main\n\ntype Server struct{}\n\nfunc (Server) Run() {} // matchword\n",
		"sevendecls.go":  seven,
		"bad\x01name.go": "package main\n// matchword\n",
	}, Options{})
	r := find(t, idx, "matchword", "context_lines", 0)
	for _, want := range []string{
		"sevendecls.go (7 matches) {Decl0, Decl1, Decl2, Decl3, Decl4}",
		"server.go (1 matches) {Server.Run}",
		"bad?name.go (1 matches)",
	} {
		if !strings.Contains(r.Text, want+"\n") {
			t.Errorf("no header %q in:\n%s", want, r.Text)
		}
	}
	if !regexp.MustCompile(`(?m)^  \d+: `).MatchString(r.Text) {
		t.Errorf("no matched line printed as \"  N: text\":\n%s", r.Text)
	}
	if r := find(t, idx, "Runx or Server", "context_lines", 1); !regexp.MustCompile(`(?m)^  \d+- `).MatchString(r.Text) {
		t.Errorf("no context line printed as \"  N- text\":\n%s", r.Text)
	}
}

func TestSanitizePathReplacesControlCharactersAndInvalidBytes(t *testing.T) {
	for in, want := range map[string]string{
		"a/b.go":        "a/b.go",
		"bad\x01name":   "bad?name",
		"tab\there":     "tab\there",
		"bad\xffbyte":   "bad?byte",
		"über/naïve.go": "über/naïve.go",
	} {
		if got := sanitizePath(in); got != want {
			t.Errorf("sanitizePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderFirstLineStatesTheIndexState(t *testing.T) {
	for _, c := range []struct {
		info    resultInfo
		matched int
		want    string
	}{
		{resultInfo{filesIndexed: 3}, 0, "[0 files matched; 3 files indexed]"},
		{resultInfo{filesIndexed: 3, indexSizeBytes: 512, symbolSources: map[string]int{"tree-sitter": 1, "go/ast": 2}}, 2,
			"[2 files matched; 3 files indexed; index 512 B; symbols: go/ast: 2, tree-sitter: 1]"},
		{resultInfo{filesIndexed: 5, indexSizeBytes: 1536, partial: true, partialReason: "files", dirtyFiles: 2}, 1,
			"[1 files matched; 5 files indexed; index 1.5 KB; partial (files limit); 2 dirty files overlaid]"},
		{resultInfo{indexSizeBytes: 3 << 20, partial: true}, 0, "[0 files matched; 0 files indexed; index 3.0 MB; partial (unknown limit)]"},
	} {
		if got := renderFirstLine(c.info, c.matched); got != c.want {
			t.Errorf("renderFirstLine = %q, want %q", got, c.want)
		}
	}
}

// The first line counts the files the result shows, not only those indexed.
func TestTheFirstLineStatesTheMatchedAndIndexedFiles(t *testing.T) {
	idx := newFixture(t, map[string]string{
		"a.go": "package p\n// needle\n",
		"b.go": "package p\n// needle\n",
		"c.go": "package p\n// other\n",
	}, Options{})
	for _, c := range []struct {
		query string
		kv    []any
		want  string
	}{
		{"needle", nil, "2 files matched"},
		{"absent", nil, "0 files matched"},
		{"needle", []any{"max_files", 1}, "1 files matched"},
	} {
		first := strings.SplitN(find(t, idx, c.query, c.kv...).Text, "\n", 2)[0]
		if !strings.Contains(first, c.want+"; 3 files indexed; index ") || !strings.Contains(first, "symbols: go/ast: 3") {
			t.Errorf("%s %v: first line %q, want %q and the index state", c.query, c.kv, first, c.want)
		}
	}
}

func TestNoMatchIsASuccessThatSaysSo(t *testing.T) {
	idx := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{})
	r := find(t, idx, "zzzznomatch")
	if lines := strings.Split(r.Text, "\n"); len(lines) != 2 || lines[1] != "No matches found." || len(paths(r)) != 0 {
		t.Errorf("text %q, files %v", r.Text, paths(r))
	}
	if r.Metadata != nil || r.Data["truncated"] != false {
		t.Errorf("a result with no match is marked truncated: %+v %v", r.Metadata, r.Data["truncated"])
	}
}

// More files than max_files ends the text with the shared cap marker, which
// at the cap of 25 says so instead of suggesting more.
func TestMoreFilesThanMaxFilesEndsWithTheCapMarker(t *testing.T) {
	idx := newFixture(t, numbered(30, "f%03d.go", "package pkg\n// commonword here\n"), Options{})
	for _, max := range []int{10, 25} {
		r := find(t, idx, "commonword", "max_files", max)
		want := tools.CapMarker("files", "max_files", max, 25, "narrow the query or add a path")
		if !strings.HasSuffix(r.Text, "\n"+want) || r.Data["note"] != want || r.Data["truncated"] != true {
			t.Errorf("max_files %d: text ends %q, note %q", max, r.Text[len(r.Text)-120:], r.Data["note"])
		}
		if r.Metadata == nil || r.Metadata.TruncatedBy != string(tools.TruncatedByLines) {
			t.Errorf("max_files %d: metadata %+v, want truncated by lines", max, r.Metadata)
		}
	}
}

// A result over the byte limit drops whole files from the end, then chunks
// and lines of the last one, and ends with the bytes marker; no marker
// suggests more than the cap of 25 files.
func TestAResultOverTheByteLimitIsCutToIt(t *testing.T) {
	var many strings.Builder
	for j := range 40 {
		fmt.Fprintf(&many, "// bigword line %d %s\n", j, strings.Repeat("x", 80))
	}
	var huge strings.Builder
	for j := range 1000 {
		fmt.Fprintf(&huge, "// bigword line %d %s\n", j, strings.Repeat("y", 80))
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		kv    []any
	}{
		{"many files", numbered(25, "big%03d.go", "package pkg\n"+many.String()), []any{"max_files", 25, "context_lines", 20}},
		{"one huge file", map[string]string{"huge.go": "package pkg\n" + huge.String()}, []any{"max_files", 1, "context_lines", 20}},
		{"one huge chunk", map[string]string{"huge.go": "package pkg\n" + huge.String()}, []any{"max_files", 1, "context_lines", 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, c.files, Options{})
			r := find(t, idx, "bigword", c.kv...)
			if len(r.Text) > tools.DefaultByteLimit || len(paths(r)) < 1 {
				t.Errorf("%d bytes, %d files; want at most %d bytes and a file", len(r.Text), len(paths(r)), tools.DefaultByteLimit)
			}
			if !strings.HasSuffix(r.Text, bytesMarker()) || r.Data["note"] != bytesMarker() {
				t.Errorf("text ends %q, want the bytes marker", r.Text[len(r.Text)-80:])
			}
			if r.Metadata == nil || r.Metadata.TruncatedBy != string(tools.TruncatedByBytes) {
				t.Errorf("metadata %+v, want truncated by bytes", r.Metadata)
			}
		})
	}

	idx := newFixture(t, numbered(40, "p%03d.go", "package pkg\n"+many.String()), Options{})
	for _, mf := range []int{1, 10, 25, 40} {
		for _, cl := range []int{0, 5, 20} {
			r := find(t, idx, "bigword", "max_files", mf, "context_lines", cl)
			if len(r.Text) > tools.DefaultByteLimit {
				t.Errorf("max_files %d context_lines %d: %d bytes", mf, cl, len(r.Text))
			}
			for _, m := range regexp.MustCompile(`max_files=(\d+)`).FindAllStringSubmatch(r.Text, -1) {
				if n, _ := strconv.Atoi(m[1]); n > maxMaxFiles {
					t.Errorf("a marker suggests max_files=%d", n)
				}
			}
		}
	}
}

// A result just under the limit that a cap marker would push over is cut at a
// line boundary: within the limit, no rune split, ending with a whole marker.
func TestAResultAtTheByteLimitIsNotCutMidLine(t *testing.T) {
	idx := newFixture(t, map[string]string{"a.go": "package p\n"}, Options{})
	info := idx.gatherResultInfo(idx.BuildStats())
	mkFiles := func(pad int) []searchResultFile {
		var lines []resultLine
		for ln := 1; ln <= 100; ln++ {
			lines = append(lines, resultLine{lineNo: ln, text: strings.Repeat("é", 200), match: true})
		}
		lines = append(lines, resultLine{lineNo: 101, text: strings.Repeat("é", pad), match: true})
		return []searchResultFile{{path: "a.go", score: 1, matchCount: 101, chunks: []resultChunk{{lines: lines}}}}
	}
	// Each pad rune is two bytes: the first pad that reaches 60 bytes short.
	pad := (tools.DefaultByteLimit - 60 - len(renderResult(mkFiles(0), info)) + 1) / 2
	full := renderResult(mkFiles(pad), info)

	r := idx.buildResult(mkFiles(pad), 1, 2, "")
	if len(r.Text) > tools.DefaultByteLimit || !utf8.ValidString(r.Text) {
		t.Fatalf("%d bytes (limit %d), valid UTF-8 %v", len(r.Text), tools.DefaultByteLimit, utf8.ValidString(r.Text))
	}
	lines := strings.Split(r.Text, "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "  ") && !strings.Contains(full, l+"\n") {
			t.Fatalf("a line of the text is not a whole line of the result: %q", l[:40])
		}
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "[") || !strings.HasSuffix(last, "]") {
		t.Errorf("the text does not end with a whole marker line: %q", last)
	}
}

// cutLines is the last-resort cut: whole lines only, whatever the bytes.
func TestCutLinesNeverSplitsALineOrARune(t *testing.T) {
	text := "alpha\nbéta\ngamma ünï\ndelta"
	for max := -1; max <= len(text); max++ {
		got := cutLines(text, max)
		if len(got) > max && max >= 0 || !utf8.ValidString(got) {
			t.Fatalf("cutLines(%d) = %q", max, got)
		}
		if got != "" && !strings.HasPrefix(text, got) || got != text && got != "" && text[len(got)] != '\n' {
			t.Fatalf("cutLines(%d) = %q is not a run of whole lines", max, got)
		}
	}
	if got := cutLines(text, -1); got != "" {
		t.Errorf("cutLines(-1) = %q", got)
	}
}
