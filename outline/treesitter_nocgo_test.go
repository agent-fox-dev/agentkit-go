//go:build !cgo

package outline

import (
	"context"
	"testing"
)

// Without cgo only Go is outlined: a Python file is recognised and not
// parsed, unsupported, and has no comment or string spans; a Go file with a
// syntax error has its full verdict and its spans.
func TestWithoutCgoOnlyGoIsOutlined(t *testing.T) {
	if f := outlineSrc(t, "a.py", "def f():\n    pass\n"); !isNone(f, NotParsedUnsupported) || f.Lang != LangPython {
		t.Fatalf("Outline(a.py) = %+v; want Python, unsupported", f)
	}
	if _, ok := CommentAndStringSpans(context.Background(), "/x/a.py", []byte("# c\n")); ok {
		t.Fatal("CommentAndStringSpans reported spans for Python without cgo")
	}
	src := "package a\n\n// F lost its brace.\nfunc F() {\n\tx := 1\n"
	if f := outlineSrc(t, "a.go", src); !f.Parsed || f.Backend != BackendGoAST || len(f.Decls) != 1 || len(f.SyntaxErrors) == 0 {
		t.Fatalf("Outline(a.go) = %+v; want parsed with its declaration and a syntax error", f)
	}
	if spans, ok := CommentAndStringSpans(context.Background(), "/x/a.go", []byte(src)); !ok || len(spans) != 1 {
		t.Fatalf("CommentAndStringSpans(a.go) = %v, %v; want the comment, ok", spans, ok)
	}
}
