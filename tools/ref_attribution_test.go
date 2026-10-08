package tools

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/outline"
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
