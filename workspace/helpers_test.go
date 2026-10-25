package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// open opens a workspace over root with no global excludes.
func open(t *testing.T, root string) *Workspace {
	t.Helper()
	ws, err := Open(root, Options{Ignore: NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// openTree opens a workspace, with no global excludes, over a new tree.
func openTree(t *testing.T, files map[string]string) *Workspace {
	t.Helper()
	return open(t, testutil.Tree(t, files))
}

// withGlobalExcludes is NoGlobalExcludes with a global excludes file holding
// patterns, found through the GitConfig hook.
func withGlobalExcludes(t *testing.T, patterns string) IgnoreOptions {
	t.Helper()
	file := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(file, []byte(patterns), 0o644); err != nil {
		t.Fatal(err)
	}
	o := NoGlobalExcludes()
	o.GitConfig = func() string { return file }
	return o
}

// counting is an IgnoreOptions whose hooks count their calls; GitConfig
// answers path.
type counting struct {
	calls, lookups atomic.Int32
	path           string
}

func (c *counting) options() IgnoreOptions {
	return IgnoreOptions{
		Getenv:    func(string) string { c.calls.Add(1); return "" },
		Home:      func() (string, error) { c.calls.Add(1); return "", os.ErrNotExist },
		GitConfig: func() string { c.lookups.Add(1); return c.path },
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
}

// walked is every rel path ws.Walk visits from sub, sorted.
func walked(t *testing.T, ws *Workspace, sub string, opts WalkOptions) []string {
	t.Helper()
	got := []string{}
	err := ws.Walk(context.Background(), sub, opts, func(rel string, _ fs.DirEntry) error {
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%q): %v", sub, err)
	}
	slices.Sort(got)
	return got
}

// notIgnored is every entry under the root that Ignored allows, sorted: what a
// walk with hidden entries included must visit.
func notIgnored(t *testing.T, ws *Workspace) []string {
	t.Helper()
	got := []string{}
	err := filepath.WalkDir(ws.Root(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == ws.Root() {
			return err
		}
		rel, _ := filepath.Rel(ws.Root(), p)
		if rel = filepath.ToSlash(rel); !ws.Ignored(rel, d.IsDir()) {
			got = append(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	return got
}
