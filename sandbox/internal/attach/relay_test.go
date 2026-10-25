//go:build !windows

package attach_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// Frames flow start, args, data from the host and plan, data, done from the
// agent, so a relay that forwards them unchanged gives the direct result.
func TestRsyncThroughARelayIsTheDirectTransfer(t *testing.T) {
	src := testutil.Tree(t, map[string]string{".gitignore": "*.tmp\n", "a/b/c.txt": "c", "x.tmp": "t", "big.bin": strings.Repeat("z", 3<<20)})
	ld := sandbox.LocalDir{Path: src, Ignore: workspace.NoGlobalExcludes()}
	relayed, direct := relaytest.New(t), relaytest.Direct(t)
	var results []sandbox.SyncResult
	for _, e := range []*relaytest.Env{relayed, direct} {
		r, err := e.Sandbox.Sync(context.Background(), ld)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, r)
	}
	if r, d := results[0], results[1]; r.Files != d.Files || r.Bytes != d.Bytes || r.Files != 3 {
		t.Errorf("relayed %+v, direct %+v", r, d)
	}
	tr := relayed.Relay.Last()
	host, agent := tr.Kinds(true), tr.Kinds(false)
	data := func(ks []string) bool { return !slices.ContainsFunc(ks, func(k string) bool { return k != "data" }) }
	if n := len(agent); len(host) < 2 || host[0] != "start" || host[1] != "args" || !data(host[2:]) ||
		n < 2 || agent[0] != "plan" || agent[n-1] != "done" || !data(agent[1:n-1]) || tr.Status != nil {
		t.Errorf("host frames %v, agent frames %v, status %v", host, agent, tr.Status)
	}
	if testutil.MustJSON(t, tree(direct.Workspace)) != testutil.MustJSON(t, tree(relayed.Workspace)) {
		t.Error("the relayed and direct workspaces differ")
	}
}

// Host data corrupted in flight fails the agent's client with the stream
// intact: the Sync fails and the sandbox stays live.
func TestRsyncCorruptedInFlightLeavesTheSandboxLive(t *testing.T) {
	e := relaytest.New(t)
	e.Relay.SetMutator(func(fromHost bool, index int, f *sandboxpb.RsyncFrame) *sandboxpb.RsyncFrame {
		if !fromHost || f.GetData() == nil || index < 3 {
			return f
		}
		flipped := slices.Clone(f.GetData())
		for i := range flipped {
			flipped[i] = ^flipped[i]
		}
		return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: flipped}}
	})
	src := testutil.Tree(t, map[string]string{"a.txt": strings.Repeat("a", 100<<10), "b/c.txt": "c"})
	if _, err := e.Sandbox.Sync(context.Background(), sandbox.LocalDir{Path: src, Ignore: workspace.NoGlobalExcludes()}); err == nil {
		t.Error("the corrupted Sync succeeded")
	}
	e.Relay.SetMutator(nil)
	tl, err := e.Sandbox.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tl {
		if r := p.Execute(context.Background(), []byte(`{"path":"."}`)); p.Name == "list_files" && !r.OK {
			t.Errorf("list_files afterwards: %+v", r)
		}
	}
}

// tree maps every file under root, relative to it, to its content.
func tree(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			rel, _ := filepath.Rel(root, p)
			out[rel] = string(b)
		}
		return nil
	})
	return out
}
