package tools

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// newWS writes files into a fresh directory and opens a workspace over it
// that reads no global git excludes.
func newWS(t testing.TB, files map[string]string) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Open(testutil.Tree(t, files), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// toolSet is For(ws, opts) keyed by tool name.
func toolSet(t testing.TB, ws *workspace.Workspace, opts Options) map[string]core.Tool {
	t.Helper()
	all, err := For(ws, opts)
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]core.Tool, len(all))
	for _, tl := range all {
		m[tl.Name] = tl
	}
	return m
}

// tool is one tool of the default set over a workspace holding files.
func tool(t testing.TB, files map[string]string, name string) core.Tool {
	t.Helper()
	return toolSet(t, newWS(t, files), Options{})[name]
}

// newFT is the fileTools behind For over ws.
func newFT(ws *workspace.Workspace, opts Options) *fileTools {
	return newFileTools(ws, opts.withDefaults(ws))
}

// call runs tl with args on a background context.
func call(tl core.Tool, args string) core.ToolResult {
	return tl.Execute(context.Background(), json.RawMessage(args))
}

// callJSON runs tl with args marshalled to JSON.
func callJSON(t testing.TB, tl core.Tool, args any) core.ToolResult {
	t.Helper()
	return call(tl, testutil.MustJSON(t, args))
}

// mustOK fails t unless r is a success.
func mustOK(t testing.TB, r core.ToolResult) core.ToolResult {
	t.Helper()
	if !r.OK {
		t.Fatalf("OK false: %s: %s", r.Error, r.Detail)
	}
	return r
}

// needPOSIX skips a test whose fixture is a POSIX shell command.
func needPOSIX(t testing.TB) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture is a POSIX shell command")
	}
	if _, _, err := ResolveShell(); err != nil {
		t.Skip("no shell available")
	}
}

func mustSymlink(t testing.TB, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}

func writeAt(t testing.TB, root, rel, body string) {
	t.Helper()
	testutil.WriteTree(t, root, map[string]string{rel: body})
}

// symbolNames lists the names in a find_symbol result.
func symbolNames(r core.ToolResult) []string {
	var names []string
	for _, m := range r.Data["symbols"].([]SymbolMatch) {
		names = append(names, m.Name)
	}
	return names
}

// findNames runs find_symbol for an exact name and lists what it found.
func findNames(t testing.TB, ft *fileTools, name string) []string {
	t.Helper()
	return symbolNames(mustOK(t, callJSON(t, ft.findSymbolTool(), map[string]any{"name": name, "exact": true})))
}

// countWalks counts the symbol table's walks for the rest of the test.
func countWalks(t testing.TB) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	orig := walkFn
	walkFn = func(ctx context.Context, ws *workspace.Workspace, sub string, opts workspace.WalkOptions, fn func(string, fs.DirEntry) error) error {
		n.Add(1)
		return orig(ctx, ws, sub, opts, fn)
	}
	t.Cleanup(func() { walkFn = orig })
	return &n
}

// holdBuild makes every symbol-table outline batch of ft wait until release
// is called or the build's context ends; started is signalled on the first.
func holdBuild(ft *fileTools) (started <-chan struct{}, release func()) {
	s, gate := make(chan struct{}, 1), make(chan struct{})
	ft.testOutlineHook = func(ctx context.Context) {
		select {
		case s <- struct{}{}:
		default:
		}
		select {
		case <-gate:
		case <-ctx.Done():
		}
	}
	var once sync.Once
	return s, func() { once.Do(func() { close(gate) }) }
}

// marks reads the symbol table's pending marks.
func marks(st *symbolTable) (dirty map[string]bool, all bool) {
	st.markMu.Lock()
	defer st.markMu.Unlock()
	return st.dirtyPaths, st.revalidateAll
}

// fakeIndex is an Index that records its Invalidate calls, answers Symbols
// with ans when ok is set, and lists candidate files when cands is set.
type fakeIndex struct {
	mu    sync.Mutex
	calls []string
	tools []core.Tool
	ans   SymbolAnswer
	ok    bool
	err   error
}

func (f *fakeIndex) Symbols(context.Context, SymbolQuery) (SymbolAnswer, bool, error) {
	return f.ans, f.ok, f.err
}
func (f *fakeIndex) Tools() []core.Tool { return f.tools }
func (f *fakeIndex) Close() error       { return nil }
func (f *fakeIndex) Invalidate(rel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, rel)
}

// take returns the Invalidate calls so far and forgets them.
func (f *fakeIndex) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.calls
	f.calls = nil
	return out
}

// candidateFakeIndex also answers which files mention a name.
type candidateFakeIndex struct {
	fakeIndex
	cands []string
}

func (c *candidateFakeIndex) CandidateFiles(context.Context, string) ([]string, error) {
	return c.cands, nil
}

// abs is rel under ws's root.
func abs(ws *workspace.Workspace, rel string) string {
	return filepath.Join(ws.Root(), filepath.FromSlash(rel))
}
