package podman

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-17-59: the offline provider tests need no container engine on PATH:
// Create, Sync, Tools, execute, Reap and Close all run through the fake
// runner seam. 17-REQ-10.1.
func TestOfflineNoEngine_TS17_59(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	f := newFake(t)
	f.agentEnv = []string{"PATH=/usr/bin:/bin"} // the in-process agent's tools need a shell
	p := testProvider(f, Options{})
	ctx := context.Background()
	s, err := p.Create(ctx, sandbox.Spec{Image: "img:1"})
	if err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatal(err)
	}
	ts, err := s.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r := callTool(t, ts, "execute", map[string]any{"command": "echo hi"}); !r.OK || !strings.Contains(r.Text, "hi") {
		t.Fatalf("execute echo hi: %+v", r)
	}
	if _, err := p.Reap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.recorded() {
		if c.argv[0] != "/usr/bin/podman" {
			t.Errorf("a command ran something other than the provider's resolved binary: %q", c.argv)
		}
	}
}
