package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
)

// childTool echoes its arguments back as Data, so a test can see what the
// handler received.
func childTool(name string) core.Tool {
	return core.Tool{
		Name:        name,
		InputSchema: schema.Object(schema.Opt("v", schema.String())),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			return core.OKResult(map[string]any{"tool": name, "args": string(in)})
		},
	}
}

// wrapperTool reaches children and runs body as its handler.
func wrapperTool(name string, body func(ctx context.Context) core.ToolResult, children ...core.Tool) core.Tool {
	return core.Tool{
		Name:           name,
		InputSchema:    schema.Object(),
		ReachableTools: children,
		Execute:        func(ctx context.Context, _ json.RawMessage) core.ToolResult { return body(ctx) },
	}
}

// runWrapper runs one turn in which the model calls the tool named call
// (with id "parent_call_1"), then a final turn. It returns the run's result
// and the scripted provider.
func runWrapper(t *testing.T, call string, mutate func(*core.AgentConfig), tools ...core.Tool) (core.RunResult, *scripted) {
	t.Helper()
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "parent_call_1", call, `{}`)),
	}}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = tools
		if mutate != nil {
			mutate(c)
		}
	})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, s
}

// resultFor is the transcript result for the top-level call id.
func resultFor(t *testing.T, res core.RunResult, id string) core.ToolResultMessage {
	t.Helper()
	for _, m := range res.Messages {
		if r, ok := m.(core.ToolResultMessage); ok && r.ToolUseID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return core.ToolResultMessage{}
}

func resultText(m core.ToolResultMessage) string {
	var b strings.Builder
	for _, c := range m.Content {
		if tb, ok := c.(core.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// TS-07-16: executeBatch attaches a NestedCaller to a wrapper's context, and
// a nested call runs the declared child.
func TestNestedDispatchInjectedForWrapper_TS07_16(t *testing.T) {
	var got []core.ToolResult
	var gotErr error
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		got, gotErr = core.CallNested(ctx, core.ToolUseBlock{ID: "c1", Name: "child", Input: json.RawMessage(`{"v":"x"}`)})
		return core.OKResult(map[string]any{"wrapped": true})
	}, childTool("child"))
	res, _ := runWrapper(t, "wrap", nil, wrap)
	if gotErr != nil || len(got) != 1 || !got[0].OK || got[0].Data["tool"] != "child" || got[0].Data["args"] != `{"v":"x"}` {
		t.Fatalf("CallNested = %+v, %v", got, gotErr)
	}
	if r := resultFor(t, res, "parent_call_1"); r.IsError {
		t.Fatalf("wrapper result = %+v", r)
	}
	// A tool that reaches nothing gets no caller.
	var leafErr error
	leaf := core.Tool{Name: "leaf", InputSchema: schema.Object(), Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		_, leafErr = core.CallNested(ctx, core.ToolUseBlock{Name: "child"})
		return core.OKResult(nil)
	}}
	runWrapper(t, "leaf", nil, leaf, childTool("child"))
	if !errors.Is(leafErr, core.ErrNoNestedCaller) {
		t.Fatalf("leaf CallNested err = %v, want ErrNoNestedCaller", leafErr)
	}
}

// TS-07-17: a name the wrapper does not reach is unknown_tool, inline, with
// no Go error — even when the agent has a tool of that name.
func TestNestedDispatchUnknownTool_TS07_17(t *testing.T) {
	var got []core.ToolResult
	var gotErr error
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		got, gotErr = core.CallNested(ctx,
			core.ToolUseBlock{ID: "c1", Name: "unknown_tool_x"},
			core.ToolUseBlock{ID: "c2", Name: "other"})
		return core.OKResult(nil)
	}, childTool("child1"))
	runWrapper(t, "wrap", nil, wrap, childTool("other"))
	if gotErr != nil || len(got) != 2 {
		t.Fatalf("CallNested = %+v, %v", got, gotErr)
	}
	for _, r := range got {
		if r.OK || r.Error != "unknown_tool" || r.Detail == "" {
			t.Fatalf("result = %+v, want unknown_tool with a detail", r)
		}
	}
}

