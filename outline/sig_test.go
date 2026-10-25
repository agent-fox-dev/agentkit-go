package outline

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A signature is one line without control characters or runs of
// whitespace, at most 200 bytes, cut on a rune boundary.
func TestSanitiseSignatureMakesOneShortLine(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"func foo(a int, b string) error", "func foo(a int, b string) error"},
		{"func foo(\n\ta int,\n\tb string,\n) error", "func foo( a int, b string, ) error"},
		{"func foo(a int,\t\t  b string)", "func foo(a int, b string)"},
		{"func foo() \x00\x01\x7f\u0085 error", "func foo() error"},
		{"func f()\r\n", "func f()"},
		{"   leading   and   trailing   ", "leading and trailing"},
		{" nbsp em", "nbsp em"},
		{"", ""},
		{strings.Repeat("a", 300), strings.Repeat("a", 200)},
		{strings.Repeat("a", 200), strings.Repeat("a", 200)},
		{strings.Repeat("a", 198) + "世tail", strings.Repeat("a", 198)},
		{strings.Repeat("a", 199) + "étail", strings.Repeat("a", 199)},
		{strings.Repeat("世", 100), strings.Repeat("世", 66)},
	} {
		got := sanitiseSignature(c.in)
		if got != c.want || !utf8.ValidString(got) {
			t.Errorf("sanitiseSignature(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
