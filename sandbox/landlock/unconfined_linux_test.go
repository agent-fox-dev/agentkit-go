package landlock

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func init() { children["unconfined"] = unconfinedChild }

// unconfinedChild uses the provider end to end, then writes a file outside
// any workspace and connects to the parent's listener: the package applied
// nothing to its own process.
func unconfinedChild() error {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "landlock-child-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	p, err := New(Options{Root: root, AgentBinary: agentBin})
	if err != nil {
		return err
	}
	s, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		return err
	}
	src, err := os.MkdirTemp("", "landlock-src-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(src)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("a"), 0o644); err != nil {
		return err
	}
	if _, err := s.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		return err
	}
	ts, err := s.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		return err
	}
	for _, tl := range ts {
		if tl.Name == "execute" {
			if r := tl.Execute(ctx, []byte(`{"command":"true"}`)); !r.OK {
				return errors.New("execute true failed: " + r.Error)
			}
		}
	}
	if err := s.Close(ctx); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("AGENTKIT_LANDLOCK_OUT"), "after-close"), []byte("x"), 0o644); err != nil {
		return err
	}
	c, err := net.DialTimeout("tcp", os.Getenv("AGENTKIT_LANDLOCK_ADDR"), 5*time.Second)
	if err != nil {
		return err
	}
	return c.Close()
}

// TS-18-64: an embedder process that used the provider end to end stays
// unconfined for files and TCP. 18-REQ-10.3.
func TestEmbedderUnconfined_TS18_64(t *testing.T) {
	needABI4(t)
	ln := listen(t, "")
	sibling := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childVar+"=unconfined", childAgentVar+"="+agentBin,
		"AGENTKIT_LANDLOCK_ADDR="+ln.Addr().String(), "AGENTKIT_LANDLOCK_OUT="+sibling)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the child: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(sibling, "after-close")); err != nil {
		t.Errorf("the child could not write outside the workspace: %v", err)
	}
	if n := ln.accepted.Load(); n != 1 {
		t.Errorf("the listener accepted %d connections, want 1", n)
	}
}