// TS-07-18: interceptors see the parent of a nested call; a direct call has
// none.
func TestNestedInterceptorParentContext_TS07_18(t *testing.T) {
	var mu sync.Mutex
	before := map[string]core.BeforeToolCallContext{}
	after := map[string]core.AfterToolCallContext{}
	wrap := wrapperTool("parent", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, core.ToolUseBlock{ID: "n1", Name: "child"})
		return core.OKResult(nil)
	}, childTool("child"))
	runWrapper(t, "parent", func(c *core.AgentConfig) {
		c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			mu.Lock()
			before[in.ToolName] = in
			mu.Unlock()
			return core.BeforeToolCallDecision{}
		}
		c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			mu.Lock()
			after[in.ToolName] = in
			mu.Unlock()
			return core.AfterToolCallDecision{}
		}
	}, wrap)
	if d := before["parent"]; d.ParentToolUseID != "" || d.ParentToolName != "" {
		t.Fatalf("direct before = %q %q, want empty", d.ParentToolUseID, d.ParentToolName)
	}
	if d := after["parent"]; d.ParentToolUseID != "" || d.ParentToolName != "" {
		t.Fatalf("direct after = %q %q, want empty", d.ParentToolUseID, d.ParentToolName)
	}
	n, ok := before["child"]
	if !ok || n.ParentToolUseID != "parent_call_1" || n.ParentToolName != "parent" || n.ToolUseID == "" {
		t.Fatalf("nested before = %+v (seen %v)", n, ok)
	}
	if a, ok := after["child"]; !ok || a.ParentToolUseID != "parent_call_1" || a.ParentToolName != "parent" {
		t.Fatalf("nested after = %+v (seen %v)", a, ok)
	}
}

// TS-07-19: a block without a terminate vote is an error result for that
// call; the wrapper carries on.
func TestNestedInterceptorBlock_TS07_19(t *testing.T) {
	var got []core.ToolResult
	var gotErr error
	var ran bool
	child := childTool("child")
	inner := child.Execute
	child.Execute = func(ctx context.Context, in json.RawMessage) core.ToolResult { ran = true; return inner(ctx, in) }
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		got, gotErr = core.CallNested(ctx, core.ToolUseBlock{ID: "n1", Name: "child"})
		return core.OKResult(map[string]any{"recovered": true})
	}, child)
	res, _ := runWrapper(t, "wrap", func(c *core.AgentConfig) {
		c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ParentToolUseID != "" {
				return core.BeforeToolCallDecision{Block: true, Reason: "permission denied"}
			}
			return core.BeforeToolCallDecision{}
		}
	}, wrap)
	if gotErr != nil || len(got) != 1 || got[0].OK || got[0].Error != "blocked_by_policy" || got[0].Detail != "permission denied" {
		t.Fatalf("CallNested = %+v, %v", got, gotErr)
	}
	if ran {
		t.Fatal("the blocked child ran")
	}
	if r := resultFor(t, res, "parent_call_1"); r.IsError || res.StopReason == core.RunStopToolTerminate {
		t.Fatalf("wrapper result %+v, stop %q", r, res.StopReason)
	}
}

// TS-07-20: a block WITH a terminate vote ends the wrapper (ErrTerminated)
// and the run.
func TestNestedInterceptorTerminate_TS07_20(t *testing.T) {
	var gotErr error
	var after bool
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		_, gotErr = core.CallNested(ctx, core.ToolUseBlock{ID: "n1", Name: "child"})
		if gotErr != nil {
			return core.ErrResult("terminated", gotErr.Error())
		}
		after = true
		return core.OKResult(nil)
	}, childTool("child"))
	res, s := runWrapper(t, "wrap", func(c *core.AgentConfig) {
		c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ParentToolUseID != "" {
				return core.BeforeToolCallDecision{Block: true, Terminate: true, Reason: "critical violation"}
			}
			return core.BeforeToolCallDecision{}
		}
	}, wrap)
	if !errors.Is(gotErr, core.ErrTerminated) || after {
		t.Fatalf("CallNested err = %v (continued %v), want ErrTerminated", gotErr, after)
	}
	if res.StopReason != core.RunStopToolTerminate || s.turnsRun() != 1 {
		t.Fatalf("stop = %q after %d turns, want tool_terminate after 1", res.StopReason, s.turnsRun())
	}
}

// TS-07-21: an interceptor's rewritten arguments are what the child gets.
func TestNestedInterceptorRewritesArguments_TS07_21(t *testing.T) {
	var got []core.ToolResult
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		got, _ = core.CallNested(ctx, core.ToolUseBlock{ID: "n1", Name: "child", Input: json.RawMessage(`{"v":"original"}`)})
		return core.OKResult(nil)
	}, childTool("child"))
	runWrapper(t, "wrap", func(c *core.AgentConfig) {
		c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ParentToolUseID != "" {
				return core.BeforeToolCallDecision{Arguments: map[string]any{"v": "rewritten"}}
			}
			return core.BeforeToolCallDecision{}
		}
	}, wrap)
	if len(got) != 1 || !got[0].OK || got[0].Data["args"] != `{"v":"rewritten"}` {
		t.Fatalf("child got %+v", got)
	}
}

