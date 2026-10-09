package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/session"
	"github.com/agentfox/agentkit-go/stop"
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

// fanOut is a wrapper that calls child twice with one CallNested.
func fanOut(child core.Tool) core.Tool {
	return wrapperTool("parent", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, core.ToolUseBlock{ID: "same", Name: child.Name}, core.ToolUseBlock{ID: "same", Name: child.Name})
		return core.OKResult(nil)
	}, child)
}

// TS-07-28: every nested call gets a unique, non-empty id of its own — not
// the wrapper's, which may repeat.
func TestNestedEventsUniqueToolUseIDs_TS07_28(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	for range 5 {
		runWrapper(t, "parent", func(c *core.AgentConfig) {
			c.ParallelTools = true
			c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
				if in.ParentToolUseID == "" {
					return core.BeforeToolCallDecision{}
				}
				mu.Lock()
				defer mu.Unlock()
				if in.ToolUseID == "" || in.ToolUseID == "same" || seen[in.ToolUseID] {
					t.Errorf("nested id %q is empty, the wrapper's, or repeated", in.ToolUseID)
				}
				seen[in.ToolUseID] = true
				return core.BeforeToolCallDecision{}
			}
		}, fanOut(childTool("child")))
	}
	if len(seen) != 10 {
		t.Fatalf("%d distinct nested ids, want 10", len(seen))
	}
}

// TS-07-29: nested calls open and close with execution events carrying
// their parent; they emit no ToolResultEvent; the JSON omits an empty parent.
func TestNestedEvents_TS07_29(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "parent_call_1", "parent", `{}`)),
	}}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{fanOut(childTool("child"))}
	})
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var starts, ends, results int
	var nestedJSON string
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.ParentToolUseID == "parent_call_1" && ev.Name == "child" {
				starts++
				b, err := session.EventJSON(ev)
				if err != nil {
					t.Fatal(err)
				}
				nestedJSON = string(b)
			}
		case core.ToolExecutionEndEvent:
			if ev.ParentToolUseID == "parent_call_1" && ev.Name == "child" {
				ends++
			}
		case core.ToolResultEvent:
			if ev.Message.ToolName == "child" {
				t.Fatalf("a nested call emitted a ToolResultEvent: %+v", ev)
			}
			results++
		}
	}
	if starts != 2 || ends != 2 || results != 1 {
		t.Fatalf("nested starts %d, ends %d, top-level results %d; want 2, 2, 1", starts, ends, results)
	}
	if !strings.Contains(nestedJSON, `"parent_tool_use_id":"parent_call_1"`) {
		t.Fatalf("nested start event JSON = %s", nestedJSON)
	}
	for _, e := range []core.Event{core.ToolExecutionStartEvent{ToolUseID: "p1"},
		core.ToolExecutionUpdateEvent{ToolUseID: "p1"}, core.ToolExecutionEndEvent{ToolUseID: "p1"}} {
		b, err := session.EventJSON(e)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "parent_tool_use_id") {
			t.Fatalf("an empty parent is serialized: %s", b)
		}
	}
	b, err := session.EventJSON(core.ToolExecutionUpdateEvent{ToolUseID: "c", ParentToolUseID: "p"})
	if err != nil || !strings.Contains(string(b), `"parent_tool_use_id":"p"`) {
		t.Fatalf("update event JSON = %s, %v", b, err)
	}
}

// TS-07-30: a nested call's audit record links it to its parent.
func TestNestedAudit_TS07_30(t *testing.T) {
	slow := core.Tool{Name: "child", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			time.Sleep(5 * time.Millisecond)
			return core.ErrResult("child_failed", "x")
		}}
	var mu sync.Mutex
	var audits []core.AuditEvent
	runWrapper(t, "parent", func(c *core.AgentConfig) {
		c.Hooks.OnAudit = func(e core.AuditEvent) { mu.Lock(); audits = append(audits, e); mu.Unlock() }
	}, fanOut(slow))
	var nested []core.AuditEvent
	for _, a := range audits {
		if a.ParentToolUseID != "" {
			nested = append(nested, a)
		}
	}
	if len(nested) != 2 {
		t.Fatalf("%d nested audit records, want 2: %+v", len(nested), audits)
	}
	for _, a := range nested {
		if a.ParentToolUseID != "parent_call_1" || a.Kind != core.AuditToolCall || a.ToolName != "child" ||
			a.ArgumentsHash == "" || !a.IsError || a.ErrorCode != "child_failed" || a.ElapsedMS <= 0 || a.ToolUseID == "" {
			t.Fatalf("nested audit = %+v", a)
		}
	}
}

