package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestRun_AnyVoteInTheBatchEndsTheRunOnceTheBatchIsDone(t *testing.T) {
	for _, c := range []struct {
		name, first string
		block, stop bool
		want        core.RunStopReason
	}{
		{"a tool's vote", "voter", false, false, core.RunStopToolTerminate},
		{"a guard that blocks and terminates", "quiet", true, true, core.RunStopToolTerminate},
		{"a guard that only blocks", "voter", true, false, core.RunStopEndTurn},
	} {
		var firstDone, siblingDone atomic.Bool // the sibling finishes only after the first call is done
		vote := tool("voter", func(context.Context, json.RawMessage) core.ToolResult {
			firstDone.Store(true)
			return core.ToolResult{OK: true, Terminate: true}
		})
		slow := tool("slow", func(context.Context, json.RawMessage) core.ToolResult {
			testutil.Within(5*time.Second, firstDone.Load)
			siblingDone.Store(true)
			return core.OKResult(nil)
		})
		guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if c.block && in.ToolUseID == "c1" {
				firstDone.Store(true)
				return core.BeforeToolCallDecision{Block: true, Terminate: c.stop}
			}
			return core.BeforeToolCallDecision{}
		}
		a, fp := scripted(t, Toolset{Tools: []core.Tool{vote, echo("quiet", nil), slow}, Guard: guard},
			calls(call("c1", c.first, "{}"), call("c2", "slow", "{}")), faux.Text("done"))
		res, err := a.Run(context.Background(), "go")
		_, answered := resultOf(res.Messages, "c2")
		if err != nil || res.StopReason != c.want || !siblingDone.Load() || !answered || (c.want == core.RunStopToolTerminate) != (fp.Calls() == 1) {
			t.Errorf("%s: %q after %d requests, sibling done %v, %v", c.name, res.StopReason, fp.Calls(), siblingDone.Load(), err)
		}
	}
}

// TestBatch_TurnsEveryFailureIntoAResult runs one batch of every way a call can
// go wrong through a Runner and through an agent run: each is a result, the
// panics are reported, and both paths give the model the same text.
func TestBatch_TurnsEveryFailureIntoAResult(t *testing.T) {
	ok := func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
	ts := func(events func(core.Event)) Toolset {
		return Toolset{Tools: []core.Tool{
			{Name: "fails", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("it broke") }},
			{Name: "panics", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { panic("kaput") }},
			{Name: "prepare_panics", InputSchema: schema.Object(), Handler: ok, PrepareArguments: func(map[string]any) map[string]any { panic("shim broke") }},
			{Name: "guard_panics", InputSchema: schema.Object(), Handler: ok},
			{Name: "guard_rewrites", InputSchema: schema.Object(schema.Opt("v", schema.Number())), Handler: func(_ context.Context, in json.RawMessage) (json.RawMessage, error) { return in, nil }},
			{Name: "guard_breaks", InputSchema: schema.Object(schema.Opt("v", schema.Number())), Handler: ok},
			{Name: "after_panics", InputSchema: schema.Object(), Handler: ok},
			{Name: "scalar", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`42`), nil }},
		}, Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			switch in.ToolName {
			case "guard_panics":
				panic("guard broke")
			case "guard_rewrites":
				return core.BeforeToolCallDecision{Arguments: map[string]any{"v": 7}}
			case "guard_breaks":
				return core.BeforeToolCallDecision{Arguments: map[string]any{"v": math.NaN()}}
			}
			return core.BeforeToolCallDecision{}
		}, After: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == "after_panics" {
				panic("after broke")
			}
			return core.AfterToolCallDecision{}
		}, Events: events}
	}
	names := []string{"fails", "panics", "prepare_panics", "guard_panics", "guard_rewrites", "guard_breaks", "after_panics", "scalar"}
	var batch []core.ToolUseBlock
	var blocks []core.ContentBlock
	for i, n := range names {
		batch = append(batch, use(t, fmt.Sprint("c", i), n, `{}`))
		blocks = append(blocks, call(fmt.Sprint("c", i), n, `{}`))
	}
	var events eventLog
	res := mustRunner(t, ts(events.Add)).Batch(context.Background(), batch...)
	want := []struct{ code, text string }{
		{"handler_error", "it broke"}, {"panic", `tool \"panics\" panicked: kaput`},
		{"invalid_arguments", `argument preparation for \"prepare_panics\" panicked: shim broke`},
		{core.BlockErrorCode, "interceptor panicked: guard broke"}, {"", `"v":7`},
		{"invalid_arguments", "BeforeToolCall returned arguments that are not JSON"}, {"", `"ok":true`}, {"", `"result":42`},
	}
	for i, w := range want {
		if res[i].Error != w.code || !strings.Contains(res[i].LLMText(), w.text) {
			t.Errorf("%s = %+v (%s); want %q carrying %q", names[i], res[i], res[i].LLMText(), w.code, w.text)
		}
	}
	var reports []string
	for _, e := range events.All() {
		if ev, ok := e.(core.ErrorEvent); ok {
			reports = append(reports, ev.Message)
		}
	}
	if len(reports) != 4 {
		t.Errorf("error events %q, want one for each panic in a handler, a shim, the guard and After", reports)
	}
	a, _ := scripted(t, ts(nil), calls(blocks...), faux.Text("done"))
	run, err := a.Run(context.Background(), "go")
	if err != nil || run.StopReason != core.RunStopEndTurn {
		t.Fatalf("Run = %q, %v; no failure of a call ends the run", run.StopReason, err)
	}
	for i, c := range batch {
		if m, _ := resultOf(run.Messages, c.ID); m.Content.Text() != res[i].LLMText() || m.IsError != !res[i].OK {
			t.Errorf("%s: the run recorded %q, Batch returned %q", c.Name, m.Content.Text(), res[i].LLMText())
		}
	}
}