// TS-07-22: a nested handler's terminate vote is ignored, annotated on the
// nested result and the wrapper's, and audited; the run goes on.
func TestNestedTerminateVoteIgnored_TS07_22(t *testing.T) {
	child := core.Tool{Name: "child", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			r := core.OKResult(nil)
			r.Terminate, r.Detail = true, "done"
			return r
		}}
	var got []core.ToolResult
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		got, _ = core.CallNested(ctx, core.ToolUseBlock{ID: "n1", Name: "child"})
		r := core.OKResult(nil)
		r.Detail = "wrapper finished"
		return r
	}, child)
	var mu sync.Mutex
	var audits []core.AuditEvent
	var wrapperOut core.ToolResult
	res, s := runWrapper(t, "wrap", func(c *core.AgentConfig) {
		c.Hooks.OnAudit = func(e core.AuditEvent) { mu.Lock(); audits = append(audits, e); mu.Unlock() }
		c.AfterToolCall = func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == "wrap" {
				wrapperOut = in.ToolResult
			}
			return core.AfterToolCallDecision{}
		}
	}, wrap)
	if len(got) != 1 || got[0].Terminate || !strings.Contains(got[0].Detail, "terminate vote ignored") ||
		!strings.Contains(got[0].Detail, "done") {
		t.Fatalf("nested result = %+v", got)
	}
	if wrapperOut.Terminate || !strings.Contains(wrapperOut.Detail, "nested terminate vote ignored") ||
		!strings.Contains(wrapperOut.Detail, "wrapper finished") {
		t.Fatalf("wrapper result = %+v", wrapperOut)
	}
	if res.StopReason == core.RunStopToolTerminate || s.turnsRun() != 2 {
		t.Fatalf("stop %q after %d turns: the ignored vote ended the run", res.StopReason, s.turnsRun())
	}
	var found bool
	for _, a := range audits {
		if a.ToolName == "child" && a.TerminateIgnored && a.ParentToolUseID == "parent_call_1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no nested audit record with TerminateIgnored: %+v", audits)
	}
}

// overlapTool records how many of its calls run at once.
func overlapTool(name string, mode core.ExecutionMode, active, maxActive *atomic.Int32) core.Tool {
	return core.Tool{Name: name, ExecutionMode: mode, InputSchema: schema.Object(schema.Opt("v", schema.String())),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			cur := active.Add(1)
			for {
				old := maxActive.Load()
				if cur <= old || maxActive.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			active.Add(-1)
			return core.OKResult(map[string]any{"args": string(in)})
		}}
}

func callN(name string, n int) []core.ToolUseBlock {
	out := make([]core.ToolUseBlock, n)
	for i := range out {
		out[i] = core.ToolUseBlock{ID: fmt.Sprint(i), Name: name, Input: json.RawMessage(fmt.Sprintf(`{"v":"%d"}`, i))}
	}
	return out
}

// TS-07-23: calls passed together run concurrently.
func TestNestedConcurrency_TS07_23(t *testing.T) {
	var active, maxActive atomic.Int32
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, callN("child", 3)...)
		return core.OKResult(nil)
	}, overlapTool("child", core.Parallel, &active, &maxActive))
	runWrapper(t, "wrap", func(c *core.AgentConfig) { c.ParallelTools = true }, wrap)
	if maxActive.Load() < 2 {
		t.Fatalf("max concurrent nested calls = %d, want more than 1", maxActive.Load())
	}
}

// TS-07-24: ParallelTools off, or a Sequential tool among the calls, runs
// them one at a time.
func TestNestedSequential_TS07_24(t *testing.T) {
	for _, tc := range []struct {
		name     string
		parallel bool
		mode     core.ExecutionMode
	}{
		{"parallel tools off", false, core.Parallel},
		{"sequential tool", true, core.Sequential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var active, maxActive atomic.Int32
			var got []core.ToolResult
			other := childTool("other")
			wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
				calls := append(callN("seq", 2), core.ToolUseBlock{Name: "other"})
				got, _ = core.CallNested(ctx, calls...)
				return core.OKResult(nil)
			}, overlapTool("seq", tc.mode, &active, &maxActive), other)
			runWrapper(t, "wrap", func(c *core.AgentConfig) { c.ParallelTools = tc.parallel }, wrap)
			if maxActive.Load() != 1 || len(got) != 3 || got[0].Data["args"] != `{"v":"0"}` {
				t.Fatalf("max concurrent = %d, results %+v", maxActive.Load(), got)
			}
		})
	}
}

