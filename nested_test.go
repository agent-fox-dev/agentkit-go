package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func nested(names ...string) []core.ToolUseBlock {
	out := make([]core.ToolUseBlock, len(names))
	for i, n := range names {
		out[i] = core.ToolUseBlock{ID: "same", Name: n}
	}
	return out
}

// child is a tool that answers with its own name and arguments.
func child(name string) core.Tool {
	return tool(name, func(_ context.Context, in json.RawMessage) core.ToolResult {
		return core.OKResult(map[string]any{"tool": name, "args": string(in)})
	})
}

// TestCallNested_RunsTheWrappersToolsOutsideTheTranscript runs a wrapper in an
// agent run: its nested calls run with ids of their own, are seen by the
// interceptors and the stream with their parent, and leave no trace in the
// transcript.
func TestCallNested_RunsTheWrappersToolsOutsideTheTranscript(t *testing.T) {
	var got []core.ToolResult
	var callErr error
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		got, callErr = core.CallNested(ctx, core.ToolUseBlock{ID: "same", Name: "a", Input: json.RawMessage(`{"v":"x"}`)}, core.ToolUseBlock{ID: "same", Name: "b"})
		return core.ToolResult{OK: true, Text: "wrapped"}
	}, child("a"), child("b"))
	var guards guardLog
	var afterParents []string
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		afterParents = append(afterParents, in.ToolName+"<"+in.ParentToolUseID+"/"+in.ParentToolName)
		return core.AfterToolCallDecision{}
	}
	a, _ := scripted(t, Toolset{Tools: []core.Tool{w}, Guard: guards.guard, After: after}, calls(call("p1", "wrap", "{}")), faux.Text("done"))
	evs, res, err := drain(t, context.Background(), a, "go")
	if err != nil || callErr != nil || len(got) != 2 || got[0].Data["args"] != `{"v":"x"}` || got[1].Data["args"] != "{}" {
		t.Fatalf("CallNested = %+v, %v; run %v", got, callErr, err)
	}
	ids := map[string]bool{}
	id := regexp.MustCompile(`^nested_[0-9a-f]{16}$`)
	for _, g := range guards.calls()[1:] {
		if !id.MatchString(g.ToolUseID) || ids[g.ToolUseID] || g.ParentToolUseID != "p1" || g.ParentToolName != "wrap" || g.Assistant == nil || g.Index != len(ids) {
			t.Errorf("the guard saw nested %+v", g)
		}
		ids[g.ToolUseID] = true
	}
	if g := guards.calls()[0]; g.ParentToolUseID != "" || len(ids) != 2 {
		t.Errorf("the guard saw the wrapper with parent %q and %d nested calls", g.ParentToolUseID, len(ids))
	}
	if sort.Strings(afterParents); strings.Join(afterParents, ",") != "a<p1/wrap,b<p1/wrap,wrap</" {
		t.Errorf("After saw %v", afterParents)
	}
	starts, ends, results := executions(evs)
	for nid := range ids {
		if starts[nid] != 1 || ends[nid] != 1 || results[nid] != 0 {
			t.Errorf("nested %s: %d/%d/%d start/end/result events, want 1/1/0", nid, starts[nid], ends[nid], results[nid])
		}
	}
	for _, e := range evs {
		if v, ok := e.(core.ToolExecutionEndEvent); ok && ids[v.ToolUseID] && v.ParentToolUseID != "p1" {
			t.Errorf("a nested end event names parent %q", v.ParentToolUseID)
		}
	}
	if len(res.Messages) != 4 {
		t.Errorf("the transcript holds %d messages, want the prompt, the wrapper's call, its result and the answer", len(res.Messages))
	}
	if m, _ := resultOf(res.Messages, "p1"); m.Content.Text() != "wrapped" {
		t.Errorf("the wrapper's result is %q", m.Content.Text())
	}
}

func TestCallNested_NeedsAToolThatReachesOthers(t *testing.T) {
	var err error
	loner := tool("loner", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		_, err = core.CallNested(ctx, nested("loner")...)
		return core.OKResult(nil)
	})
	mustRunner(t, Toolset{Tools: []core.Tool{loner}}).Call(context.Background(), "loner", nil)
	if !errors.Is(err, core.ErrNoNestedCaller) {
		t.Fatalf("CallNested from a tool reaching nothing = %v, want ErrNoNestedCaller", err)
	}
}

