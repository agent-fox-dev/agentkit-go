//go:build !windows

package sandbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-66 (smoke, 20-PATH-3): a host opens a workspace, syncs a local
// directory into a local sandbox with LocalDir{Ignore: ws.Ignore()}, and
// attaches to the sandbox's agent: ignored files are not transferred, the
// attach finds no ErrToolMismatch, and a read_file call through the proxies
// returns a synced file. Every component is real: workspace, rsyncx, the local
// provider, the agent, the host's attach and tools.For.
func TestSmokeLocalDirSyncAndAttach_TS20_66(t *testing.T) {
	isolate(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		".gitignore":  "ignored.txt\nbuild/\n",
		"keep.txt":    "synced content\n",
		"ignored.txt": "not transferred\n",
		"build/out.o": "not transferred\n",
	})
	ws, err := workspace.Open(src, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}

	sb := newLocal(t)
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: ws.Ignore()}); err != nil {
		t.Fatalf("Sync(LocalDir): %v", err)
	}
	workdir := sb.Agent().Workdir
	if !lexists(filepath.Join(workdir, "keep.txt")) {
		t.Error("keep.txt was not transferred")
	}
	for _, rel := range []string{"ignored.txt", "build"} {
		if lexists(filepath.Join(workdir, rel)) {
			t.Errorf("%s is ignored by the workspace's rules and was transferred", rel)
		}
	}

	proxies, err := sb.Tools(ctx, sandbox.ToolOptions{})
	if errors.Is(err, sandbox.ErrToolMismatch) {
		t.Fatalf("attach refused the agent: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != 11 {
		t.Fatalf("the host exposes %d tools, want 11", len(proxies))
	}
	r := tool(t, sb, "read_file").Execute(ctx, []byte(`{"path":"keep.txt"}`))
	if !r.OK || !strings.Contains(r.Text, "synced content") {
		t.Fatalf("read_file through the proxies: %+v", r)
	}
}
