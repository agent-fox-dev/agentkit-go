package gatewayd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/toolconformance"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// mustJSON is v as JSON.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TS-19-67: every proxy tool from a gateway sandbox matches tools.All and
// passes the shared conformance table. 19-REQ-10.1.
func TestParity_TS19_67(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	sb := create(t, p, unrestrictedSpec)
	proxies, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if len(proxies) != len(host) {
		t.Fatalf("%d proxies, %d tools.All tools", len(proxies), len(host))
	}
	for i, px := range proxies {
		h := host[i]
		if px.Name != h.Name || px.Description != h.Description || !slices.Equal(px.PromptGuidelines, h.PromptGuidelines) ||
			px.Builtin != h.Builtin || px.ExecutionMode != h.ExecutionMode || px.Label != h.Label {
			t.Errorf("%s differs from tools.All's %s in a definition field", px.Name, h.Name)
		}
		if mustJSON(t, px.InputSchema) != mustJSON(t, h.InputSchema) || mustJSON(t, px.OutputSchema) != mustJSON(t, h.OutputSchema) {
			t.Errorf("%s: the schemas differ from tools.All's", px.Name)
		}
	}
	builder := func(t *testing.T, tree toolconformance.Tree) []core.Tool {
		t.Helper()
		sb := create(t, p, unrestrictedSpec)
		if _, err := sb.Sync(context.Background(), sandbox.Tar{R: bytes.NewReader(tree.Tar(t))}); err != nil {
			t.Fatal(err)
		}
		tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	for _, id := range toolconformance.CaseNames() {
		t.Run(id, func(t *testing.T) { toolconformance.RunGroup(t, id, builder) })
	}
}