// namedSpanRecorder keeps every span's name and attributes.
type namedSpanRecorder struct {
	mu    sync.Mutex
	spans []map[string]any
	names []string
}

func (r *namedSpanRecorder) StartSpan(name string, fn func(core.Span) error) error {
	sp := &namedSpan{attrs: map[string]any{}}
	err := fn(sp)
	r.mu.Lock()
	r.names = append(r.names, name)
	r.spans = append(r.spans, sp.attrs)
	r.mu.Unlock()
	return err
}

type namedSpan struct{ attrs map[string]any }

func (s *namedSpan) SetAttributes(kv map[string]any) {
	for k, v := range kv {
		s.attrs[k] = v
	}
}
func (s *namedSpan) SetStatus(err error) {
	if err != nil {
		s.attrs["status"] = err.Error()
	}
}
func (s *namedSpan) AddEvent(string, map[string]any) {}
func (s *namedSpan) End()                            {}

// TS-07-31: each nested handler runs in an agentkit.tool_call span that
// records its parent.
func TestNestedTracing_TS07_31(t *testing.T) {
	rec := &namedSpanRecorder{}
	runWrapper(t, "parent", func(c *core.AgentConfig) { c.Tracer = rec }, fanOut(childTool("child")))
	nested := 0
	for i, attrs := range rec.spans {
		if rec.names[i] != "agentkit.tool_call" {
			continue
		}
		if attrs["parent_tool_use_id"] == "parent_call_1" {
			nested++
			if attrs["tool_name"] != "child" || attrs["tool_use_id"] == "" || attrs["is_error"] != false {
				t.Fatalf("nested span = %+v", attrs)
			}
		}
	}
	if nested != 2 {
		t.Fatalf("%d nested spans, want 2: %+v", nested, rec.spans)
	}
}

// TS-07-32: usage a nested handler reports reaches the agent at once.
func TestNestedUsage_TS07_32(t *testing.T) {
	var during int64
	var a *Agent
	spender := core.Tool{Name: "child", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, _ json.RawMessage) core.ToolResult {
			core.ReportUsage(ctx, core.Usage{InputTokens: 100, Set: core.UsageInputTokens})
			during = a.Usage().InputTokens
			return core.OKResult(nil)
		}}
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "parent_call_1", "parent", `{}`)),
	}}
	a = newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{wrapperTool("parent", func(ctx context.Context) core.ToolResult {
			_, _ = core.CallNested(ctx, core.ToolUseBlock{Name: "child"})
			return core.OKResult(nil)
		}, spender)}
	})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if during < 100 || a.Usage().InputTokens < 100 {
		t.Fatalf("usage during the call %d, after %d; want at least 100", during, a.Usage().InputTokens)
	}
}

// TS-07-33: the transcript and the session log hold the wrapper's call and
// result, and nothing of its nested calls.
func TestNestedHistory_TS07_33(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, _ := openTestSession(t, path)
	defer store.Close()
	res, _ := runWrapper(t, "parent", func(c *core.AgentConfig) { c.SessionStore = store },
		fanOut(childTool("nested_child_tool")))
	_ = res
	var wrapperCalls, wrapperResults int
	check := func(where string, msgs core.Messages) {
		for _, m := range msgs {
			switch v := m.(type) {
			case core.AssistantMessage:
				for _, b := range v.Content {
					if tu, ok := b.(core.ToolUseBlock); ok {
						if tu.Name == "nested_child_tool" {
							t.Fatalf("%s: a nested call is in the transcript", where)
						}
						wrapperCalls++
					}
				}
			case core.ToolResultMessage:
				if v.ToolName == "nested_child_tool" {
					t.Fatalf("%s: a nested result is in the transcript", where)
				}
				wrapperResults++
			}
		}
	}
	check("history", res.Messages)
	if wrapperCalls != 1 || wrapperResults != 1 {
		t.Fatalf("history has %d calls and %d results, want the wrapper's 1 and 1", wrapperCalls, wrapperResults)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "nested_child_tool") {
		t.Fatal("the session log records a nested call")
	}
	if !strings.Contains(string(raw), "parent_call_1") {
		t.Fatal("the session log does not record the wrapper call")
	}
}

