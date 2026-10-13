//go:build !windows

package sandbox_test

import (
	"context"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/treegen"
)

// TS-14-19: for every generated tree, a LocalDir Sync with PATH emptied
// leaves the workspace with exactly Walk's entries plus .git trees minus
// escaping symlinks. 14-REQ-4.3.
func TestSyncMatchesWalk_TS14_19(t *testing.T) {
	isolate(t)
	t.Setenv("PATH", "")
	ctx := context.Background()
	for seed := uint64(1); seed <= 100; seed++ {
		src := treegen.Gen(t, seed)
		expect := treegen.WalkSet(t, src)
		maps.Copy(expect, treegen.GitTrees(t, src))
		for p := range treegen.Escaping(t, src) {
			delete(expect, p)
		}
		sb := newLocal(t)
		ws := sb.Agent().Workdir
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		got := treegen.Entries(t, ws)
		for p := range expect {
			want := treegen.Describe(filepath.Join(src, filepath.FromSlash(p)), false)
			if got[p] != want {
				t.Errorf("seed %d: %s is %q, want %q", seed, p, got[p], want)
			}
		}
		for p := range got {
			if !expect[p] {
				t.Errorf("seed %d: the workspace has %s, which is not in the transfer set", seed, p)
			}
		}
		_ = sb.Close(ctx)
		if t.Failed() {
			return
		}
	}
}

// gitPaths is every path with a .git component under root.
func gitPaths(root string) map[string]bool {
	out := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == ".git" {
			out[treegen.Rel(root, p)] = true
			if d.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	return out
}

// TS-14-16: a path excluded by either side's rules is left untouched on the
// receiving side in both directions, and nothing outside the receiving tree
// changes. 14-REQ-3.8.
func TestEitherSideExclusion_TS14_16(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	for seed := uint64(1); seed <= 50; seed++ {
		for _, export := range []bool{false, true} {
			outside := t.TempDir()
			writeFiles(t, outside, map[string]string{"keep.txt": "k"})
			sb := newLocal(t)
			ws := sb.Agent().Workdir
			src, recv := t.TempDir(), ws
			if export {
				src, recv = ws, t.TempDir()
			}
			treegen.Pair(t, seed, src, recv, outside)
			excl := map[string]bool{}
			for _, root := range []string{src, recv} {
				maps.Copy(excl, treegen.Ignored(t, root))
				maps.Copy(excl, treegen.Escaping(t, root))
				if export {
					maps.Copy(excl, gitPaths(root))
				}
			}
			before, out0 := treegen.Snapshot(t, recv, excl), treegen.Snapshot(t, outside, map[string]bool{".": true})
			var err error
			if export {
				err = sb.Export(ctx, ".", sandbox.LocalDir{Path: recv, Ignore: hermetic, Delete: true})
			} else {
				_, err = sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic, Delete: true})
			}
			if err != nil {
				t.Fatalf("seed %d, export %v: %v", seed, export, err)
			}
			after := treegen.Snapshot(t, recv, excl)
			for _, p := range slices.Sorted(maps.Keys(before)) {
				if after[p] != before[p] {
					t.Errorf("seed %d, export %v: excluded %s went from %q to %q", seed, export, p, before[p], after[p])
				}
			}
			for p := range after {
				if _, ok := before[p]; !ok {
					t.Errorf("seed %d, export %v: excluded %s appeared: %q", seed, export, p, after[p])
				}
			}
			if out1 := treegen.Snapshot(t, outside, map[string]bool{".": true}); !maps.Equal(out0, out1) {
				t.Errorf("seed %d, export %v: outside changed: %v, was %v", seed, export, out1, out0)
			}
			_ = sb.Close(ctx)
			if t.Failed() {
				t.Logf("seed %d, export %v: excluded %s", seed, export, strings.Join(slices.Sorted(maps.Keys(excl)), ", "))
				return
			}
		}
	}
}
