package tools

import "testing"

// TestRipgrepPatternKeepsGoMeaning pins what ripgrep is handed: Go's ASCII
// classes as ranges, ASCII word boundaries, and line anchors that mean the
// same per line.
func TestRipgrepPatternKeepsGoMeaning(t *testing.T) {
	for in, want := range map[string]string{
		`def \w+\(`:  `def [0-9A-Z_a-z]+\(`,
		`\d`:         `[0-9]`,
		`\bcafé\b`:   `(?-u:\b)café(?-u:\b)`,
		`\Bx`:        `(?-u:\B)x`,
		`^\{$`:       `(?m:^\{$)`,
		`a\\b`:       `a\\b`,
		`(unclosed`:  `(unclosed`,
		`[\w-]+\.go`: `[\-0-9A-Z_a-z]+\.go`,
	} {
		if got := ripgrepPattern(in); got != want {
			t.Errorf("ripgrepPattern(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSmartCaseCountsLiteralsOnly: an escape's letter, a flag group and a
// group name are syntax, not text.
func TestSmartCaseCountsLiteralsOnly(t *testing.T) {
	for pat, want := range map[string]bool{
		"error":       false,
		"Error":       true,
		`error\S`:     false,
		`\W\D\B\A\z`:  false,
		`\p{Lu}x`:     false,
		`\pLx`:        false,
		`(?U)a+`:      false,
		`(?P<Name>x)`: false,
		`(?<Name>x)`:  false,
		`(?<=A)x`:     true,
		`\QFoo\E`:     true,
		`[A-Z]`:       true,
		`\x{41}`:      false,
		`café`:        false,
		`CAFÉ`:        true,
		`\\N`:         true, // an escaped backslash, then a literal N
	} {
		if got := hasUppercaseLiteral(pat); got != want {
			t.Errorf("hasUppercaseLiteral(%q) = %v, want %v", pat, got, want)
		}
	}
}