// TestCallNested_AnswersEachFailingCallWithAResultOfItsOwn sends one group of
// calls that fail in every way a nested call can; the siblings still run.
func TestCallNested_AnswersEachFailingCallWithAResultOfItsOwn(t *testing.T) {
	var events eventLog
	var got []core.ToolResult
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		got, _ = core.CallNested(ctx, core.ToolUseBlock{Name: "other"}, core.ToolUseBlock{Name: "fails"}, core.ToolUseBlock{Name: "panics"},
			core.ToolUseBlock{Name: "strict", Input: json.RawMessage(`{"n":"x"}`)}, core.ToolUseBlock{Name: "fails", Input: json.RawMessage(`[1]`)},
			core.ToolUseBlock{Name: "shim"}, core.ToolUseBlock{Name: "denied"}, core.ToolUseBlock{Name: "rewritten", Input: json.RawMessage(`{"v":"a"}`)},
			core.ToolUseBlock{Name: "broken"}, core.ToolUseBlock{Name: "ok"})
		return core.OKResult(nil)
	}, returning("fails", core.ErrResult("failed", "no")), tool("panics", func(context.Context, json.RawMessage) core.ToolResult { panic("kaboom") }),
		core.Tool{Name: "strict", InputSchema: schema.Object(schema.Prop("n", schema.Int())), Execute: child("strict").Execute},
		core.Tool{Name: "shim", InputSchema: schema.Object(), Execute: child("shim").Execute, PrepareArguments: func(map[string]any) map[string]any { panic("shim broke") }},
		child("denied"), child("rewritten"), child("broken"), child("ok"))
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		switch in.ToolName {
		case "denied":
			return core.BeforeToolCallDecision{Block: true}
		case "rewritten":
			return core.BeforeToolCallDecision{Arguments: map[string]any{"v": "b"}}
		case "broken":
			return core.BeforeToolCallDecision{Arguments: map[string]any{"v": math.Inf(1)}}
		}
		return core.BeforeToolCallDecision{}
	}
	mustRunner(t, Toolset{Tools: []core.Tool{w, child("other")}, Guard: guard, Events: events.add}).Call(context.Background(), "wrap", nil)
	want := []struct{ code, detail string }{
		{"unknown_tool", `wrap cannot call "other": no tool named "other" is available in this run; available tools: broken, denied, fails, ok, panics, rewritten, shim, strict`},
		{"failed", "no"}, {"panic", `tool "panics" panicked: kaboom`}, {"invalid_arguments", ""}, {"invalid_arguments", ""},
		{"invalid_arguments", `argument preparation for "shim" panicked: shim broke`}, {core.BlockErrorCode, "blocked by policy"},
		{"", ""}, {"invalid_arguments", "BeforeToolCall returned arguments that are not JSON: "}, {"", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("%d results, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Error != w.code || !strings.HasPrefix(got[i].Detail, w.detail) {
			t.Errorf("call %d = %+v, want %q %q", i, got[i], w.code, w.detail)
		}
	}
	if got[7].Data["args"] != `{"v":"b"}` || !got[9].OK {
		t.Errorf("the rewritten call ran with %v, the last sibling %+v", got[7].Data["args"], got[9])
	}
	starts, ends, _ := executions(events.all())
	if len(starts) != 11 || len(ends) != 11 {
		t.Errorf("%d calls opened and %d closed, want all 11", len(starts), len(ends))
	}
}

func TestCallNested_RunsConcurrentlyUnlessASequentialToolIsCalledAndKeepsCallOrder(t *testing.T) {
	for _, mode := range []core.ExecutionMode{core.Parallel, core.Sequential} {
		m := meet(3, 5*time.Second)
		if mode == core.Sequential {
			m.wait = 20 * time.Millisecond
		}
		var got []core.ToolResult
		w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
			var group []core.ToolUseBlock
			for i, n := range []string{"par", "mode", "par"} {
				group = append(group, core.ToolUseBlock{Name: n, Input: json.RawMessage(fmt.Sprintf(`{"v":"%d"}`, i))})
			}
			got, _ = core.CallNested(ctx, group...)
			return core.OKResult(nil)
		}, attending("par", m, core.Parallel), attending("mode", m, mode))
		mustRunner(t, Toolset{Tools: []core.Tool{w}}).Call(context.Background(), "wrap", nil)
		want := int32(3)
		if mode == core.Sequential {
			want = 1
		}
		if m.peak.Load() != want || len(got) != 3 {
			t.Errorf("%v: %d ran at once, %d results; want %d", mode, m.peak.Load(), len(got), want)
		}
		for i, r := range got {
			if r.Data["args"] != fmt.Sprintf(`{"v":"%d"}`, i) {
				t.Errorf("%v: result %d is %v", mode, i, r.Data["args"])
			}
		}
	}
}

