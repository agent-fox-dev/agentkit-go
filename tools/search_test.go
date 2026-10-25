package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// searchTree is a project with ignored, binary, hidden and nested-repository
// files, each holding "needle".
func searchTree(t *testing.T) string {
	t.Helper()
	return testutil.Tree(t, map[string]string{
		".gitignore":                "node_modules/\n*.log\n",
		"main.go":                   "package main\n\nfunc needle() {}\n\nfunc other() {}\n",
		"lib/helper.go":             "package lib\n\n// needle here too\nvar Needle = 1\n",
		"README.md":                 "the needle is in the haystack\n",
		"UPPER.GO":                  "needle in an uppercase-extension file\n",
		"debug.log":                 "needle in an ignored file\n",
		"node_modules/pkg/index.js": "var needle = require('needle')\n",
		".env":                      "needle=hidden\n",
		"vendor/sub/.git/HEAD":      "ref: refs/heads/main\n",
		"vendor/sub/.gitignore":     "secret.txt\n",
		"vendor/sub/ok.go":          "// needle in a nested repo\n",
		"vendor/sub/secret.txt":     "needle in a file the nested repo ignores\n",
		"blob.bin":                  "needle\x00" + strings.Repeat("\x00", 64),
	})
}

func search(t *testing.T, root string, p SearchParams) SearchResult {
	t.Helper()
	res, err := SearchIn(context.Background(), root, p, workspace.NoGlobalExcludes())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func sites(res SearchResult) []string {
	out := []string{}
	for _, m := range res.Matches {
		out = append(out, fmt.Sprintf("%s:%d", m.File, m.Line))
	}
	return out
}

func TestSearchSelectsTheProjectsOwnTextFiles(t *testing.T) {
	root := searchTree(t)
	res := search(t, root, SearchParams{Pattern: "needle"})
	// Ignored, hidden and binary files are skipped, a nested repository's
	// rules apply inside it, and an all-lowercase pattern matches any case.
	want := []string{"README.md:1", "UPPER.GO:1", "lib/helper.go:3", "lib/helper.go:4", "main.go:3", "vendor/sub/ok.go:1"}
	if got := sites(res); !reflect.DeepEqual(got, want) {
		t.Fatalf("matches %v, want %v", got, want)
	}
	// A binary file is selected, so counted, and then not scanned.
	if res.FilesSearched != 6 || res.Truncated {
		t.Fatalf("files_searched %d truncated %v, want 6", res.FilesSearched, res.Truncated)
	}
	// Truncation stops the reading, not the count.
	if res := search(t, root, SearchParams{Pattern: "needle", MaxMatches: 2}); len(res.Matches) != 2 || !res.Truncated || res.FilesSearched != 6 || res.TruncatedBy != TruncatedByLines {
		t.Fatalf("max_matches 2: %v truncated %v files %d", sites(res), res.Truncated, res.FilesSearched)
	}
	// Search reads the real ignore environment, SearchIn the one it is given.
	testutil.Hermetic(t)
	if real, err := Search(context.Background(), root, SearchParams{Pattern: "needle"}); err != nil || !reflect.DeepEqual(sites(real), want) {
		t.Fatalf("Search = %v, %v", sites(real), err)
	}
	for glob, want := range map[string]int{"": 6, "*.go": 4} {
		if n, err := CountCandidates(context.Background(), root, SearchParams{FileGlob: glob}); err != nil || n != want {
			t.Errorf("CountCandidates(%q) = %d, %v; want %d", glob, n, err, want)
		}
	}
}

func TestSearchReadsOnlyTheIgnoreFilesUnderItsRoot(t *testing.T) {
	top := testutil.Tree(t, map[string]string{
		".gitignore":  "*.log\n", // a parent of the search root
		"sub/.ignore": "*.txt\n", // ripgrep's file, not git's
		"sub/a.log":   "needle\n", "sub/b.txt": "needle\n", "sub/c.md": "needle\n",
	})
	if got := sites(search(t, top+"/sub", SearchParams{Pattern: "needle"})); !reflect.DeepEqual(got, []string{"a.log:1", "b.txt:1", "c.md:1"}) {
		t.Fatalf("got %v", got)
	}
}

func TestSearchSemantics(t *testing.T) {
	utf16 := func(s string, little bool) string {
		b := []byte{0xFE, 0xFF}
		if little {
			b = []byte{0xFF, 0xFE}
		}
		for _, r := range s {
			if little {
				b = append(b, byte(r), byte(r>>8))
			} else {
				b = append(b, byte(r>>8), byte(r))
			}
		}
		return string(b)
	}
	root := testutil.Tree(t, map[string]string{
		"u.py":      "def café():\n    return 1\nx = ٣\n",
		"crlf.cs":   "class Foo\r\n{\r\n}\r\n",
		"e.txt":     "Error: boom\nQuoted\n",
		"early.dat": strings.Repeat("x", 13000) + "\nlate needle\n\x00\n",
		"far.dat":   "far needle\n" + strings.Repeat("yyyyyyyyyyyyyyy\n", 20000) + "\x00\nfar needle again\n",
		"le.ps1":    utf16("needle here\nsecond\n", true),
		"be.ps1":    utf16("big needle\n", false) + "\xd8\x00\x00A", // an unpaired high surrogate, then A
		"odd.ps1":   utf16("odd needle", true) + "x",                // a trailing odd byte
		"bom.txt":   "\xEF\xBB\xBFfirst line\nsecond\n",
		"min.js":    "var x = \"" + strings.Repeat("a", 5<<20) + "\"; NEEDLE_ONE\nafter\nNEEDLE_TWO\n",
	})
	yes, no := true, false
	for _, c := range []struct {
		name string
		q    SearchParams
		want []string
	}{
		{`\w is ASCII`, SearchParams{Pattern: `def \w+\(`}, []string{}},
		{`\d is ASCII`, SearchParams{Pattern: `\d`, FileGlob: "u.py"}, []string{"u.py:2"}},
		{`\b is ASCII`, SearchParams{Pattern: `\bcafé\b`}, []string{}},
		{"$ before CRLF", SearchParams{Pattern: `Foo$`}, []string{"crlf.cs:1"}},
		{"^...$ on a CRLF line", SearchParams{Pattern: `^\{$`}, []string{"crlf.cs:2"}},
		{"a NUL in the first 64 KiB is binary", SearchParams{Pattern: `late needle`}, []string{}},
		{"a later NUL ends the file's search", SearchParams{Pattern: `far needle`}, []string{"far.dat:1"}},
		{"smart-case ignores escapes", SearchParams{Pattern: `error\S`}, []string{"e.txt:1"}},
		{"an uppercase literal is sensitive", SearchParams{Pattern: `ERROR\S`}, []string{}},
		{"case_sensitive true wins", SearchParams{Pattern: `error\S`, CaseSensitive: &yes}, []string{}},
		{"case_sensitive false wins", SearchParams{Pattern: `ERROR`, CaseSensitive: &no}, []string{"e.txt:1"}},
		{"UTF-16LE with a BOM is transcoded", SearchParams{Pattern: `^needle here$`}, []string{"le.ps1:1"}},
		{"UTF-16BE with a BOM is transcoded", SearchParams{Pattern: `^big needle$`}, []string{"be.ps1:1"}},
		{"an unpaired surrogate is U+FFFD", SearchParams{Pattern: "^�A$"}, []string{"be.ps1:2"}},
		{"a trailing odd byte is U+FFFD", SearchParams{Pattern: "odd needle�$"}, []string{"odd.ps1:1"}},
		{"a UTF-8 BOM is not part of line 1", SearchParams{Pattern: `^first`}, []string{"bom.txt:1"}},
		{"a 5 MB line does not end the search", SearchParams{Pattern: `NEEDLE_`, FileGlob: "*.js"}, []string{"min.js:1", "min.js:3"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := sites(search(t, root, c.q)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%q matched %v, want %v", c.q.Pattern, got, c.want)
			}
		})
	}
	if m := search(t, root, SearchParams{Pattern: `NEEDLE_ONE`}).Matches[0]; utf8.RuneCountInString(m.Text) != SearchLineChars+1 || !strings.HasSuffix(m.Text, "…") {
		t.Errorf("a long line is returned whole: %d runes", utf8.RuneCountInString(m.Text))
	}
}

func TestHasUppercaseLiteralReadsOnlyTheLiteralText(t *testing.T) {
	for pat, want := range map[string]bool{
		`error\S\W\D\B\A`: false, `\p{Lu}x`: false, `\pLx`: false, `\x{41}`: false, `\x41`: false,
		`(?i)x`: false, `(?P<Name>x)`: false, `(?<Name>x)`: false, `(?-s:x)`: false,
		`\Qa.B\E`: true, `\Qab\E`: false, `\Qab`: false, `(?<=A)x`: true, `É`: true, `Error`: true, `x\`: false,
	} {
		if got := hasUppercaseLiteral(pat); got != want {
			t.Errorf("hasUppercaseLiteral(%q) = %v, want %v", pat, got, want)
		}
	}
}

func TestSearchContextLines(t *testing.T) {
	root := searchTree(t)
	m := search(t, root, SearchParams{Pattern: "func needle", ContextLines: 2, FileGlob: "main.go"}).Matches
	if len(m) != 1 || !reflect.DeepEqual(m[0].Before, []string{"package main", ""}) || !reflect.DeepEqual(m[0].After, []string{"", "func other() {}"}) {
		t.Fatalf("matches %+v", m)
	}
	// The glob is AgentKit's dialect, relative to the search root.
	if got := sites(search(t, root+"/lib", SearchParams{Pattern: "needle", FileGlob: "*.{go,md}"})); !reflect.DeepEqual(got, []string{"helper.go:3", "helper.go:4"}) {
		t.Fatalf("a relative glob: %v", got)
	}
}

func TestSearchAppliesTheByteCapBeforeTheMatchCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "needle %03d %s\n", i, strings.Repeat("x", 480))
	}
	root := testutil.Tree(t, map[string]string{"wide.txt": b.String()})
	res := search(t, root, SearchParams{Pattern: "needle", ContextLines: 20})
	if !res.Truncated || res.TruncatedBy != TruncatedByBytes || len(res.Matches) >= SearchMatchCap || len(testutil.MustJSON(t, res.Matches)) > DefaultByteLimit+1024 {
		t.Fatalf("truncated %v by %q with %d matches", res.Truncated, res.TruncatedBy, len(res.Matches))
	}
	ws, _ := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	r := mustOK(t, call(toolSet(t, ws, Options{})["search_files"], `{"pattern":"needle","context_lines":20}`))
	if r.Metadata.TruncatedBy != "bytes" || r.Data["note"] != SearchBytesMarker(len(res.Matches), DefaultByteLimit) {
		t.Fatalf("the tool: %+v note %v", r.Metadata, r.Data["note"])
	}
}

func TestTheSearchTool(t *testing.T) {
	root := searchTree(t)
	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	st := toolSet(t, ws, Options{})["search_files"]
	r := mustOK(t, call(st, `{"pattern":"needle","max_matches":2}`))
	if r.Data["note"] != SearchMarker(2) || r.Metadata.TruncatedBy != "lines" || !strings.HasSuffix(r.Text, "\n"+SearchMarker(2)) {
		t.Errorf("the marker names max_matches: %v", r.Data["note"])
	}
	if r := mustOK(t, call(st, `{"pattern":"needle","path":"lib","context_lines":1}`)); r.Text != "helper.go\n  2- \n  3: // needle here too\n  4: var Needle = 1" {
		t.Errorf("Text = %q", r.Text)
	}
	if r := mustOK(t, call(st, `{"pattern":"zzz"}`)); r.Text != "No matches (6 files searched)." || r.Data["matches"] == nil {
		t.Errorf("no match: %q", r.Text)
	}
	for args, detail := range map[string]string{
		`{"pattern":" "}`:                    "pattern is required",
		`{"pattern":"x","context_lines":25}`: "context_lines must be between 0 and 20 (got 25); retry with context_lines=20 or less, or omit it for no context",
		`{"pattern":"x","context_lines":-1}`: "context_lines must be between 0 and 20 (got -1); retry with context_lines=20 or less, or omit it for no context",
	} {
		if r := call(st, args); r.Error != "invalid_arguments" || r.Detail != detail {
			t.Errorf("%s: %q %q\nwant %q", args, r.Error, r.Detail, detail)
		}
	}
	if r := call(st, `{"pattern":"foo("}`); r.Error != "invalid_arguments" || !strings.HasPrefix(r.Detail, "invalid pattern: error parsing regexp: missing closing )") ||
		!strings.HasSuffix(r.Detail, `The pattern is a regular expression (RE2): write a literal metacharacter with a backslash, for example \( for a literal ( or \. for a literal .`) {
		t.Errorf("a bad pattern: %q %q", r.Error, r.Detail)
	}
	var perr *SearchPatternError
	if _, err := Search(context.Background(), root, SearchParams{Pattern: "a(b"}); !errors.As(err, &perr) || errors.Unwrap(perr) == nil {
		t.Errorf("a bad pattern is a *SearchPatternError: %v", err)
	}
	// A max_matches over the cap is clamped by the argument repair, not refused.
	for _, raw := range []string{`{"pattern":"x","max_matches":500}`, `{"pattern":"x","max_matches":50}`, `{"pattern":"x","max_matches":"many"}`} {
		c, err := core.NewToolUse("c", "search_files", json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		p, err := core.PrepareArguments(st, c)
		want := map[string]string{`{"pattern":"x","max_matches":500}`: "100", `{"pattern":"x","max_matches":50}`: "50"}[raw]
		if want != "" && (err != nil || fmt.Sprint(p.Args["max_matches"]) != want) {
			t.Errorf("%s: max_matches %v, %v; want %s", raw, p.Args["max_matches"], err, want)
		}
	}
	if args := st.PrepareArguments(map[string]any{"max_matches": float64(101)}); args["max_matches"] != SearchMatchCap {
		t.Errorf("a float64 over the cap: %v", args)
	}
	if args := st.PrepareArguments(map[string]any{"max_matches": json.Number("x")}); args["max_matches"] != json.Number("x") {
		t.Errorf("a malformed number is left for validation: %v", args)
	}
}

func TestRenderSearchTextIsGrepStyleFileByFile(t *testing.T) {
	res := SearchResult{FilesSearched: 7, Matches: []SearchMatch{
		{File: "a/b.go", Line: 12, Text: "\tx := needle()", Before: []string{"func f() {"}, After: []string{"\treturn x", "}"}},
		{File: "a/b.go", Line: 13, Text: "\treturn needle", Before: []string{"\tx := needle()"}, After: []string{"}"}},
		{File: "c.md", Line: 1, Text: "needle"},
	}}
	// A line that is both a match and another match's context is the match.
	want := "a/b.go\n  11- func f() {\n  12: \tx := needle()\n  13: \treturn needle\n  14- }\n\nc.md\n  1: needle\n[m]"
	if got := RenderSearchText(res, "[m]"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := capLine(strings.Repeat("é", 300)); got != strings.Repeat("é", 300) {
		t.Errorf("300 two-byte runes are under the 500-character cap")
	}
	if got := capLine(strings.Repeat("é", 600)); !utf8.ValidString(got) || got != strings.Repeat("é", 500)+"…" {
		t.Errorf("capLine cut a rune or kept %d runes", utf8.RuneCountInString(got))
	}
	if !bytes.Equal([]byte(capLine("plain")), []byte("plain")) {
		t.Error("a short line is unchanged")
	}
}
