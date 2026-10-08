package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/agentfox/agentkit-go/outline"
)

// findEnclosing returns the innermost declaration spanning the 1-based line number.
// If no declaration spans line, it returns a zero outline.Decl with Kind "file".
func findEnclosing(decls []outline.Decl, line int) outline.Decl {
	if line < 1 {
		return outline.Decl{Kind: "file"}
	}
	var best outline.Decl
	bestSpan := -1
	found := false

	for _, d := range decls {
		if d.StartLine <= 0 {
			continue
		}
		endLine := d.EndLine
		if endLine == 0 {
			endLine = d.StartLine
		}
		if d.StartLine <= line && endLine >= line {
			span := endLine - d.StartLine
			// Innermost declaration: smallest line span.
			// When spans are equal, prefer the one starting closer to the site line.
			if !found || span < bestSpan || (span == bestSpan && d.StartLine > best.StartLine) {
				best = d
				bestSpan = span
				found = true
			}
		}
	}

	if !found {
		return outline.Decl{Kind: "file"}
	}
	return best
}

// findEnclosingDecl returns the innermost declaration spanning the 1-based line number
// within the file outline. If fileOutline is nil or no declaration spans line,
// it returns a zero outline.Decl with Kind "file".
func findEnclosingDecl(fileOutline *outline.File, line int) outline.Decl {
	if fileOutline == nil || len(fileOutline.Decls) == 0 {
		return outline.Decl{Kind: "file"}
	}
	return findEnclosing(fileOutline.Decls, line)
}

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

// extractSourceLine extracts the 1-based line from raw source content and returns its sanitized snippet.
func extractSourceLine(content []byte, line int) string {
	if line < 1 || len(content) == 0 {
		return ""
	}
	currentLine := 1
	start := 0
	for i, b := range content {
		if b == '\n' {
			if currentLine == line {
				return sanitizeSnippet(string(content[start:i]))
			}
			currentLine++
			start = i + 1
		}
	}
	if currentLine == line && start < len(content) {
		return sanitizeSnippet(string(content[start:]))
	}
	return ""
}

// attributeSite assigns the enclosing declaration and sanitized source line
// to a reference site using file outline and source content.
func attributeSite(site *ReferenceSite, fileOutline *outline.File, content []byte) {
	if site == nil {
		return
	}
	site.Enclosing = findEnclosingDecl(fileOutline, site.Line)
	if len(content) > 0 && site.Source == "" {
		site.Source = extractSourceLine(content, site.Line)
	}
}
