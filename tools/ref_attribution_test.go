package tools

import (
	"context"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// TS-05-28 (unit): Attribution engine renders signature or '<Kind> <Name>' for declarations and '<file>' for top level
// Verifies: 05-REQ-5.4
func TestAttribution_RenderLabel_TS_05_28(t *testing.T) {
	// Given: a declaration with signature 'func (r *Runner) Run() error', a declaration without signature 'class Controller', and a top-level file declaration
	declWithSig := outline.Decl{
		Kind:      outline.KindMethod,
		Name:      "Run",
		Signature: "func (r *Runner) Run() error",
	}
	declWithoutSig := outline.Decl{
		Kind: outline.KindClass,
		Name: "Controller",
	}
	fileDecl := outline.Decl{
		Kind: "file",
	}

	// When: enclosing declaration labels are rendered
	// Then:
	// - the first renders as 'func (r *Runner) Run() error'
	// - the second renders as 'class Controller'
	// - the top-level declaration renders as '<file>'
	if got := renderEnclosingLabel(declWithSig); got != "func (r *Runner) Run() error" {
		t.Fatalf("renderEnclosingLabel(declWithSig) = %q, want %q", got, "func (r *Runner) Run() error")
	}
	if got := renderEnclosingLabel(declWithoutSig); got != "class Controller" {
		t.Fatalf("renderEnclosingLabel(declWithoutSig) = %q, want %q", got, "class Controller")
	}
	if got := renderEnclosingLabel(fileDecl); got != "<file>" {
		t.Fatalf("renderEnclosingLabel(fileDecl) = %q, want %q", got, "<file>")
	}

	// Also test zero outline.Decl with empty Kind
	zeroDecl := outline.Decl{}
	if got := renderEnclosingLabel(zeroDecl); got != "<file>" {
		t.Fatalf("renderEnclosingLabel(zeroDecl) = %q, want %q", got, "<file>")
	}
}

// TS-05-29 (property): Source line snippet extraction trims whitespace, caps at 200 bytes, and sanitizes control characters
// Verifies: 05-REQ-5.5
func TestAttribution_SanitizeSnippet_TS_05_29(t *testing.T) {
	// Given: arbitrary raw source lines containing whitespace, tabs, control characters (\x00-\x1F), and varying byte lengths
	genRawLines := func() []string {
		var lines []string

		// Edge cases
		lines = append(lines,
			"",
			"   ",
			"\t\t\t\n\r",
			"\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0A\x0B\x0C\x0D\x0E\x0F",
			"simple code line",
			"   leading and trailing spaces   ",
			"\t\tval x = 42;\t\t",
			"let y = \"hello\\x00world\"; // comment with \x07 bell",
			strings.Repeat("a", 200),
			strings.Repeat("b", 201),
			strings.Repeat("c", 300),
			strings.Repeat("   word   ", 50),
			strings.Repeat("\x01\x02test\x03\x04", 40),
			"prefix "+strings.Repeat("x", 190)+" 日本語 "+strings.Repeat("y", 50),
			"prefix "+strings.Repeat("x", 192)+" 🚀 "+strings.Repeat("y", 50),
			"prefix "+strings.Repeat("x", 195)+" Привет "+strings.Repeat("y", 50),
			strings.Repeat(" ", 50)+"middle"+strings.Repeat(" ", 50),
		)

		// Randomized lines
		rng := rand.New(rand.NewSource(12345))
		charPool := []string{
			"a", "B", "1", "_", " ", "\t", "\r", "\n",
			"\x00", "\x01", "\x05", "\x1B", "\x1F", "\x7F",
			"日", "本", "語", "🚀", "Ж", "ø",
		}

		for i := 0; i < 200; i++ {
			length := rng.Intn(400)
			var sb strings.Builder
			for j := 0; j < length; j++ {
				sb.WriteString(charPool[rng.Intn(len(charPool))])
			}
			lines = append(lines, sb.String())
		}

		return lines
	}

	// When: for any reference site, the source line snippet is extracted and sanitized
	// Then:
	// - leading and trailing whitespace is stripped
	// - byte length of the resulting snippet does not exceed 200 bytes
	// - no ASCII control characters (byte < 0x20, except space) exist in the output snippet
	for idx, raw := range genRawLines() {
		snip := sanitizeSnippet(raw)

		if strings.HasPrefix(snip, " ") {
			t.Fatalf("[%d] snip has leading space: %q (from raw %q)", idx, snip, raw)
		}
		if strings.HasSuffix(snip, " ") {
			t.Fatalf("[%d] snip has trailing space: %q (from raw %q)", idx, snip, raw)
		}
		if len([]byte(snip)) > 200 {
			t.Fatalf("[%d] len([]byte(snip)) = %d > 200: %q", idx, len([]byte(snip)), snip)
		}
		if !utf8.ValidString(snip) {
			t.Fatalf("[%d] snip is not valid UTF-8: %q", idx, snip)
		}
		for _, b := range []byte(snip) {
			if !(b >= 0x20 || b >= 0x7F) {
				t.Fatalf("[%d] found control character byte 0x%02X in snippet: %q", idx, b, snip)
			}
		}
	}
}

// TS-05-25 (unit): Attribution engine inspects outline declarations spanning the site line
// Verifies: 05-REQ-5.1
func TestAttribution_SpanningDecl_TS_05_25(t *testing.T) {
	// Given: a file outline containing functions at lines 10-20, 25-35, and 40-50
	decls := []outline.Decl{
		{Kind: outline.KindFunc, Name: "a", StartLine: 10, EndLine: 20},
		{Kind: outline.KindFunc, Name: "b", StartLine: 25, EndLine: 35},
		{Kind: outline.KindFunc, Name: "c", StartLine: 40, EndLine: 50},
	}

	// When: the enclosing declaration of a site at line 30 is looked up
	enc := findEnclosingDecl(decls, 30)

	// Then: the declaration at 25-35 is chosen and the other two are ignored
	if enc.Name != "b" || enc.StartLine != 25 || enc.EndLine != 35 {
		t.Fatalf("findEnclosingDecl(decls, 30) = %+v, want b at 25-35", enc)
	}
	// The span is inclusive at both ends.
	if enc := findEnclosingDecl(decls, 35); enc.Name != "b" {
		t.Fatalf("findEnclosingDecl(decls, 35) = %+v, want b", enc)
	}
	if enc := findEnclosingDecl(decls, 40); enc.Name != "c" {
		t.Fatalf("findEnclosingDecl(decls, 40) = %+v, want c", enc)
	}
}

// TS-05-26 (unit): Attribution engine chooses innermost nested declaration with smallest line span
// Verifies: 05-REQ-5.2
func TestAttribution_Innermost_TS_05_26(t *testing.T) {
	// Given: an outer class Engine at lines 10-60 and an inner method Start at lines 20-30
	decls := []outline.Decl{
		{Kind: outline.KindClass, Name: "Engine", StartLine: 10, EndLine: 60},
		{Kind: outline.KindMethod, Name: "Start", Container: "Engine", StartLine: 20, EndLine: 30},
	}

	// When: the enclosing declaration of a site at line 25 is resolved
	enc := findEnclosingDecl(decls, 25)

	// Then: the innermost declaration Start (span 10) wins over Engine (span 50)
	if enc.Name != "Start" {
		t.Fatalf("findEnclosingDecl(decls, 25) = %+v, want Start", enc)
	}
	// Outside Start but inside Engine, Engine encloses.
	if enc := findEnclosingDecl(decls, 45); enc.Name != "Engine" {
		t.Fatalf("findEnclosingDecl(decls, 45) = %+v, want Engine", enc)
	}
}

// TS-05-27 (unit): Attribution engine assigns zero declaration with Kind 'file' for top-level reference sites
// Verifies: 05-REQ-5.3
func TestAttribution_TopLevel_TS_05_27(t *testing.T) {
	// Given: a declaration at lines 15-25 and a site at line 5
	decls := []outline.Decl{{Kind: outline.KindFunc, Name: "Foo", StartLine: 15, EndLine: 25}}

	// When: the enclosing declaration of line 5 is resolved
	enc := findEnclosingDecl(decls, 5)

	// Then: Enclosing is a zero outline.Decl with Kind 'file'
	if enc != (outline.Decl{Kind: "file"}) {
		t.Fatalf("findEnclosingDecl(decls, 5) = %+v, want zero Decl with Kind file", enc)
	}
	if enc := findEnclosingDecl(nil, 1); enc != (outline.Decl{Kind: "file"}) {
		t.Fatalf("findEnclosingDecl(nil, 1) = %+v, want zero Decl with Kind file", enc)
	}
}

// TS-05-25, TS-05-26 (wiring): reference sites returned by Workspace.References
// carry the enclosing declaration from the file's outline, with its signature
// and line range, for Go and (where an outline backend exists) Python.
// Verifies: 05-REQ-5.1, 05-REQ-5.2
func TestAttribution_WiredIntoReferences_TS_05_25(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.22\n")
	writeFile(t, root, "lib.go", "package m\n\nfunc Target() int { return 1 }\n\nfunc Caller() int {\n\treturn Target()\n}\n")
	writeFile(t, root, "calc.py", "def calculate_tax(x):\n    return x\n\n\nclass Shop:\n    def checkout(self, x):\n        return calculate_tax(x)\n")
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	goRes, err := ws.References(context.Background(), outline.Decl{Kind: outline.KindFunc, Name: "Target", StartLine: 3}, ReferenceOptions{IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(goRes.Sites) != 1 {
		t.Fatalf("Go sites = %+v, want one", goRes.Sites)
	}
	enc := goRes.Sites[0].Enclosing
	if enc.Name != "Caller" || enc.StartLine != 5 || enc.EndLine != 7 || enc.Signature == "" {
		t.Fatalf("Go site Enclosing = %+v, want the outline's Caller decl at 5-7 with a signature", enc)
	}

	pyOutline, err := outline.Outline(context.Background(), filepath.Join(root, "calc.py"), nil, outline.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if pyOutline.Backend == outline.BackendNone {
		t.Log("no Python outline backend in this build; Python attribution not checked")
		return
	}
	pyRes, err := ws.References(context.Background(), outline.Decl{Kind: outline.KindFunc, Name: "calculate_tax", StartLine: 1}, ReferenceOptions{IncludeTests: true})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range pyRes.Sites {
		if s.Line == 7 {
			found = true
			if s.Enclosing.Name != "checkout" || s.Enclosing.StartLine != 6 {
				t.Fatalf("Python site at line 7 Enclosing = %+v, want checkout at line 6", s.Enclosing)
			}
		}
	}
	if !found {
		t.Fatalf("no Python site at line 7: %+v", pyRes.Sites)
	}
}

// TS-05-29 (property): non-Go reference sites carry the same sanitized
// snippet as Go sites: no control characters, tabs included.
// Verifies: 05-REQ-5.5
func TestAttribution_ScannerSnippet_TS_05_29(t *testing.T) {
	content := []byte("\x01\tcalc(1)\t# tab\x7f\n")
	sites := scanContentForMatches("x.py", content, outline.Decl{Name: "calc"})
	if len(sites) != 1 {
		t.Fatalf("sites = %+v, want one", sites)
	}
	if got, want := sites[0].Source, sanitizeSnippet("\x01\tcalc(1)\t# tab\x7f"); got != want {
		t.Fatalf("Source = %q, want %q", got, want)
	}
	for _, r := range sites[0].Source {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("Source %q has control character %q", sites[0].Source, r)
		}
	}
}
