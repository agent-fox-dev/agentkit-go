//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// gitignoreFixture builds an index over 100 filler Go files plus b.go, so a
// handful of dirty paths stay under the 5% rebuild threshold, runs the first
// search to build it, and backdates every indexed file past the 2 s racy
// window so that a revalidation walk does not mark unchanged files dirty.
func gitignoreFixture(t *testing.T) (root string, idx *Index, search func(q string) core.ToolResult) {
	t.Helper()

	root = t.TempDir()
	for i := 0; i < 100; i++ {
		mkFile(t, root, fmt.Sprintf("f%03d.go", i), fmt.Sprintf("package pkg\nfunc F%03d() {}\n", i))
	}
	mkFile(t, root, "b.go", "package pkg\n// bToken\nfunc B() {}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	idx, err = newIndex(ws, Options{
		TempDir:      t.TempDir(),
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })

	tool := idx.Tools()[0]
	search = func(q string) core.ToolResult {
		in, _ := json.Marshal(map[string]any{"query": q})
		return tool.Execute(context.Background(), in)
	}

	if r := search("bToken"); !r.OK {
		t.Fatalf("first search: %s: %s", r.Error, r.Detail)
	}
	idx.mu.Lock()
	for rel, fi := range idx.fileInfos {
		fi.indexedAt = time.Now().Add(-time.Hour)
		idx.fileInfos[rel] = fi
	}
	idx.mu.Unlock()
	return root, idx, search
}

func writeGitignore(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 03-REQ-6.3 and 03-REQ-6.8: every edit of a .gitignore is examined by a
// revalidation walk, not only the first, so a file the second edit newly
// ignores leaves the results.
func TestEverySecondGitignoreEditRevalidates(t *testing.T) {
	root, idx, search := gitignoreFixture(t)

	// First edit: ignores something unrelated; b.go stays visible.
	writeGitignore(t, root, "nothing.txt\n")
	idx.Invalidate(".gitignore")
	before := idx.RevalCount()
	r := search("bToken")
	if !r.OK {
		t.Fatalf("search after first edit: %s: %s", r.Error, r.Detail)
	}
	if got := idx.RevalCount(); got != before+1 {
		t.Errorf("first .gitignore edit: revalidations %d -> %d, want +1", before, got)
	}
	if got := getResultFilePaths(r); !slices.Equal(got, []string{"b.go"}) {
		t.Errorf("after the first edit results = %v, want [b.go]", got)
	}

	// Second edit: now ignores b.go.
	writeGitignore(t, root, "b.go\n")
	idx.Invalidate(".gitignore")
	before = idx.RevalCount()
	r = search("bToken")
	if !r.OK {
		t.Fatalf("search after second edit: %s: %s", r.Error, r.Detail)
	}
	if got := idx.RevalCount(); got != before+1 {
		t.Errorf("second .gitignore edit: revalidations %d -> %d, want +1", before, got)
	}
	if got := getResultFilePaths(r); len(got) != 0 {
		t.Errorf("after the second edit results = %v, want none: b.go is now ignored", got)
	}

	// Third edit: b.go is visible again.
	writeGitignore(t, root, "nothing.txt\n")
	idx.Invalidate(".gitignore")
	r = search("bToken")
	if !r.OK {
		t.Fatalf("search after third edit: %s: %s", r.Error, r.Detail)
	}
	if got := getResultFilePaths(r); !slices.Equal(got, []string{"b.go"}) {
		t.Errorf("after the third edit results = %v, want [b.go]", got)
	}
}

// A repeated search with nothing new marked does not walk again: the
// revalidation is once per mark, not once per query.
func TestUnchangedGitignoreIsNotRevalidatedAgain(t *testing.T) {
	root, idx, search := gitignoreFixture(t)

	writeGitignore(t, root, "nothing.txt\n")
	idx.Invalidate(".gitignore")
	if r := search("bToken"); !r.OK {
		t.Fatalf("search: %s: %s", r.Error, r.Detail)
	}
	after := idx.RevalCount()
	for i := 0; i < 3; i++ {
		if r := search("bToken"); !r.OK {
			t.Fatalf("repeat search: %s: %s", r.Error, r.Detail)
		}
	}
	if got := idx.RevalCount(); got != after {
		t.Errorf("revalidations %d -> %d across searches with no new mark, want no change", after, got)
	}
}

// A file created after the build and seen by one revalidation walk is later
// hidden by a .gitignore edit: it must leave the results too, although it was
// never in the built index.
func TestNewFileIgnoredByALaterGitignoreEditLeavesTheResults(t *testing.T) {
	root, idx, search := gitignoreFixture(t)

	mkFile(t, root, "n.go", "package pkg\n// nToken\nfunc N() {}\n")
	idx.Invalidate("n.go")
	r := search("nToken")
	if !r.OK {
		t.Fatalf("search for the new file: %s: %s", r.Error, r.Detail)
	}
	if got := getResultFilePaths(r); !slices.Equal(got, []string{"n.go"}) {
		t.Fatalf("new file results = %v, want [n.go]", got)
	}

	writeGitignore(t, root, "n.go\n")
	idx.Invalidate(".gitignore")
	r = search("nToken")
	if !r.OK {
		t.Fatalf("search after ignoring the new file: %s: %s", r.Error, r.Detail)
	}
	if got := getResultFilePaths(r); len(got) != 0 {
		t.Errorf("results = %v, want none: n.go is now ignored", got)
	}
}
