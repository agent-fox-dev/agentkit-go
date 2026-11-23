package outline

import (
	"bytes"
	"context"
	"go/scanner"
	"go/token"
)

// CommentAndStringSpans returns the byte ranges [start, end) of the comments
// and string literals in src, in order and not overlapping.
//
// For a Go file it answers in every build, from Go's tokens: every line and
// block comment, interpreted and raw string, and rune literal. ok is then
// true whether or not the file has syntax errors, and ctx is not consulted.
// An unterminated raw string or block comment runs to the end of src; an
// unterminated interpreted string or rune literal ends with its line. An
// empty or nil src has no spans.
//
// For any other language the spans come from the language's tree-sitter
// grammar, in a build with cgo; ok is false when there is no grammar, in a
// build without cgo, or when ctx ended.
func CommentAndStringSpans(ctx context.Context, path string, src []byte) (spans [][2]int, ok bool) {
	if LangFor(path, src) == LangGo {
		return goCommentAndStringSpans(src), true
	}
	return commentAndStringSpans(ctx, path, src)
}

// goCommentAndStringSpans scans src as Go and returns the span of every
// COMMENT, STRING and CHAR token. The scanner removes carriage returns from
// the literal of a raw string and a comment, so their ends are found in src
// rather than taken from the literal's length.
func goCommentAndStringSpans(src []byte) [][2]int {
	var spans [][2]int
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) {}, scanner.ScanComments)
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return spans
		}
		if tok != token.COMMENT && tok != token.STRING && tok != token.CHAR {
			continue
		}
		start := file.Offset(pos)
		end := start + len(lit)
		switch rest := src[start:]; {
		case bytes.HasPrefix(rest, []byte("//")):
			end = start + lineEnd(rest)
		case bytes.HasPrefix(rest, []byte("/*")):
			end = start + closedAt(rest, "*/")
		case bytes.HasPrefix(rest, []byte("`")):
			end = start + closedAt(rest, "`")
		}
		spans = append(spans, [2]int{start, end})
	}
}

// lineEnd is the length of a line comment at the start of b: up to its
// newline, without a carriage return before it, or all of b.
func lineEnd(b []byte) int {
	n := len(b)
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		n = i
	}
	if n > 0 && b[n-1] == '\r' {
		n--
	}
	return n
}

// closedAt is the length of a token at the start of b that opens with the
// first byte of b and closes with closer: through closer, or all of b when
// it is not closed.
func closedAt(b []byte, closer string) int {
	open := 1
	if closer == "*/" {
		open = 2
	}
	if i := bytes.Index(b[open:], []byte(closer)); i >= 0 {
		return open + i + len(closer)
	}
	return len(b)
}
