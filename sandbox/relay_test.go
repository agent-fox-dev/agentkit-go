//go:build !windows

package sandbox_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
)

// treeOf maps every entry under root to its type and content or target.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			out[rel] = "-> " + l
		case d.IsDir():
			out[rel] = "dir"
		default:
			b, _ := os.ReadFile(p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}

// TS-14-6: frames flow start, plan, args, data, done, and a relay forwarding
// RsyncFrame messages unchanged gives the same result as a direct stream.
// 14-REQ-2.3, 14-REQ-2.7.
func TestRelayedTransfers_TS14_6(t *testing.T) {
	relayed, direct := relaytest.New(t), relaytest.Direct(t)
	src := t.TempDir()
	writeFiles(t, src, map[string]string{".gitignore": "*.tmp\n", "a/b/c.txt": "c", "d.txt": "d", "x.tmp": "t",
		"big.bin": strings.Repeat("z", 3<<20)})
	ctx := context.Background()
	var results [2]sandbox.SyncResult
	for i, e := range []*relaytest.Env{relayed, direct} {
		r, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic})
		if err != nil {
			t.Fatal(err)
		}
		results[i] = r
		if err := e.Sandbox.Export(ctx, ".", sandbox.LocalDir{Path: filepath.Join(t.TempDir(), "dst")}); err != nil {
			t.Fatal(err)
		}
	}
	trs := relayed.Relay.Transfers()
	if len(trs) != 2 {
		t.Fatalf("%d transfers recorded, want 2", len(trs))
	}
	for i, tr := range trs {
		host, agent := tr.Kinds(true), tr.Kinds(false)
		if len(host) < 2 || host[0] != "start" || host[1] != "args" || slices.ContainsFunc(host[2:], func(k string) bool { return k != "data" }) {
			t.Errorf("transfer %d: host frames %v, want start, args, then data", i, host)
		}
		n := len(agent)
		if n < 2 || agent[0] != "plan" || agent[n-1] != "done" || slices.ContainsFunc(agent[1:n-1], func(k string) bool { return k != "data" }) {
			t.Errorf("transfer %d: agent frames %v, want plan, data, done", i, agent)
		}
		planAt, argsAt := -1, -1
		for j, f := range tr.Frames {
			if f.Kind == "plan" && planAt < 0 {
				planAt = j
			}
			if f.Kind == "args" {
				argsAt = j
			}
		}
		if planAt < 0 || argsAt < planAt {
			t.Errorf("transfer %d: args at %d before plan at %d", i, argsAt, planAt)
		}
		if tr.Status != nil || tr.OtherRPCs != 0 {
			t.Errorf("transfer %d: status %v, %d other RPCs", i, tr.Status, tr.OtherRPCs)
		}
	}
	r, d := results[0], results[1]
	if r.Files != d.Files || r.Bytes != d.Bytes || r.Deleted != d.Deleted {
		t.Errorf("relayed %+v, direct %+v", r, d)
	}
	if !mapsEqual(treeOf(t, relayed.Workspace), treeOf(t, direct.Workspace)) {
		t.Error("the relayed and direct workspaces differ")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
