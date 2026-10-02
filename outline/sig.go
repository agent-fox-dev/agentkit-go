package outline

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxSignatureBytes is the hard limit on a sanitised signature.
const maxSignatureBytes = 200

// sanitiseSignature removes control characters, collapses whitespace runs
// (including newlines) to one space, trims, and cuts at maxSignatureBytes on a
// rune boundary so the result is always valid UTF-8.
func sanitiseSignature(line string) string {
	var b strings.Builder
	b.Grow(len(line))

	lastWasSpace := false
	for _, r := range line {
		// Drop control characters (anything below space except tab/newline,
		// which are collapsed to space, plus DEL and C1 controls).
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			// Treat as whitespace for collapsing purposes.
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
			continue
		}
		if unicode.IsSpace(r) {
			if !lastWasSpace {
				b.WriteByte(' ')
				lastWasSpace = true
			}
			continue
		}
		b.WriteRune(r)
		lastWasSpace = false
	}

	s := strings.TrimSpace(b.String())

	// Cut at maxSignatureBytes on a rune boundary.
	if len(s) <= maxSignatureBytes {
		return s
	}
	return truncateOnRuneBoundary(s, maxSignatureBytes)
}

// truncateOnRuneBoundary returns the longest prefix of s that is at most
// maxBytes bytes and does not split a multi-byte rune.
func truncateOnRuneBoundary(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk forward through complete runes, stopping when the next rune
	// would push us past maxBytes.
	pos := 0
	for pos < len(s) {
		_, size := utf8.DecodeRuneInString(s[pos:])
		if pos+size > maxBytes {
			break
		}
		pos += size
	}
	return s[:pos]
}
