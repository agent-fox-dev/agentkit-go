//go:build !windows

package codesearch

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// openWorkspace opens root without the machine's global excludes.
func openWorkspace(t *testing.T, root string) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// indexFor is an index on ws, its shards in a temporary directory unless o
// names one, closed at cleanup.
func indexFor(t *testing.T, ws *workspace.Workspace, o Options) *Index {
	t.Helper()
	if o.TempDir == "" {
		o.TempDir = t.TempDir()
	}
	idx, err := newIndex(ws, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// newFixture is an unbuilt index over a workspace holding files.
func newFixture(t *testing.T, files map[string]string, o Options) *Index {
	t.Helper()
	return indexFor(t, openWorkspace(t, testutil.Tree(t, files)), o)
}

// needCaseSensitiveFS skips a test whose fixture relies on paths that differ
// only in case, which a case-insensitive filesystem (macOS's default APFS)
// collapses into one file.
func needCaseSensitiveFS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "case"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "CASE")); err == nil {
		t.Skip("the filesystem is case-insensitive")
	}
}

// numbered is n files named by format from their number, each holding body
// with every verb in it formatting the number too.
func numbered(n int, format, body string) map[string]string {
	files := make(map[string]string, n)
	for i := range n {
		args := make([]any, strings.Count(body, "%"))
		for j := range args {
			args[j] = i
		}
		files[fmt.Sprintf(format, i)] = fmt.Sprintf(body, args...)
	}
	return files
}

// with is files plus extra.
func with(files map[string]string, extra map[string]string) map[string]string {
	for k, v := range extra {
		files[k] = v
	}
	return files
}

// call runs code_search with args under ctx.
func call(ctx context.Context, idx *Index, args map[string]any) core.ToolResult {
	in, _ := json.Marshal(args)
	return idx.Tools()[0].Execute(ctx, in)
}

// find runs code_search for query, with further arguments given as key,
// value pairs, and fails the test unless it succeeds.
func find(t *testing.T, idx *Index, query string, kv ...any) core.ToolResult {
	t.Helper()
	args := map[string]any{"query": query}
	for i := 0; i+1 < len(kv); i += 2 {
		args[kv[i].(string)] = kv[i+1]
	}
	r := call(context.Background(), idx, args)
	if !r.OK {
		t.Fatalf("code_search %v: %s: %s", args, r.Error, r.Detail)
	}
	return r
}

// paths is the files of a code_search result, in its order.
func paths(r core.ToolResult) []string {
	var out []string
	files, _ := r.Data["files"].([]any)
	for _, f := range files {
		out = append(out, f.(map[string]any)["path"].(string))
	}
	return out
}

// write writes content to rel in the index's workspace.
func write(t *testing.T, idx *Index, rel, content string) {
	t.Helper()
	testutil.WriteTree(t, idx.ws.Root(), map[string]string{rel: content})
}

// remove deletes rel from the index's workspace.
func remove(t *testing.T, idx *Index, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(idx.ws.Root(), filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// backdate makes every record of the build look an hour old, past the racy
// window, so that a revalidation walk trusts the (size, mtime) of an
// unchanged file.
func backdate(idx *Index) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for rel, fi := range idx.fileInfos {
		fi.indexedAt = time.Now().Add(-time.Hour)
		idx.fileInfos[rel] = fi
	}
}

// blockOutline parks every build of idx in its first outline batch until
// release is called. started receives once per build that reaches it. The
// release runs at cleanup too, before the index is closed.
func blockOutline(t *testing.T, idx *Index) (started <-chan struct{}, release func()) {
	t.Helper()
	ch := make(chan struct{}, 4)
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	idx.testOutlineHook = func(string, int) {
		select {
		case ch <- struct{}{}:
		default:
		}
		<-gate
	}
	t.Cleanup(release)
	return ch, release
}

// async runs code_search for query under ctx in a goroutine.
func async(ctx context.Context, idx *Index, query string) <-chan core.ToolResult {
	out := make(chan core.ToolResult, 1)
	go func() { out <- call(ctx, idx, map[string]any{"query": query}) }()
	return out
}

// recv is the next value of ch, failing the test after five seconds.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	var zero T
	return zero
}

// stays reports whether ch delivers nothing for a short while.
func stays[T any](ch <-chan T) bool {
	return !testutil.Within(50*time.Millisecond, func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	})
}

// gone reports whether path no longer exists.
func gone(path string) bool {
	_, err := os.Stat(path)
	return os.IsNotExist(err)
}

// treeSitter reports whether this build outlines non-Go files with
// tree-sitter (it needs cgo); otherwise they get no symbols.
func treeSitter() bool {
	f, _ := outline.Outline(context.Background(), "/x/a.py", []byte("def f():\n    pass\n"), outline.Options{})
	return f.Backend == outline.BackendTreeSitter
}

// needleFiles is a small tree whose Go files mention needle.
var needleFiles = map[string]string{
	"a.go": "package p\n\n// Needle mentions needle.\nfunc Needle() int { return 1 }\n",
	"b.go": "package p\n\n// Other does not.\nfunc Other() int { return 2 }\n",
	"c.go": "package p\n\n// Third mentions needle too.\nfunc Third() int { return 3 }\n",
}

// storeOf is the per-workspace store directory of ws under indexDir.
func storeOf(ws *workspace.Workspace, indexDir string) string {
	sum := sha256.Sum256([]byte(ws.Root()))
	return filepath.Join(indexDir, fmt.Sprintf("agentkit-codesearch-%x", sum[:]))
}

// readManifest decodes the manifest of a store directory, or fails the test.
func readManifest(t *testing.T, store string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(store, "manifest.json"))
	if err != nil {
		t.Fatalf("reading the manifest: %v", err)
	}
	return testutil.Object(t, b)
}

// treeOf is every path under dir, relative and sorted.
func treeOf(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}
