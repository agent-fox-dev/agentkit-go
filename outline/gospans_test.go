package outline

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Go's comments and string and rune literals are spans in every build,
// taken from its tokens, with or without syntax errors, whatever the state
// of the context.
func TestGoCommentAndStringSpans(t *testing.T) {
	const lits = "package a\n// c\nvar s = \"x\" + `r\nq` + string('c') /* b */\n"
	crlf := strings.ReplaceAll(lits, "\n", "\r\n")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		name string
		src  []byte
		want [][2]int
	}{
		{"every kind of literal", []byte(lits), [][2]int{{10, 14}, {23, 26}, {29, 34}, {44, 47}, {49, 56}}},
		// A raw string keeps its carriage return; a line comment ends before its own.
		{"CRLF line endings", []byte(crlf), [][2]int{{11, 15}, {25, 28}, {31, 37}, {47, 50}, {52, 59}}},
		// An interpreted string ends with its line, a block comment with the file.
		{"unterminated literals", []byte("package a\nvar u = \"open\n/* never"), [][2]int{{18, 23}, {24, 32}}},
		{"unterminated raw string", []byte("package a\nvar r = `x\ny"), [][2]int{{18, 22}}},
		{"syntax errors and no literal", []byte("package a\nfunc broken( {\n"), nil},
		{"empty", []byte{}, nil},
		{"nil", nil, nil},
	} {
		for _, ctx := range []context.Context{context.Background(), cancelled} {
			spans, ok := CommentAndStringSpans(ctx, "/x/a.go", c.src)
			if !ok || len(spans) != len(c.want) || (len(c.want) > 0 && !slices.Equal(spans, c.want)) {
				t.Errorf("%s (ctx err %v): %v, %v; want %v, true", c.name, ctx.Err(), spans, ok, c.want)
			}
		}
	}
	// Each span is the whole token, delimiters included, and code is in none.
	src := []byte(lits)
	spans, _ := CommentAndStringSpans(context.Background(), "/x/a.go", src)
	for _, s := range spans {
		tok := string(src[s[0]:s[1]])
		if !slices.ContainsFunc([]string{"//", "/**/", `""`, "``", "''"}, func(d string) bool {
			return strings.HasPrefix(tok, d[:len(d)/2]) && strings.HasSuffix(tok, d[len(d)/2:]) || d == "//" && strings.HasPrefix(tok, d)
		}) {
			t.Errorf("span %v is %q, not a whole token", s, tok)
		}
	}
	for _, code := range []string{"var s", "string("} {
		off := strings.Index(lits, code)
		if slices.ContainsFunc(spans, func(s [2]int) bool { return s[0] <= off && off < s[1] }) {
			t.Errorf("code %q at %d is in a span %v", code, off, spans)
		}
	}
}
