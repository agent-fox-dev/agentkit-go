package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/outline"
)

// renderEnclosingLabel renders the signature or '<Kind> <Name>' for a declaration,
// or '<file>' when Kind equals 'file' or is empty.
func renderEnclosingLabel(decl outline.Decl) string {
	if decl.Kind == "file" || decl.Kind == "" {
		return "<file>"
	}
	sig := strings.TrimSpace(decl.Signature)
	if sig != "" {
		return sig
	}
	if decl.Name != "" {
		return fmt.Sprintf("%s %s", decl.Kind, decl.Name)
	}
	return string(decl.Kind)
}

// sanitizeSnippet trims whitespace, caps at 200 bytes, and replaces ASCII control characters (< 0x20) with spaces.
func sanitizeSnippet(rawLine string) string {
	var b strings.Builder
	for _, r := range rawLine {
		if r < 32 || r == 127 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	if len(res) > 200 {
		res = res[:200]
		// Backtrack if truncated inside a multi-byte UTF-8 sequence.
		for len(res) > 0 && !utf8.ValidString(res) {
			res = res[:len(res)-1]
		}
		res = strings.TrimRight(res, " ")
	}
	return res
}
