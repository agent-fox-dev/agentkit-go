package agentkit

// Spec 07 smoke tests: each execution path end to end through the real Agent,
// executeBatch, nestedCaller and interceptors, with only the model scripted.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// TS-07-35 (smoke, 07-PATH-1): a wrapper fans out to two children through
// CallNested; they run concurrently, their events name the wrapper's call, the wrapper gets results in call order, and only the
// wrapper's call reaches the transcript.
func TestSmokeNestedParallelThroughWrapper_TS07_35(t *testing.T) {
	var active, maxActive atomic.Int32
	slowChild := func(name string) core.Tool {
		return core.Tool{Name: name, InputSchema: schema.Object(),
			Execute: func(context.Context, json.RawMessage) core.ToolResult {
				cur := active.Add(1)
				for {
					old := maxActive.Load()
					if cur <= old || maxActive.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(30 * time.Millisecond)
				active.Add(-1)
				return core.OKResult(map[string]any{"from": name})
			}}
	}
	var order []string
	wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		res, err := core.CallNested(ctx, core.ToolUseBlock{Name: "child1"}, core.ToolUseBlock{Name: "child2"})
		if err != nil {
			return core.ErrResult("nested_failed", err.Error())
		}
		for _, r := range res {
			order = append(order, r.Data["from"].(string))
		}
		return core.OKResult(map[string]any{"children": len(res)})
	}, slowChild("child1"), slowChild("child2"))

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "parent_call_1", "wrapper", `{}`)),
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.Tools = []core.Tool{wrapper}
	})
	st, err := a.Stream(context.Background(), "run wrapper")
	if err != nil {
		t.Fatal(err)
	}
	var nestedEvents int
	for e := range st.Events() {
		if ev, ok := e.(core.ToolExecutionEndEvent); ok && ev.ParentToolUseID == "parent_call_1" {
			nestedEvents++
		}
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}

	if maxActive.Load() < 2 {
		t.Fatalf("children ran %d at a time, want concurrently", maxActive.Load())
	}
	if strings.Join(order, ",") != "child1,child2" {
		t.Fatalf("wrapper saw results in order %v", order)
	}
	if nestedEvents != 2 {
		t.Fatalf("%d nested end events, want 2", nestedEvents)
	}
	if r := resultFor(t, res, "parent_call_1"); r.IsError || !strings.Contains(resultText(r), `"children":2`) {
		t.Fatalf("wrapper result = %+v", r)
	}
	for _, m := range a.Messages() {
		switch v := m.(type) {
		case core.AssistantMessage:
			for _, b := range v.Content {
				if tu, ok := b.(core.ToolUseBlock); ok && tu.Name != "wrapper" {
					t.Fatalf("history holds a nested call to %s", tu.Name)
				}
			}
		case core.ToolResultMessage:
			if v.ToolName != "wrapper" {
				t.Fatalf("history holds a nested result from %s", v.ToolName)
			}
		}
	}
	if res.StopReason == core.RunStopToolTerminate || s.turnsRun() != 2 {
		t.Fatalf("run stopped %q after %d turns", res.StopReason, s.turnsRun())
	}
}

// TS-07-36 (smoke, 07-PATH-2): New refuses mutually reachable tools,
// naming the cycle.
func TestSmokeNestedCycleRefused_TS07_36(t *testing.T) {
	_, err := New(agentCfg(cyclicPair()))
	if err == nil || !strings.Contains(err.Error(), "agentkit: reachable tools cycle detected: toolA -> toolB -> toolA") {
		t.Fatalf("New err = %v", err)
	}
}

// TS-07-37 (smoke, 07-PATH-3): an interceptor blocks a nested call and votes
// to terminate; CallNested returns ErrTerminated, the wrapper stops, and the
// run ends on the tool vote without another model turn.
func TestSmokeNestedInterceptorTerminates_TS07_37(t *testing.T) {
	var mu sync.Mutex
	var seen []core.BeforeToolCallContext
	var nestedErr error
	var continued bool
	wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		_, nestedErr = core.CallNested(ctx, core.ToolUseBlock{Name: "child"})
		if errors.Is(nestedErr, core.ErrTerminated) {
			return core.ErrResult("terminated", nestedErr.Error())
		}
		continued = true
		return core.OKResult(nil)
	}, childTool("child"))
	res, s := runWrapper(t, "wrapper", func(c *Config) {
		c.Guard = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			mu.Lock()
			seen = append(seen, in)
			mu.Unlock()
			if in.ParentToolUseID != "" {
				return core.BeforeToolCallDecision{Block: true, Terminate: true, Reason: "abort nested"}
			}
			return core.BeforeToolCallDecision{}
		}
	}, wrapper)
	if !errors.Is(nestedErr, core.ErrTerminated) || continued {
		t.Fatalf("CallNested err = %v, wrapper continued %v", nestedErr, continued)
	}
	if len(seen) != 2 || seen[1].ParentToolUseID != "parent_call_1" || seen[1].ParentToolName != "wrapper" {
		t.Fatalf("interceptor saw %+v", seen)
	}
	if res.StopReason != core.RunStopToolTerminate || s.turnsRun() != 1 {
		t.Fatalf("stop %q after %d turns, want tool_terminate after 1", res.StopReason, s.turnsRun())
	}
}

// TS-07-38 (smoke, 07-PATH-4): a wrapper reaching execute with no Guard is
// refused at construction, before any request to the model.
func TestSmokeNestedUnguardedShellRefused_TS07_38(t *testing.T) {
	s := &scripted{}
	_, err := New(Config{Provider: streamFunc(s.stream), Model: testModelID, Tools: []core.Tool{shellWrapper()}})
	if !errors.Is(err, core.ErrUnguardedExecute) || !strings.Contains(err.Error(), `wrapper "code_mode" reached shell tool "execute"`) {
		t.Fatalf("New err = %v", err)
	}
	if s.turnsRun() != 0 {
		t.Fatalf("%d requests reached the model", s.turnsRun())
	}
}
