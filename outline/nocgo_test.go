//go:build !cgo

package outline_test

import (
	"context"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
)

// nonGoBackend is the backend a recognised non-Go file gets in this build.
const nonGoBackend = outline.BackendNone

// Without cgo only Go is outlined: a Python file is read and returned as
// none, and no file has comment or string spans.
func TestWithoutCgoOnlyGoIsOutlined(t *testing.T) {
	f, err := outline.Outline(context.Background(), "/x/a.py", []byte("def f():\n    pass\n"), outline.Options{})
	if err != nil || f.Backend != outline.BackendNone || f.Lang != outline.LangPython || len(f.Decls) != 0 {
		t.Fatalf("Outline(a.py) = %+v, %v; want Python, none, no decls", f, err)
	}
	if _, ok := outline.CommentAndStringSpans(context.Background(), "/x/a.py", []byte("# c\n")); ok {
		t.Fatal("CommentAndStringSpans reported spans without cgo")
	}
}