func TestBatch_AfterSeesTheResultAndItsEditsReachTheMessageNotTheResult(t *testing.T) {
	two := 2
	md := &core.ToolMetadata{ExitCode: &two, Outcome: "exit"}
	probe := returning("probe", core.ToolResult{Error: "command_exit", Text: "x", Data: map[string]any{"k": 1}, Metadata: md})
	var seen core.AfterToolCallContext
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		seen = in
		in.Result.Content = core.Content{core.TextBlock{Text: "edited"}}
		in.Result.Metadata.Outcome = "edited"
		return core.AfterToolCallDecision{}
	}
	var events eventLog
	res := mustRunner(t, Toolset{Tools: []core.Tool{probe}, After: after, Events: events.Add}).Call(context.Background(), "probe", nil)
	if seen.ToolResult.Error != "command_exit" || seen.ToolResult.Metadata != md || seen.Result.Metadata == md || seen.Elapsed < 0 {
		t.Errorf("After saw %+v; want the handler's result and a message holding a copy of its metadata", seen)
	}
	if res.LLMText() != "x" || md.Outcome != "exit" {
		t.Errorf("the edit reached the returned result (%q) or the handler's metadata (%q)", res.LLMText(), md.Outcome)
	}
	for _, e := range events.All() {
		if ev, ok := e.(core.ToolResultEvent); ok && (ev.Message.Content.Text() != "edited" || ev.Message.Metadata.Outcome != "edited") {
			t.Errorf("the result event carries %+v, want the edit", ev.Message)
		}
	}
	a, _ := scripted(t, Toolset{Tools: []core.Tool{probe}, After: after}, calls(call("c1", "probe", "{}")), faux.Text("done"))
	run, _ := a.Run(context.Background(), "go")
	if m, _ := resultOf(run.Messages, "c1"); m.Content.Text() != "edited" {
		t.Errorf("the transcript holds %q, want the edit", m.Content.Text())
	}
}