func TestCallNested_AnInterceptorsTerminateVoteEndsTheWrapperAndTheRun(t *testing.T) {
	deny := func(in core.BeforeToolCallContext) bool { return in.ToolName == "deny" }
	yes := true
	for _, c := range []struct {
		name  string
		guard core.BeforeToolCall
		after core.AfterToolCall
		group []string
	}{
		{"a guard that blocks and terminates", blockIf(deny, true), nil, []string{"ok", "deny", "ok"}},
		{"an After that terminates", nil, func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == "deny" {
				return core.AfterToolCallDecision{Terminate: &yes}
			}
			return core.AfterToolCallDecision{}
		}, []string{"deny"}},
		{"a vote in a wrapper nested in the wrapper", blockIf(deny, true), nil, []string{"inner"}},
	} {
		var first, second error
		inner := wrapper("inner", func(ctx context.Context) core.ToolResult {
			_, err := core.CallNested(ctx, nested("deny")...)
			return core.ErrResult("terminated", fmt.Sprint(err))
		}, child("deny"))
		w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
			_, first = core.CallNested(ctx, nested(c.group...)...)
			_, second = core.CallNested(ctx, nested("ok")...)
			return core.OKResult(nil)
		}, child("ok"), child("deny"), inner)
		a, fp := scripted(t, Toolset{Tools: []core.Tool{w}, Guard: c.guard, After: c.after}, calls(call("p1", "wrap", "{}")), faux.Text("never"))
		evs, res, err := drain(t, context.Background(), a, "go")
		if !errors.Is(first, core.ErrTerminated) || !errors.Is(second, core.ErrTerminated) {
			t.Errorf("%s: CallNested = %v, then %v; want ErrTerminated", c.name, first, second)
		}
		if err != nil || res.StopReason != core.RunStopToolTerminate || fp.Calls() != 1 {
			t.Errorf("%s: the run stopped %q after %d requests, %v", c.name, res.StopReason, fp.Calls(), err)
		}
		starts, ends, _ := executions(evs)
		for id, n := range starts {
			if n != 1 || ends[id] != 1 {
				t.Errorf("%s: call %s opened %d and closed %d times", c.name, id, n, ends[id])
			}
		}
	}
}

func TestCallNested_DropsAHandlersTerminateVoteAndSaysSo(t *testing.T) {
	var deep, top []core.ToolResult
	stop := returning("stop", core.ToolResult{OK: true, Terminate: true, Detail: "done"})
	inner := wrapper("inner", func(ctx context.Context) core.ToolResult {
		deep, _ = core.CallNested(ctx, nested("stop")...)
		return core.OKResult(nil)
	}, stop)
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		top, _ = core.CallNested(ctx, nested("inner", "stop")...)
		return core.ToolResult{OK: true, Detail: "wrapper finished"}
	}, inner, stop)
	res := mustRunner(t, Toolset{Tools: []core.Tool{w}}).Call(context.Background(), "wrap", nil)
	if len(deep) != 1 || deep[0].Terminate || deep[0].Detail != "done; terminate vote ignored: stop was called through inner" {
		t.Errorf("the vote two levels down = %+v", deep)
	}
	if len(top) != 2 || top[0].Detail != "nested terminate vote ignored" || top[1].Terminate ||
		top[1].Detail != "done; terminate vote ignored: stop was called through wrap" {
		t.Errorf("the wrapper's nested results = %+v", top)
	}
	if res.Terminate || res.Detail != "wrapper finished; nested terminate vote ignored" {
		t.Errorf("the wrapper's result = %+v", res)
	}
}

func TestCallNested_AbortsUnderACancelledContext(t *testing.T) {
	var ran atomic.Int32
	var before, during []core.ToolResult
	slow := tool("slow", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		ran.Add(1)
		<-ctx.Done()
		return core.OKResult(nil)
	})
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		cctx, cancel := context.WithCancel(ctx)
		go func() { testutil.Within(5*time.Second, func() bool { return ran.Load() > 0 }); cancel() }()
		during, _ = core.CallNested(cctx, nested("slow")...)
		before, _ = core.CallNested(cctx, nested("slow")...)
		return core.OKResult(nil)
	}, slow)
	mustRunner(t, Toolset{Tools: []core.Tool{w}}).Call(context.Background(), "wrap", nil)
	for _, got := range [][]core.ToolResult{during, before} {
		if len(got) != 1 || got[0].Error != "aborted" || got[0].Detail != "Operation aborted" {
			t.Errorf("result %+v, want aborted", got)
		}
	}
	if ran.Load() != 1 {
		t.Errorf("the handler started %d times; a call made after the cancel must not start", ran.Load())
	}
}

func TestCallNested_ReportsUsageToTheAgentAtOnce(t *testing.T) {
	var a *Agent
	var during int64
	spender := tool("spend", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		core.ReportUsage(ctx, core.Usage{InputTokens: 100})
		during = a.Usage().InputTokens
		return core.OKResult(nil)
	})
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, nested("spend")...)
		return core.OKResult(nil)
	}, spender)
	a, _ = scripted(t, Toolset{Tools: []core.Tool{w}}, calls(call("p1", "wrap", "{}")))
	res, err := a.Run(context.Background(), "go")
	if err != nil || during != 100 || res.Usage.InputTokens != 100 {
		t.Fatalf("usage during the call %d, of the run %d, %v; want 100 both", during, res.Usage.InputTokens, err)
	}
}

func TestCallNested_AnAfterHookMayCallNested(t *testing.T) {
	var reentered []core.ToolResult
	after := func(ctx context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		if in.ParentToolUseID != "" {
			reentered, _ = core.CallNested(ctx, nested("missing")...)
		}
		return core.AfterToolCallDecision{}
	}
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, nested("a")...)
		return core.OKResult(nil)
	}, child("a"))
	done := make(chan struct{})
	go func() {
		mustRunner(t, Toolset{Tools: []core.Tool{w}, After: after}).Call(context.Background(), "wrap", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a nested call made from After deadlocked")
	}
	if len(reentered) != 1 {
		t.Errorf("the re-entrant call returned %+v", reentered)
	}
}
