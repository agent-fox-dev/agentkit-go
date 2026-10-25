package podman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
)

// It also unwraps, so attach.ClientConn and attach.Live reach the agent.
func TestTheSandboxForwardsToTheAttachedAgent(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no container engine anywhere
	f := withGit(t, newFake(t))
	s := create(t, f, sandbox.Spec{})
	ctx := context.Background()
	if s.ID() != volumeID(f) || !s.Agent().Git || s.Spec().Image != "img:1" {
		t.Errorf("ID %q, Agent %+v, Spec().Image %q", s.ID(), s.Agent(), s.Spec().Image)
	}
	if cc := attach.ClientConn(s); cc == nil || cc != attach.ClientConn(s.(interface{ Unwrap() sandbox.Sandbox }).Unwrap()) || attach.Live(s) != nil {
		t.Errorf("attach.ClientConn(sandbox) = %v, want the attached sandbox's", cc)
	}
	r, err := s.Sync(ctx, sandbox.LocalDir{Path: testutil.Tree(t, map[string]string{"a.txt": "a", "b.txt": "b"})})
	if err != nil || r.Files != 2 {
		t.Fatalf("Sync = %+v, %v; want 2 files", r, err)
	}
	ts, err := s.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res := callTool(t, ts, "execute", map[string]any{"command": "echo hi > new.txt && cat a.txt"}); !res.OK || !strings.Contains(res.Text, "a") {
		t.Fatalf("execute: %+v", res)
	}
	out := t.TempDir()
	if err := s.Export(ctx, ".", sandbox.LocalDir{Path: out}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(out, "new.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("Export brought back new.txt = %q, %v", b, err)
	}
	if err := s.Close(ctx); err != nil || !f.ran("volume", "rm", "agentkit-"+s.ID()) {
		t.Errorf("Close = %v, destroy step ran %t", err, f.ran("volume", "rm"))
	}
	for _, c := range f.recorded() {
		if c.argv[0] != "/usr/bin/podman" {
			t.Errorf("a command ran something other than the resolved binary: %q", c.argv)
		}
	}
}

func TestAnAgentThatEndsEarlyKeepsTheVolumeUntilClose(t *testing.T) {
	f := newFake(t)
	s := create(t, f, sandbox.Spec{})
	f.lastProc().exit(0)
	ctx := context.Background()
	testutil.Eventually(t, "Tools to report ErrUnavailable", func() bool {
		_, err := s.Tools(ctx, sandbox.ToolOptions{})
		return errors.Is(err, sandbox.ErrUnavailable)
	})
	_, serr := s.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()})
	if eerr := s.Export(ctx, ".", sandbox.LocalDir{Path: t.TempDir()}); !errors.Is(serr, sandbox.ErrUnavailable) || !errors.Is(eerr, sandbox.ErrUnavailable) {
		t.Errorf("Sync = %v, Export = %v; want ErrUnavailable", serr, eerr)
	}
	early := f.ran("volume", "rm")
	if _ = s.Close(ctx); early || !f.ran("volume", "rm", "agentkit-"+s.ID()) {
		t.Errorf("the volume was removed before Close (%t) or not by it", early)
	}
}
