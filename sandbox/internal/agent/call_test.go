package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-13-20: unknown tools, calls before Tools, marshal failures and panics
// come back as failed results with RPC status OK. 13-REQ-3.6.
func TestCallFailuresAreResults_TS13_20(t *testing.T) {
	a := newAgent(t, "", testEnv(t), time.Hour)
	stub := func(name string, run func() core.ToolResult) core.Tool {
		return core.Tool{Name: name, Execute: func(context.Context, json.RawMessage) core.ToolResult { return run() }}
	}
	a.build = func(*workspace.Workspace, tools.Options) ([]core.Tool, error) {
		return []core.Tool{
			stub("ok", func() core.ToolResult { return core.OKResult(map[string]any{"v": 1}) }),
			stub("badjson", func() core.ToolResult { return core.OKResult(map[string]any{"ch": make(chan int)}) }),
			stub("boom", func() core.ToolResult { panic("kaboom") }),
		}, nil
	}
	c, _ := serveRaw(t, a)
	ctx := context.Background()

	r, err := c.Call(ctx, &sandboxpb.CallRequest{Name: "ok", Input: []byte(`{}`)})
	if err != nil || r.Ok || r.Error != "unknown_tool" {
		t.Fatalf("a call before Tools: %v, %v; want unknown_tool and no RPC error", r, err)
	}
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	r, err = c.Call(ctx, &sandboxpb.CallRequest{Name: "nosuch", Input: []byte(`{}`)})
	if err != nil || r.Ok || r.Error != "unknown_tool" {
		t.Fatalf("nosuch: %v, %v", r, err)
	}
	for _, n := range []string{"ok", "badjson", "boom"} {
		if !strings.Contains(r.Detail, n) {
			t.Errorf("unknown_tool detail %q does not list %s", r.Detail, n)
		}
	}
	for name, code := range map[string]string{"badjson": "marshal_failed", "boom": "panic"} {
		r, err := c.Call(ctx, &sandboxpb.CallRequest{Name: name, Input: []byte(`{}`)})
		if err != nil || r.Ok || r.Error != code {
			t.Errorf("%s: %v, %v; want %s and no RPC error", name, r, err, code)
		}
	}
	if r := call(t, c, "ok", `{}`); !r.Ok || string(r.Data) != `{"v":1}` {
		t.Errorf("ok: %v", r)
	}
	if _, err := c.Hello(ctx, &sandboxpb.HelloRequest{}); err != nil {
		t.Fatalf("the agent stopped serving: %v", err)
	}
}