// TS-07-25: whatever order calls finish in, results come back in call order.
func TestNestedConcurrencyPreservesOrder_TS07_25(t *testing.T) {
	r := rand.New(rand.NewSource(25))
	child := core.Tool{Name: "child", InputSchema: schema.Object(schema.Prop("i", schema.Int()), schema.Prop("ms", schema.Int())),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var a struct{ I, Ms int }
			_ = json.Unmarshal(in, &a)
			time.Sleep(time.Duration(a.Ms) * time.Millisecond)
			return core.OKResult(map[string]any{"i": a.I})
		}}
	for iter := range 10 {
		n := 1 + r.Intn(8)
		calls := make([]core.ToolUseBlock, n)
		for i := range calls {
			calls[i] = core.ToolUseBlock{Name: "child", Input: json.RawMessage(fmt.Sprintf(`{"i":%d,"ms":%d}`, i, r.Intn(15)))}
		}
		var got []core.ToolResult
		var gotErr error
		wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
			got, gotErr = core.CallNested(ctx, calls...)
			return core.OKResult(nil)
		}, child)
		runWrapper(t, "wrap", func(c *core.AgentConfig) { c.ParallelTools = true }, wrap)
		if gotErr != nil || len(got) != n {
			t.Fatalf("iteration %d: %d results, %v", iter, len(got), gotErr)
		}
		for i, res := range got {
			if fmt.Sprint(res.Data["i"]) != fmt.Sprint(i) {
				t.Fatalf("iteration %d: result %d is call %v", iter, i, res.Data["i"])
			}
		}
	}
}

// TS-07-26: one call failing — an error result, a panic, invalid arguments
// — does not stop its siblings.
func TestNestedIsolation_TS07_26(t *testing.T) {
	fail := core.Tool{Name: "fail", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.ErrResult("failed", "no") }}
	boom := core.Tool{Name: "boom", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { panic("kaboom") }}
	ok := core.Tool{Name: "ok", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			time.Sleep(10 * time.Millisecond)
			r := core.OKResult(nil)
			r.Detail = "success"
			return r
		}}
	strict := core.Tool{Name: "strict", InputSchema: schema.Object(schema.Prop("n", schema.Int())),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
	var got []core.ToolResult
	var gotErr error
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		got, gotErr = core.CallNested(ctx,
			core.ToolUseBlock{Name: "fail"}, core.ToolUseBlock{Name: "boom"},
			core.ToolUseBlock{Name: "strict", Input: json.RawMessage(`{"n":"x"}`)}, core.ToolUseBlock{Name: "ok"})
		return core.OKResult(nil)
	}, fail, boom, strict, ok)
	runWrapper(t, "wrap", func(c *core.AgentConfig) { c.ParallelTools = true }, wrap)
	if gotErr != nil || len(got) != 4 {
		t.Fatalf("CallNested = %+v, %v", got, gotErr)
	}
	if got[0].OK || got[0].Error != "failed" || got[1].OK || got[1].Error != "panic" ||
		got[2].OK || got[2].Error != "invalid_arguments" || !got[3].OK || got[3].Detail != "success" {
		t.Fatalf("results = %+v", got)
	}
}

// TS-07-27: cancelling the context aborts in-flight nested calls, and calls
// not yet started do not start.
func TestNestedCancel_TS07_27(t *testing.T) {
	var started atomic.Int32
	slow := core.Tool{Name: "slow", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
			started.Add(1)
			<-ctx.Done()
			return core.ErrResult("handler_saw_cancel", "")
		}}
	var got []core.ToolResult
	var gotErr error
	var late []core.ToolResult
	wrap := wrapperTool("wrap", func(ctx context.Context) core.ToolResult {
		cctx, cancel := context.WithCancel(ctx)
		go func() { time.Sleep(20 * time.Millisecond); cancel() }()
		got, gotErr = core.CallNested(cctx, core.ToolUseBlock{Name: "slow"})
		late, _ = core.CallNested(cctx, core.ToolUseBlock{Name: "slow"})
		return core.OKResult(nil)
	}, slow)
	runWrapper(t, "wrap", nil, wrap)
	want := core.ToolResult{OK: false, Error: "aborted", Detail: "Operation aborted"}
	if gotErr != nil || len(got) != 1 || got[0].OK || got[0].Error != want.Error || got[0].Detail != want.Detail {
		t.Fatalf("in-flight result = %+v, %v, want aborted", got, gotErr)
	}
	if len(late) != 1 || late[0].Error != "aborted" || late[0].Detail != "Operation aborted" || started.Load() != 1 {
		t.Fatalf("after cancel: %+v, handler started %d times", late, started.Load())
	}
}
