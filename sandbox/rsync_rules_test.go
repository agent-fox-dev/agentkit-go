//go:build !windows

package sandbox_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
)

// symlinkTree writes the TS-14-11 links (name -> target) and a.txt under
// root.
var symlinkTree = map[string]string{
	"abs": "/etc/hosts", "up": "../x", "sub/up2": "../../x", "sub/dd": "x/../../..",
	"sub/in": "../a.txt", "self": ".", "sub/in2": "y/../a",
}

func writeLinks(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range symlinkTree {
		p := filepath.Join(root, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
}

// rawPlan opens a raw Rsync stream on sb, sends start and returns the plan.
func rawPlan(t *testing.T, client sandboxpb.AgentClient, start *sandboxpb.RsyncStart) (*sandboxpb.RsyncPlan, error) {
	t.Helper()
	st, err := client.Rsync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.CloseSend() }()
	if err := st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: start}}); err != nil {
		t.Fatal(err)
	}
	f, err := st.Recv()
	if err != nil {
		return nil, err
	}
	return f.GetPlan(), nil
}

// TS-14-11: every escaping symlink gets a rule on both sides, decided by
// tarsafe's symlink predicate and independent of IncludeIgnored.
// 14-REQ-3.3.
func TestEscapingSymlinkRules_TS14_11(t *testing.T) {
	root := t.TempDir()
	writeLinks(t, root)
	want := []string{"abs", "sub/dd", "sub/up2", "up"}
	for _, inc := range []bool{false, true} {
		rs, err := rsyncx.Compute(context.Background(), root, rsyncx.RuleOptions{IncludeIgnored: inc})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rs.Escaping, want) {
			t.Errorf("IncludeIgnored %v: escaping rules %v, want %v", inc, rs.Escaping, want)
		}
		for name, target := range symlinkTree {
			if slices.Contains(rs.Escaping, name) != tarsafe.SymlinkEscapes(name, target) {
				t.Errorf("%s -> %s: rule %v, predicate %v", name, target, slices.Contains(rs.Escaping, name), tarsafe.SymlinkEscapes(name, target))
			}
		}
	}

	isolate(t)
	sb := newLocal(t)
	writeLinks(t, sb.Agent().Workdir)
	plan, err := rawPlan(t, localtest.RawClient(sb), &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !slices.Contains(plan.Rules, w) {
			t.Errorf("the plan's rules %v lack %s", plan.Rules, w)
		}
	}
	for _, r := range plan.Rules {
		if strings.HasPrefix(r, "sub/in") || r == "self" {
			t.Errorf("the plan excludes the inside link %s", r)
		}
	}
}
