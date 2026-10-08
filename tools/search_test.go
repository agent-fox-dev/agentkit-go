package tools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

type coreTool = core.Tool

// searchTree builds a fixture with the things that separate a real search tool
// from a regexp over filepath.Walk: an ignored directory, a nested repository,
// a binary file, and content that matches in more than one case.
func searchTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".gitignore", "node_modules/\n*.log\n")
	write("main.go", "package main\n\nfunc needle() {}\n\nfunc other() {}\n")
	write("lib/helper.go", "package lib\n\n// needle here too\nvar Needle = 1\n")
	write("README.md", "the needle is in the haystack\n")
	write("debug.log", "needle in an ignored file\n")
	write("node_modules/pkg/index.js", "var needle = require('needle')\n")

	// A nested repository with its own rules: they apply within it and do not
	// leak outward (REQ-TOOL-05.3).
	write("vendor/sub/.git/HEAD", "ref: refs/heads/main\n")
	write("vendor/sub/.gitignore", "secret.txt\n")
	write("vendor/sub/ok.go", "// needle in a nested repo\n")
	write("vendor/sub/secret.txt", "needle in an ignored nested file\n")

	// A file whose extension differs from the glob only in CASE. AgentKit's
	// glob dialect is smart-case and ripgrep's is not, which is exactly the
	// divergence the accelerated path must not inherit.
	write("UPPER.GO", "needle in an uppercase-extension file\n")

	// Binary: a NUL byte in the first block.
	if err := os.WriteFile(filepath.Join(root, "blob.bin"),
		append([]byte("needle\x00"), make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func names(res tools.SearchResult) []string {
	out := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		out = append(out, fmt.Sprintf("%s:%d", m.File, m.Line))
	}
	return out
}

// ---- the declared semantics

// TestSearchSkipsIgnoredAndBinaryFiles is the difference REQ-TOOL-05 names
// between a search tool and a regexp over a walk: one returns the project's
// files, the other returns node_modules.
func TestSearchSkipsIgnoredAndBinaryFiles(t *testing.T) {
	root := searchTree(t)
	res, err := runNative(t, root, tools.SearchParams{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(res), " ")
	for _, forbidden := range []string{"node_modules", "debug.log", "blob.bin"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("%s must not be searched; got %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "lib/helper.go") {
		t.Fatalf("expected the project's own files; got %s", got)
	}
}

// TestANestedRepositorysRulesDoNotLeakOutward is REQ-TOOL-05.3. The nested
// repo ignores secret.txt; the outer tree does not, and the outer tree's rules
// say nothing about it either way.
func TestANestedRepositorysRulesDoNotLeakOutward(t *testing.T) {
	root := searchTree(t)
	res, err := runNative(t, root, tools.SearchParams{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(names(res), " ")
	if strings.Contains(got, "secret.txt") {
		t.Fatalf("the nested repo ignores secret.txt; got %s", got)
	}
	if !strings.Contains(got, "vendor/sub/ok.go") {
		t.Fatalf("the nested repo's other files are still searched; got %s", got)
	}
}

// TestSmartCaseIsTheDefault. AgentKit's declared semantics, not ripgrep's:
// absent means smart-case, so a lowercase pattern matches any case and a
// pattern carrying an uppercase rune does not.
func TestSmartCaseIsTheDefault(t *testing.T) {
	root := searchTree(t)
	yes, no := true, false
	lower, err := runNative(t, root, tools.SearchParams{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(names(lower), " "), "lib/helper.go:4") {
		t.Fatalf("an all-lowercase pattern must match `var Needle`; got %v", names(lower))
	}

	upper, err := runNative(t, root, tools.SearchParams{Pattern: "Needle"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range upper.Matches {
		if !strings.Contains(m.Text, "Needle") {
			t.Fatalf("an uppercase rune makes the pattern sensitive; %q matched", m.Text)
		}
	}

	forced, err := runNative(t, root, tools.SearchParams{
		Pattern: "Needle", CaseSensitive: &no})
	if err != nil {
		t.Fatal(err)
	}
	if len(forced.Matches) <= len(upper.Matches) {
		t.Fatal("case_sensitive=false must widen the result beyond smart-case")
	}

	strict, err := runNative(t, root, tools.SearchParams{
		Pattern: "needle", CaseSensitive: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if len(strict.Matches) >= len(lower.Matches) {
		t.Fatal("case_sensitive=true must narrow the result below smart-case; " +
			"absent and false are not the same answer")
	}
}

// TestContextLinesSurroundTheMatch.
func TestContextLinesSurroundTheMatch(t *testing.T) {
	root := searchTree(t)
	res, err := runNative(t, root, tools.SearchParams{
		Pattern: "func needle", ContextLines: 2, FileGlob: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("want 1 match, got %v", names(res))
	}
	m := res.Matches[0]
	if len(m.Before) != 2 || m.Before[1] != "" || m.Before[0] != "package main" {
		t.Fatalf("before context wrong: %q", m.Before)
	}
	if len(m.After) != 2 || m.After[1] != "func other() {}" {
		t.Fatalf("after context wrong: %q", m.After)
	}
}

// TestTheFileGlobUsesAgentKitsDialect. rg's glob dialect is close but not
// identical; ours is the declared one, so the accelerated path re-filters.
func TestTheFileGlobUsesAgentKitsDialect(t *testing.T) {
	root := searchTree(t)
	res, err := runNative(t, root, tools.SearchParams{
		Pattern: "needle", FileGlob: "**/*.{go,md}"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) == 0 {
		t.Fatal("brace expansion must select the .go and .md files")
	}
	var sawUpper bool
	for _, m := range res.Matches {
		ext := strings.ToLower(filepath.Ext(m.File))
		if ext != ".go" && ext != ".md" {
			t.Fatalf("%s does not match the glob", m.File)
		}
		if m.File == "UPPER.GO" {
			sawUpper = true
		}
	}
	// Smart-case globbing is AgentKit's declared dialect and ripgrep's
	// is case-sensitive, so this is the assertion that the accelerated
	// path did not inherit ripgrep's rules.
	if !sawUpper {
		t.Fatalf("an all-lowercase glob must match UPPER.GO; got %v", names(res))
	}
}

// TestSearchTruncatesAtMaxMatches.
func TestSearchTruncatesAtMaxMatches(t *testing.T) {
	root := searchTree(t)
	res, err := runNative(t, root, tools.SearchParams{
		Pattern: "needle", MaxMatches: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 2 || !res.Truncated {
		t.Fatalf("want 2 matches and truncated=true; got %d, %v",
			len(res.Matches), res.Truncated)
	}
}

// TestAnInvalidPatternIsAnArgumentError, not a failed search: the caller has to
// change the pattern, and a retry cannot help.
func TestAnInvalidPatternIsAnArgumentError(t *testing.T) {
	root := searchTree(t)
	_, err := tools.Search(context.Background(), root, tools.SearchParams{Pattern: "a(b"})
	if err == nil {
		t.Fatal("an unparseable pattern must be refused")
	}
	var perr *tools.SearchPatternError
	if !asPatternError(err, &perr) {
		t.Fatalf("want a SearchPatternError so the tool can report invalid_arguments; got %T", err)
	}
}

// ---- the parity test REQ-TOOL-05 requires

// ---- the tool envelope

func TestSearchToolRejectsAPathOutsideTheWorkspace(t *testing.T) {
	root := searchTree(t)
	tool := searchTool(t, root)
	res := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"needle","path":"../.."}`))
	if res.OK || res.Error != "path_not_allowed" {
		t.Fatalf("want path_not_allowed, got %+v", res)
	}
}

func TestSearchToolBoundsContextLines(t *testing.T) {
	root := searchTree(t)
	tool := searchTool(t, root)
	res := tool.Execute(context.Background(),
		json.RawMessage(`{"pattern":"needle","context_lines":500}`))
	if res.OK || res.Error != "invalid_arguments" {
		t.Fatalf("context must be bounded: 100 matches with 500 lines either side is "+
			"50000 lines the model pays for; got %+v", res)
	}
}

// TestSearchAppliesTheByteCap is REQ-TOOL-09's second limit for search_files
// and REQ-TOOL-15.3. 100 matches with 20 lines of context either side at 500
// chars a line is two megabytes; the 50 KB cap must fire first and say so.
func TestSearchAppliesTheByteCap(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "needle %03d %s\n", i, strings.Repeat("x", 480))
	}
	if err := os.WriteFile(filepath.Join(root, "wide.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := runNative(t, root, tools.SearchParams{Pattern: "needle", ContextLines: 20})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || res.TruncatedBy != tools.TruncatedByBytes {
		t.Fatalf("want truncated_by=bytes, got truncated=%v by=%q with %d matches",
			res.Truncated, res.TruncatedBy, len(res.Matches))
	}
	if len(res.Matches) >= tools.SearchMatchCap {
		t.Fatal("the byte cap must fire BEFORE the match cap here")
	}
	payload, _ := json.Marshal(res.Matches)
	if len(payload) > tools.DefaultByteLimit+1024 {
		t.Fatalf("payload is %d bytes, over the 50 KB budget", len(payload))
	}

	// The envelope names the limit that fired and a call that narrows.
	out := searchTool(t, root).Execute(context.Background(),
		json.RawMessage(`{"pattern":"needle","context_lines":20}`))
	if !out.OK || out.Metadata == nil || out.Metadata.TruncatedBy != "bytes" {
		t.Fatalf("metadata.truncated_by must be \"bytes\": %+v", out.Metadata)
	}
	note, _ := out.Data["note"].(string)
	if !strings.Contains(note, "50.0KB") || strings.Contains(note, "limit=") {
		t.Fatalf("the marker must name the byte limit, not find_files' limit=: %q", note)
	}
}

// TestSearchMarkerNamesMaxMatches is REQ-TOOL-09b for search_files: the
// marker used to say `limit=`, a parameter the tool does not have.
func TestSearchMarkerNamesMaxMatches(t *testing.T) {
	root := searchTree(t)
	res := searchTool(t, root).Execute(context.Background(),
		json.RawMessage(`{"pattern":"needle","max_matches":2}`))
	note, _ := res.Data["note"].(string)
	if !strings.Contains(note, "max_matches=4") || strings.Contains(note, "limit=") {
		t.Fatalf("marker = %q, want it to name max_matches", note)
	}
}

// ---- helpers

func runNative(t *testing.T, root string, p tools.SearchParams) (tools.SearchResult, error) {
	t.Helper()
	// The global excludes layer is pinned EMPTY (NFR-TEST-04): a developer's
	// own ~/.config/git/ignore must not decide whether this test passes.
	return tools.SearchIn(context.Background(), root, p, tools.NoGlobalExcludes())
}

func dump(r tools.SearchResult) string {
	b, _ := json.Marshal(r.Matches)
	return string(b)
}

func asPatternError(err error, target **tools.SearchPatternError) bool {
	for err != nil {
		if p, ok := err.(*tools.SearchPatternError); ok {
			*target = p
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// searchTool builds the real tool against a workspace rooted at root.
func searchTool(t *testing.T, root string) coreTool {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name == "search_files" {
			return tl
		}
	}
	t.Fatal("search_files is not in the default tool set")
	return coreTool{}
}

// ---- REQ-TOOL-14.6: read_file detects images by magic bytes

func readTool(t *testing.T, root string) coreTool {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name == "read_file" {
			return tl
		}
	}
	t.Fatal("read_file is missing")
	return coreTool{}
}

// TestReadFileDetectsAnImageByItsBytesNotItsName is REQ-TOOL-14.6. A PNG
// called .txt is still a PNG, and splitting it into "lines" hands the model
// several kilobytes of mojibake.
func TestReadFileDetectsAnImageByItsBytesNotItsName(t *testing.T) {
	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 16, 8))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "screenshot.txt"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	res := readTool(t, root).Execute(context.Background(),
		json.RawMessage(`{"path":"screenshot.txt"}`))
	if !res.OK {
		t.Fatalf("read failed: %+v", res)
	}
	if len(res.Blocks) != 1 {
		t.Fatalf("want a text note plus one ImageBlock; got %d blocks", len(res.Blocks))
	}
	img2, ok := res.Blocks[0].(core.ImageBlock)
	if !ok {
		t.Fatalf("block is %T, want core.ImageBlock", res.Blocks[0])
	}
	if img2.MimeType != "image/png" {
		t.Fatalf("mime %q", img2.MimeType)
	}
	note, _ := res.Data["note"].(string)
	if !strings.Contains(note, "16") || !strings.Contains(note, "8") {
		t.Fatalf("the note must say what was read; got %q", note)
	}
	if _, isText := res.Data["content"]; isText {
		t.Fatal("an image must not also be returned as text lines")
	}
}

// TestReadFileRefusesAnAnimatedPNGAtTheTool. Forwarding it lands the failure on
// the NEXT provider request — by which time the image is in history and every
// later request fails the same way.
func TestReadFileRefusesAnAnimatedPNGAtTheTool(t *testing.T) {
	root := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	animated := insertPNGChunk(buf.Bytes(), "acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0})
	if err := os.WriteFile(filepath.Join(root, "anim.png"), animated, 0o644); err != nil {
		t.Fatal(err)
	}

	res := readTool(t, root).Execute(context.Background(), json.RawMessage(`{"path":"anim.png"}`))
	if res.OK || res.Error != "unsupported_image" {
		t.Fatalf("want unsupported_image, got %+v", res)
	}
	if !strings.Contains(res.Detail, "APNG") && !strings.Contains(res.Detail, "animated") {
		t.Fatalf("the refusal must name the problem; got %q", res.Detail)
	}
}

// TestReadFileStillReadsText. The image path must not capture ordinary files.
func TestReadFileStillReadsText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := readTool(t, root).Execute(context.Background(), json.RawMessage(`{"path":"a.go"}`))
	if !res.OK || len(res.Blocks) != 0 {
		t.Fatalf("a text file must return no image blocks; got %+v", res)
	}
}

// insertPNGChunk adds a chunk right after IHDR.
func insertPNGChunk(src []byte, typ string, payload []byte) []byte {
	const sigLen = 8
	ihdrLen := int(uint32(src[sigLen])<<24 | uint32(src[sigLen+1])<<16 |
		uint32(src[sigLen+2])<<8 | uint32(src[sigLen+3]))
	at := sigLen + 8 + ihdrLen + 4

	chunk := make([]byte, 0, 12+len(payload))
	n := uint32(len(payload))
	chunk = append(chunk, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	chunk = append(chunk, typ...)
	chunk = append(chunk, payload...)
	c := crc32.NewIEEE()
	_, _ = c.Write([]byte(typ))
	_, _ = c.Write(payload)
	crc := c.Sum32()
	chunk = append(chunk, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))

	out := make([]byte, 0, len(src)+len(chunk))
	out = append(out, src[:at]...)
	out = append(out, chunk...)
	out = append(out, src[at:]...)
	return out
}

// ---- review fixes

// TestSearchReadsOnlyItsOwnIgnoreSources is B3. Search reads the global
// excludes, .git/info/exclude and .gitignore files from the search ROOT down
// (REQ-TOOL-05.2): not a parent directory's .gitignore, and not .ignore files.
func TestSearchReadsOnlyItsOwnIgnoreSources(t *testing.T) {
	top := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(top, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "*.log\n")  // a PARENT of the search root
	write("sub/.ignore", "*.txt\n") // an .ignore file, which is rg's, not git's
	write("sub/a.log", "needle\n")
	write("sub/b.txt", "needle\n")
	write("sub/c.md", "needle\n")
	root := filepath.Join(top, "sub")
	res, err := runNative(t, root, tools.SearchParams{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names(res), " "); got != "a.log:1 b.txt:1 c.md:1" {
		t.Fatalf("got %q; a parent .gitignore and an .ignore file are not ignore sources", got)
	}
}

// TestSearchSemanticsOutsideGo pins five rules that never bite on a Go tree
// (issue #89): Unicode classes, CRLF line ends, a NUL past the binary sniff,
// smart-case over escapes, and BOM-marked files.
func TestSearchSemanticsOutsideGo(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, body []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("u.py", []byte("def café():\n    return 1\nx = ٣\n"))
	write("crlf.cs", []byte("class Foo\r\n{\r\n}\r\n"))
	write("e.txt", []byte("Error: boom\n"))
	// A NUL inside ripgrep's first buffer makes the file binary...
	write("early.dat", append(append(bytes.Repeat([]byte("x"), 13000), "\nlate needle\n"...), 0, '\n'))
	// ...and one far past it ends the search there, keeping what came before.
	far := append([]byte("far needle\n"), bytes.Repeat([]byte("yyyyyyyyyyyyyyy\n"), 20000)...)
	write("far.dat", append(far, "\x00\nfar needle again\n"...))
	// UTF-16LE with a BOM, as Windows PowerShell writes it, and a UTF-8 BOM.
	utf16 := []byte{0xFF, 0xFE}
	for _, r := range "needle here\nsecond\n" {
		utf16 = append(utf16, byte(r), 0)
	}
	write("w.ps1", utf16)
	write("bom.txt", []byte("\xEF\xBB\xBFfirst line\nsecond\n"))

	yes := true
	for _, c := range []struct {
		name string
		q    tools.SearchParams
		want []string
	}{
		{"\\w is ASCII", tools.SearchParams{Pattern: `def \w+\(`}, []string{}},
		{"\\d is ASCII", tools.SearchParams{Pattern: `\d`, FileGlob: "u.py"}, []string{"u.py:2"}},
		{"\\b is ASCII", tools.SearchParams{Pattern: `\bcafé\b`}, []string{}},
		{"$ before CRLF", tools.SearchParams{Pattern: `Foo$`}, []string{"crlf.cs:1"}},
		{"^...$ on a CRLF line", tools.SearchParams{Pattern: `^\{$`}, []string{"crlf.cs:2"}},
		{"a NUL in the first buffer is binary", tools.SearchParams{Pattern: `late needle`}, []string{}},
		{"a late NUL stops the search", tools.SearchParams{Pattern: `far needle`}, []string{"far.dat:1"}},
		{"smart-case ignores escapes", tools.SearchParams{Pattern: `error\S`}, []string{"e.txt:1"}},
		{"an uppercase literal is still sensitive", tools.SearchParams{Pattern: `ERROR\S`}, []string{}},
		{"explicit sensitivity wins", tools.SearchParams{Pattern: `error\S`, CaseSensitive: &yes}, []string{}},
		{"UTF-16 with a BOM is transcoded", tools.SearchParams{Pattern: `^needle here$`}, []string{"w.ps1:1"}},
		{"a UTF-8 BOM is not part of line 1", tools.SearchParams{Pattern: `^first`}, []string{"bom.txt:1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := runNative(t, root, c.q)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if got := names(res); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%q matched %v, want %v", c.q.Pattern, got, c.want)
			}
		})
	}
}

// TestReadFileSaysWhenAFileIsNotUTF8 (issue #89): a Latin-1 file reached the
// model with U+FFFD for each invalid byte under `encoding: utf-8`, and an
// edit built from what the model saw then failed not_found with no reason.
// The envelope says the file is not UTF-8, and the text says what that costs.
func TestReadFileSaysWhenAFileIsNotUTF8(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "l1.txt"), []byte("caf\xe9\nplain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("café\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := readTool(t, root)
	res := read.Execute(context.Background(), json.RawMessage(`{"path":"l1.txt"}`))
	if !res.OK {
		t.Fatalf("read failed: %s %s", res.Error, res.Detail)
	}
	data := res.Data
	if data["encoding"] == "utf-8" {
		t.Fatalf("encoding = %v for a file that is not valid UTF-8", data["encoding"])
	}
	if !strings.Contains(res.Text, "not valid UTF-8") || !strings.Contains(res.Text, "edit_file") {
		t.Fatalf("the text does not say what invalid UTF-8 costs:\n%s", res.Text)
	}

	res = read.Execute(context.Background(), json.RawMessage(`{"path":"ok.txt"}`))
	data = res.Data
	if data["encoding"] != "utf-8" || strings.Contains(res.Text, "not valid UTF-8") {
		t.Fatalf("a UTF-8 file is reported as %v:\n%s", data["encoding"], res.Text)
	}
}
