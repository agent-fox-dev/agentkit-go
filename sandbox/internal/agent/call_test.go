package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// An unknown tool, a call before Tools, a result that does not marshal and
// a panic are failed results with RPC status OK, and the agent keeps serving.
func TestCallFailuresAreResultsNotRPCErrors(t *testing.T) {
	a := newAgent(t, "", testEnv(t), time.Hour)
	a.build = func(*workspace.Workspace, tools.Options) ([]core.Tool, error) {
		return []core.Tool{
			stub("ok", func(context.Context) core.ToolResult { return core.OKResult(map[string]any{"v": 1}) }),
			stub("baddata", func(context.Context) core.ToolResult { return core.OKResult(map[string]any{"ch": make(chan int)}) }),
			stub("boom", func(context.Context) core.ToolResult { panic("kaboom") }),
		}, nil
	}
	c, _ := serveRaw(t, a)
	ctx := context.Background()
	if r := call(t, c, "ok", `{}`); r.Ok || r.Error != "unknown_tool" {
		t.Fatalf("a call before Tools: %v; want unknown_tool", r)
	}
	if _, err := c.Tools(ctx, &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	r := call(t, c, "nosuch", `{}`)
	if r.Ok || r.Error != "unknown_tool" || !strings.Contains(r.Detail, "ok, baddata, boom") {
		t.Fatalf("nosuch: %v; want unknown_tool listing the tools", r)
	}
	for name, code := range map[string]string{"baddata": "marshal_failed", "boom": "panic"} {
		if r := call(t, c, name, `{}`); r.Ok || r.Error != code {
			t.Errorf("%s: %v; want %s", name, r, code)
		}
	}
	if r := call(t, c, "ok", `{}`); !r.Ok || string(r.Data) != `{"v":1}` || len(r.Metadata) != 0 {
		t.Errorf("ok: %v", r)
	}
	if _, err := c.Hello(ctx, &sandboxpb.HelloRequest{}); err != nil {
		t.Fatalf("the agent stopped serving: %v", err)
	}
}

// Calls run concurrently: four calls that each wait for all four to start
// all finish.
func TestCallRunsCallsConcurrently(t *testing.T) {
	const n = 4
	var entered atomic.Int32
	all := make(chan struct{})
	a := newAgent(t, "", testEnv(t), time.Hour)
	a.build = func(*workspace.Workspace, tools.Options) ([]core.Tool, error) {
		return []core.Tool{stub("barrier", func(ctx context.Context) core.ToolResult {
			if entered.Add(1) == n {
				close(all)
			}
			select {
			case <-all:
				return core.OKResult(nil)
			case <-ctx.Done():
				return core.ErrResult("timeout", "the other calls did not start")
			}
		})}, nil
	}
	c, _ := serveRaw(t, a)
	if _, err := c.Tools(context.Background(), &sandboxpb.ToolsRequest{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := make(chan *sandboxpb.CallResponse, n)
	for range n {
		go func() {
			r, _ := c.Call(ctx, &sandboxpb.CallRequest{Name: "barrier"})
			results <- r
		}()
	}
	for range n {
		if r := <-results; !r.GetOk() {
			t.Errorf("barrier call: %v; calls are serialised", r)
		}
	}
}
