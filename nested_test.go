package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

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
	a, _ := scripted(t, Toolset{Tools: []core.Tool{w}, Guard: guards.Guard, After: after}, calls(call("p1", "wrap", "{}")), faux.Text("done"))
	evs, res, err := drain(t, context.Background(), a, "go")
	if err != nil || callErr != nil || len(got) != 2 || got[0].Data["args"] != `{"v":"x"}` || got[1].Data["args"] != "{}" {
		t.Fatalf("CallNested = %+v, %v; run %v", got, callErr, err)
	}
	ids := map[string]bool{}
	id := regexp.MustCompile(`^nested_[0-9a-f]{16}$`)
	for _, g := range guards.Calls()[1:] {
		if !id.MatchString(g.ToolUseID) || ids[g.ToolUseID] || g.ParentToolUseID != "p1" || g.ParentToolName != "wrap" || g.Assistant == nil || g.Index != len(ids) {
			t.Errorf("the guard saw nested %+v", g)
		}
		ids[g.ToolUseID] = true
	}
	if g := guards.Calls()[0]; g.ParentToolUseID != "" || len(ids) != 2 {
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
