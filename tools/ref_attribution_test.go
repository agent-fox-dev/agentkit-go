package tools

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/outline"
)

// TS-05-25 (unit): Attribution engine inspects outline declarations spanning the site line
// Verifies: 05-REQ-5.1
func TestAttribution_InspectLineSpan_TS_05_25(t *testing.T) {
	// Given: a file outline containing functions at lines 10-20, 25-35, and 40-50
	decls := []outline.Decl{
		{Kind: outline.KindFunc, Name: "F1", StartLine: 10, EndLine: 20},
		{Kind: outline.KindFunc, Name: "F2", StartLine: 25, EndLine: 35},
		{Kind: outline.KindFunc, Name: "F3", StartLine: 40, EndLine: 50},
	}

	// When: attribution engine looks up enclosing declaration for reference site at line 30
	enc := findEnclosing(decls, 30)

	// Then:
	// - declarations at 10-20 and 40-50 are ignored
	// - declaration at 25-35 spanning line 30 is identified as candidate enclosing declaration
	if enc.StartLine != 25 || enc.EndLine != 35 {
		t.Fatalf("expected decl at 25-35, got StartLine=%d EndLine=%d Name=%q", enc.StartLine, enc.EndLine, enc.Name)
	}
	if enc.Name != "F2" {
		t.Fatalf("expected F2, got %q", enc.Name)
	}

	// Also verify findEnclosingDecl with outline.File
	file := &outline.File{
		Path:  "example.go",
		Decls: decls,
	}
	encFile := findEnclosingDecl(file, 30)
	if encFile.StartLine != 25 || encFile.EndLine != 35 || encFile.Name != "F2" {
		t.Fatalf("findEnclosingDecl failed: got StartLine=%d EndLine=%d Name=%q", encFile.StartLine, encFile.EndLine, encFile.Name)
	}
}

// TS-05-26 (unit): Attribution engine chooses innermost nested declaration with smallest line span
// Verifies: 05-REQ-5.2
func TestAttribution_InnermostNested_TS_05_26(t *testing.T) {
	// Given: a file outline with outer class 'Engine' at lines 10-60 and inner method 'Start' at lines 20-30
	decls := []outline.Decl{
		{Kind: outline.KindClass, Name: "Engine", StartLine: 10, EndLine: 60},
		{Kind: outline.KindMethod, Name: "Start", StartLine: 20, EndLine: 30},
	}

	// When: attribution engine resolves enclosing declaration for a reference site at line 25
	enc := findEnclosing(decls, 25)

	// Then:
	// - both Engine (span 50) and Start (span 10) contain line 25
	// - innermost declaration Start with smallest span 10 is assigned to ReferenceSite.Enclosing
	if enc.Name != "Start" {
		t.Fatalf("expected innermost decl 'Start', got %q", enc.Name)
	}
	if enc.Kind != outline.KindMethod {
		t.Fatalf("expected KindMethod, got %v", enc.Kind)
	}

	// Also verify findEnclosingDecl
	file := &outline.File{
		Path:  "engine.py",
		Decls: decls,
	}
	encFile := findEnclosingDecl(file, 25)
	if encFile.Name != "Start" {
		t.Fatalf("findEnclosingDecl: expected innermost decl 'Start', got %q", encFile.Name)
	}
}

// TS-05-27 (unit): Attribution engine assigns zero declaration with Kind 'file' for top-level reference sites
// Verifies: 05-REQ-5.3
func TestAttribution_TopLevelFile_TS_05_27(t *testing.T) {
	// Given: a file outline with a declaration starting at line 15, and a reference site at line 5 (top-level import or var)
	decls := []outline.Decl{
		{Kind: outline.KindFunc, Name: "Foo", StartLine: 15, EndLine: 25},
	}

	// When: attribution engine resolves enclosing declaration for line 5
	enc := findEnclosing(decls, 5)

	// Then:
	// - ReferenceSite.Enclosing is a zero outline.Decl with Kind set to 'file'
	if enc.Kind != "file" {
		t.Fatalf("expected Kind 'file', got %q", enc.Kind)
	}
	if enc.Name != "" {
		t.Fatalf("expected empty Name for top-level file decl, got %q", enc.Name)
	}

	// Also verify when line is after all declarations
	encAfter := findEnclosing(decls, 30)
	if encAfter.Kind != "file" || encAfter.Name != "" {
		t.Fatalf("expected Kind 'file' for line after decls, got %v", encAfter)
	}

	// Also verify findEnclosingDecl with nil file outline
	encNil := findEnclosingDecl(nil, 5)
	if encNil.Kind != "file" || encNil.Name != "" {
		t.Fatalf("expected Kind 'file' for nil outline, got %v", encNil)
	}

	// Also verify findEnclosingDecl with empty decls
	encEmpty := findEnclosingDecl(&outline.File{}, 5)
	if encEmpty.Kind != "file" || encEmpty.Name != "" {
		t.Fatalf("expected Kind 'file' for empty outline, got %v", encEmpty)
	}
}

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
			"prefix " + strings.Repeat("x", 190) + " 日本語 " + strings.Repeat("y", 50),
			"prefix " + strings.Repeat("x", 192) + " 🚀 " + strings.Repeat("y", 50),
			"prefix " + strings.Repeat("x", 195) + " Привет " + strings.Repeat("y", 50),
			strings.Repeat(" ", 50) + "middle" + strings.Repeat(" ", 50),
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
