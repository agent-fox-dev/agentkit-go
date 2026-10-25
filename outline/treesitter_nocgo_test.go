//go:build !cgo

package outline

import (
	"context"
	"testing"
)

// Without cgo only Go is outlined: a Python file is recognised and returned
// as none, and no file has comment or string spans.
func TestWithoutCgoOnlyGoIsOutlined(t *testing.T) {
	if f := outlineSrc(t, "a.py", "def f():\n    pass\n"); !isNone(f) || f.Lang != LangPython {
		t.Fatalf("Outline(a.py) = %+v; want Python, none", f)
	}
	if _, ok := CommentAndStringSpans(context.Background(), "/x/a.py", []byte("# c\n")); ok {
		t.Fatal("CommentAndStringSpans reported spans without cgo")
	}
}
