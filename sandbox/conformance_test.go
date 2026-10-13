package sandbox_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/toolconformance"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// proxyBuilder syncs tree into a fresh local sandbox and returns its proxy
// tools.
func proxyBuilder(t *testing.T, tree toolconformance.Tree) []core.Tool {
	t.Helper()
	sb := newLocal(t)
	if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(tree.Tar(t))}); err != nil {
		t.Fatal(err)
	}
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

// TS-13-64: the shared internal/toolconformance case table passes against
// proxy tools over local (the tools package runs it against tools.All).
// 13-REQ-10.4.
func TestProxyConformance_TS13_64(t *testing.T) {
	isolate(t)
	names := toolconformance.CaseNames()
	for _, prev := range []string{"TS-06-22", "TS-06-23", "TS-06-24", "TS-06-25", "TS-06-26",
		"TS-06-27", "TS-06-28", "TS-06-29", "TS-06-30", "TS-06-33"} {
		if !slices.Contains(names, prev) {
			t.Errorf("the case table lost %s", prev)
		}
	}
	for _, id := range names {
		t.Run(id, func(t *testing.T) { toolconformance.RunGroup(t, id, proxyBuilder) })
	}
}