// TS-07-34: stop.WhenToolCalled sees top-level calls only.
func TestNestedStopPolicy_TS07_34(t *testing.T) {
	res, s := runWrapper(t, "parent", func(c *core.AgentConfig) {
		c.StopPolicy = stop.Any(stop.WhenToolCalled("child_tool"), stop.AfterTurns(10))
	}, fanOut(childTool("child_tool")))
	if res.StopReason == core.RunStopPolicy || s.turnsRun() != 2 {
		t.Fatalf("stop %q after %d turns: a nested call tripped the stop policy", res.StopReason, s.turnsRun())
	}
	// The policy itself works on the top-level call.
	res, s = runWrapper(t, "parent", func(c *core.AgentConfig) {
		c.StopPolicy = stop.Any(stop.WhenToolCalled("parent"), stop.AfterTurns(10))
	}, fanOut(childTool("child_tool")))
	if res.StopReason != core.RunStopPolicy || s.turnsRun() != 1 {
		t.Fatalf("stop %q after %d turns, want the policy to stop after 1", res.StopReason, s.turnsRun())
	}
}

// A block-and-terminate still closes the blocked call on the stream and
// audits it, and closes the calls already queued behind it: every nested
// call that opened closes exactly once, as a direct one does.
func TestNestedTerminateClosesEveryOpenedCall(t *testing.T) {
	var mu sync.Mutex
	var audits []core.AuditEvent
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "parent_call_1", "parent", `{}`)),
	}}
	wrap := wrapperTool("parent", func(ctx context.Context) core.ToolResult {
		_, err := core.CallNested(ctx, core.ToolUseBlock{Name: "ok"}, core.ToolUseBlock{Name: "deny"}, core.ToolUseBlock{Name: "ok"})
		return core.ErrResult("terminated", fmt.Sprint(err))
	}, childTool("ok"), childTool("deny"))
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{wrap}
		c.Hooks.OnAudit = func(e core.AuditEvent) { mu.Lock(); audits = append(audits, e); mu.Unlock() }
		c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ToolName == "deny" {
				return core.BeforeToolCallDecision{Block: true, Terminate: true, Reason: "no"}
			}
			return core.BeforeToolCallDecision{}
		}
	})
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	open := map[string]int{}
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.ParentToolUseID != "" {
				open[ev.ToolUseID]++
			}
		case core.ToolExecutionEndEvent:
			if ev.ParentToolUseID != "" {
				open[ev.ToolUseID]--
			}
		}
	}
	if len(open) != 2 {
		t.Fatalf("%d nested calls opened, want 2 (the queued ok and the blocked deny)", len(open))
	}
	for id, n := range open {
		if n != 0 {
			t.Fatalf("nested call %s opened and closed unevenly (%d)", id, n)
		}
	}
	var blocked bool
	nested := 0
	for _, e := range audits {
		if e.ParentToolUseID == "" {
			continue
		}
		nested++
		if e.ToolName == "deny" && e.ErrorCode == "blocked_by_policy" {
			blocked = true
		}
	}
	if !blocked || nested != 2 {
		t.Fatalf("nested audits = %d (blocked deny recorded %v): %+v", nested, blocked, audits)
	}
}

// An AfterToolCall that itself calls CallNested on the context it is given
// must not deadlock the dispatcher.
func TestNestedAfterToolCallMayCallNested(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wrap := wrapperTool("parent", func(ctx context.Context) core.ToolResult {
			_, _ = core.CallNested(ctx, core.ToolUseBlock{Name: "child"})
			return core.OKResult(nil)
		}, childTool("child"))
		runWrapper(t, "parent", func(c *core.AgentConfig) {
			c.AfterToolCall = func(ctx context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
				if in.ParentToolUseID != "" {
					_, _ = core.CallNested(ctx, core.ToolUseBlock{Name: "nothing"})
				}
				return core.AfterToolCallDecision{}
			}
		}, wrap)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked")
	}
}
