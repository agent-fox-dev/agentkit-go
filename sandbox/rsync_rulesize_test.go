//go:build !windows

package sandbox_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// ruleList is n bytes of rules.
func ruleList(n int) []string {
	var out []string
	for n > 0 {
		k := min(n, 200)
		out = append(out, strings.Repeat("r", k))
		n -= k
	}
	return out
}

// ignoredFiles writes, under root, a .gitignore of *.ign and enough empty
// ignored files with 240-byte names starting with side to give at least n
// bytes of rules.
func ignoredFiles(t *testing.T, root, side string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.ign\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("n", 240-4-8-len(side))
	for i := 0; i*240 < n; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%s%08d%s.ign", side, i, pad)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TS-14-15: rules over 8 MiB are refused before any data frame, by the
// agent with FailedPrecondition before plan and by the host before args,
// naming the size and the limit. 14-REQ-3.7.
func TestRuleSizeCap_TS14_15(t *testing.T) {
	if err := rsyncx.CheckRuleSize(ruleList(8 << 20)); err != nil {
		t.Errorf("8 MiB of rules refused: %v", err)
	}
	err := rsyncx.CheckRuleSize(ruleList(8<<20 + 1))
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(8<<20+1)) || !strings.Contains(err.Error(), "8 MiB") {
		t.Errorf("8 MiB + 1 of rules: %v", err)
	}
	if testing.Short() {
		t.Skip("the file-heavy cases are skipped under -short")
	}
	ctx := context.Background()
	for _, c := range []struct {
		name       string
		host, agen int
	}{{"host", 8<<20 + 240, 0}, {"split", 5 << 20, 5 << 20}} {
		e := relaytest.New(t)
		src := t.TempDir()
		ignoredFiles(t, src, "h", c.host)
		if c.agen > 0 {
			ignoredFiles(t, e.Workspace, "a", c.agen)
		}
		before := treeOf(t, e.Workspace)
		_, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic})
		if err == nil || !strings.Contains(err.Error(), "8 MiB") {
			t.Errorf("%s: Sync = %v, want an error naming 8 MiB", c.name, err)
		}
		tr := e.Relay.Last()
		if k := tr.Kinds(false); len(k) != 1 || k[0] != "plan" {
			t.Errorf("%s: agent frames %v, want only plan", c.name, k)
		}
		if k := tr.Kinds(true); len(k) != 1 || k[0] != "start" {
			t.Errorf("%s: host frames %v, want only start", c.name, k)
		}
		if !mapsEqual(treeOf(t, e.Workspace), before) {
			t.Errorf("%s: the workspace changed", c.name)
		}
		proxyCall(t, e.Sandbox, "list_files", `{"path":"."}`)
	}

	e := relaytest.New(t)
	ignoredFiles(t, e.Workspace, "a", 8<<20+240)
	raw := localtest.RawClient(e.Sandbox)
	plan, err := rawPlan(t, raw, &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC})
	if plan != nil || status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "8 MiB") {
		t.Errorf("the raw stream got plan %v, %v; want FailedPrecondition naming 8 MiB", plan != nil, err)
	}
	_, err = e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: t.TempDir(), Ignore: hermetic})
	if err == nil || !strings.Contains(err.Error(), "8 MiB") || status.Code(err) != codes.FailedPrecondition {
		t.Errorf("Sync with oversized agent rules = %v", err)
	}
	proxyCall(t, e.Sandbox, "list_files", `{"path":"."}`)
}
