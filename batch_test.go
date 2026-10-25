package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestBatch_PreparesInCallOrderBeforeAnyHandler(t *testing.T) {
	var events eventLog
	var log []string // appended by the prepare phase and the handlers; the claim is that they do not interleave
	var handlers atomic.Int32
	h := func(context.Context, json.RawMessage) core.ToolResult { handlers.Add(1); return core.OKResult(nil) }
	strict := core.Tool{Name: "strict", InputSchema: schema.Object(schema.Prop("text", schema.String())), Execute: h}
	guards := guardLog{decide: func(in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		log = append(log, fmt.Sprintf("guard %s handlers=%d", in.ToolUseID, handlers.Load()))
		return core.BeforeToolCallDecision{Block: in.ToolName == "blocked", Reason: "no"}
	}}
	r := mustRunner(t, Toolset{Tools: []core.Tool{tool("ok", h), strict, tool("blocked", h)}, Guard: guards.guard, Events: events.add})
	res := r.Batch(context.Background(), use(t, "c1", "ok", `{}`), use(t, "c2", "missing", `{}`),
		use(t, "c3", "strict", `{}`), use(t, "c4", "blocked", `{}`), use(t, "c5", "ok", `{}`))
	var codes []string
	for _, x := range res {
		codes = append(codes, x.Error)
	}
	if want := []string{"", "unknown_tool", "invalid_arguments", core.BlockErrorCode, ""}; !slices.Equal(codes, want) {
		t.Errorf("codes %v, want %v", codes, want)
	}
	if want := []string{"guard c1 handlers=0", "guard c4 handlers=0", "guard c5 handlers=0"}; !slices.Equal(log, want) {
		t.Errorf("guard calls %v, want %v: in order, unknown and invalid calls skipped, before any handler", log, want)
	}
	var starts []string
	for _, e := range events.all() {
		if s, ok := e.(core.ToolExecutionStartEvent); ok {
			starts = append(starts, s.ToolUseID)
		}
	}
	if !slices.Equal(starts, []string{"c1", "c2", "c3", "c4", "c5"}) {
		t.Errorf("start events %v, want call order", starts)
	}
}

func TestBatch_ADoneContextAbortsEveryCallNotYetFinalized(t *testing.T) {
	for _, cancelAt := range []string{"before the batch", "c2"} {
		var ran atomic.Int32
		var events eventLog
		ctx, cancel := context.WithCancel(context.Background())
		if cancelAt == "before the batch" {
			cancel()
		}
		guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ToolUseID == cancelAt {
				cancel()
			}
			return core.BeforeToolCallDecision{Block: in.ToolUseID == "c1", Terminate: true}
		}
		r := mustRunner(t, Toolset{Tools: []core.Tool{echo("echo", &ran)}, Guard: guard, Events: events.add})
		res := r.Batch(ctx, use(t, "c1", "echo", `{}`), use(t, "c2", "echo", `{}`), use(t, "c3", "echo", `{}`))
		cancel()
		starts, ends, results := executions(events.all())
		for i, id := range []string{"c1", "c2", "c3"} {
			want := core.ErrResult("aborted", "Operation aborted")
			if i == 0 && cancelAt != "before the batch" {
				want = core.ToolResult{Error: core.BlockErrorCode, Detail: "blocked by policy", Terminate: true}
			}
			if res[i].Error != want.Error || res[i].Detail != want.Detail || res[i].Terminate != want.Terminate || starts[id] != 1 || ends[id] != 1 || results[id] != 1 {
				t.Errorf("cancelled %s: %s = %+v with %d/%d/%d events; want %+v, one of each", cancelAt, id, res[i], starts[id], ends[id], results[id], want)
			}
		}
		if ran.Load() != 0 {
			t.Errorf("cancelled %s: %d handlers ran", cancelAt, ran.Load())
		}
	}
}

func TestBatch_RunsHandlersConcurrentlyUnlessASequentialToolIsCalled(t *testing.T) {
	for _, c := range []struct {
		name  string
		modes []core.ExecutionMode
		peak  int32
	}{
		{"all parallel", []core.ExecutionMode{core.Parallel, core.Parallel, core.Parallel}, 3},
		{"one sequential", []core.ExecutionMode{core.Parallel, core.Sequential, core.Parallel}, 1},
	} {
		m := meet(3, 5*time.Second)
		if c.peak == 1 {
			m.wait = 20 * time.Millisecond
		}
		var ts Toolset
		var batch []core.ToolUseBlock
		for i, mode := range c.modes {
			name := fmt.Sprint("t", i)
			ts.Tools = append(ts.Tools, attending(name, m, mode))
			batch = append(batch, use(t, fmt.Sprint("c", i), name, fmt.Sprintf(`{"v":"%d"}`, i)))
		}
		res := mustRunner(t, ts).Batch(context.Background(), batch...)
		if m.peak.Load() != c.peak {
			t.Errorf("%s: %d handlers ran at once, want %d", c.name, m.peak.Load(), c.peak)
		}
		for i, x := range res {
			if x.Data["args"] != fmt.Sprintf(`{"v":"%d"}`, i) {
				t.Errorf("%s: result %d belongs to %v", c.name, i, x.Data["args"])
			}
		}
	}
}

func TestBatch_FinalizesOneCallAtATimeAndReturnsResultsInCallOrder(t *testing.T) {
	var inAfter, overlaps, finalized atomic.Int32
	var order []string
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		if inAfter.Add(1) > 1 {
			overlaps.Add(1)
		}
		order = append(order, in.ToolUseID) // safe only because finalization is serialized
		testutil.Within(10*time.Millisecond, func() bool { return inAfter.Load() > 1 })
		inAfter.Add(-1)
		finalized.Add(1)
		return core.AfterToolCallDecision{}
	}
	last := tool("last", func(context.Context, json.RawMessage) core.ToolResult {
		testutil.Within(5*time.Second, func() bool { return finalized.Load() == 2 })
		return core.OKResult(map[string]any{"n": "last"})
	})
	r := mustRunner(t, Toolset{Tools: []core.Tool{last, echo("echo", nil)}, After: after})
	res := r.Batch(context.Background(), use(t, "c0", "last", `{}`), use(t, "c1", "echo", `{}`), use(t, "c2", "echo", `{}`))
	if overlaps.Load() != 0 || len(order) != 3 || order[2] != "c0" {
		t.Errorf("After ran %v with %d overlaps; want serialized, the slow call last", order, overlaps.Load())
	}
	if res[0].Data["n"] != "last" || res[1].Data["echoed"] != true || res[2].Data["echoed"] != true {
		t.Errorf("results %+v are not in call order", res)
	}
}

func TestBatch_ResultsTerminateIsTheCallsFinalVote(t *testing.T) {
	vote := func(b bool) core.AfterToolCall {
		return func(context.Context, core.AfterToolCallContext) core.AfterToolCallDecision {
			return core.AfterToolCallDecision{Terminate: &b}
		}
	}
	quiet := echo("quiet", nil)
	for _, c := range []struct {
		name string
		ts   Toolset
		call string
		want bool
	}{
		{"the handler's vote stands", Toolset{Tools: []core.Tool{voter("voter")}, After: noopAfter}, "voter", true},
		{"After replaces the handler's vote", Toolset{Tools: []core.Tool{voter("voter")}, After: vote(false)}, "voter", false},
		{"After votes for a quiet handler", Toolset{Tools: []core.Tool{quiet}, After: vote(true)}, "quiet", true},
		{"a blocked call takes the guard's vote", Toolset{Tools: []core.Tool{quiet}, Guard: blockIf(func(core.BeforeToolCallContext) bool { return true }, true)}, "quiet", true},
		{"a blocked call casts none of its own", Toolset{Tools: []core.Tool{voter("voter")}, Guard: blockIf(func(core.BeforeToolCallContext) bool { return true }, false)}, "voter", false},
		{"an invalid call casts none", Toolset{Tools: []core.Tool{{Name: "voter", InputSchema: schema.Object(schema.Prop("n", schema.Int())), Execute: voter("v").Execute}}}, "voter", false},
	} {
		if got := mustRunner(t, c.ts).Call(context.Background(), c.call, nil).Terminate; got != c.want {
			t.Errorf("%s: Terminate = %v, want %v", c.name, got, c.want)
		}
	}
}

func noopAfter(context.Context, core.AfterToolCallContext) core.AfterToolCallDecision {
	return core.AfterToolCallDecision{}
}

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
	res := mustRunner(t, ts(events.add)).Batch(context.Background(), batch...)
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
	for _, e := range events.all() {
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
	res := mustRunner(t, Toolset{Tools: []core.Tool{probe}, After: after, Events: events.add}).Call(context.Background(), "probe", nil)
	if seen.ToolResult.Error != "command_exit" || seen.ToolResult.Metadata != md || seen.Result.Metadata == md || seen.Elapsed < 0 {
		t.Errorf("After saw %+v; want the handler's result and a message holding a copy of its metadata", seen)
	}
	if res.LLMText() != "x" || md.Outcome != "exit" {
		t.Errorf("the edit reached the returned result (%q) or the handler's metadata (%q)", res.LLMText(), md.Outcome)
	}
	for _, e := range events.all() {
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

func TestToolResultMessage_RendersTheResultForTheTranscript(t *testing.T) {
	c := core.ToolUseBlock{ID: "tu", Name: "read_file"}
	body := "func main() {\n\tfmt.Println(\"hi\")\n}\n"
	md := &core.ToolMetadata{TotalBytes: 5}
	img := core.ThinkingBlock{Thinking: "an extra block"}
	for _, x := range []struct {
		name  string
		res   core.ToolResult
		text  string
		error bool
	}{
		{"a tool's own rendering, verbatim", core.ToolResult{OK: true, Data: map[string]any{"content": body}, Text: body}, body, false},
		{"the envelope without one", core.ToolResult{OK: true, Data: map[string]any{"n": 1}}, `{"data":{"n":1},"ok":true}`, false},
		{"an error envelope", core.ErrResult("edit_not_found", "not found"), `{"detail":"not found","error":"edit_not_found","ok":false}`, true},
		{"a rendered error", core.ToolResult{Error: "boom", Text: "rendered"}, "rendered", true},
		{"metadata, copied", core.ToolResult{OK: true, Text: "t", Metadata: md, Blocks: core.Content{img}}, "t", false},
		{"empty metadata, dropped", core.ToolResult{OK: true, Text: "t", Metadata: &core.ToolMetadata{}}, "t", false},
	} {
		m := toolResultMessage(c, x.res)
		if m.Content.Text() != x.text || m.IsError != x.error || m.ToolUseID != "tu" || m.ToolName != "read_file" {
			t.Errorf("%s: %+v, want text %q", x.name, m, x.text)
		}
		if x.res.Blocks != nil && (len(m.Content) != 2 || m.Content[1] != core.ContentBlock(img)) {
			t.Errorf("%s: the tool's blocks do not follow the text: %v", x.name, m.Content)
		}
		if wantMD := x.res.Metadata != nil && !x.res.Metadata.IsEmpty(); (m.Metadata != nil) != wantMD || (wantMD && (m.Metadata == md || *m.Metadata != *md)) {
			t.Errorf("%s: metadata %+v; want a copy when there is any", x.name, m.Metadata)
		}
	}
}

func TestUnknownToolMessage_ListsTheAvailableToolsSortedAndCapped(t *testing.T) {
	tools := func(names ...string) map[string]core.Tool {
		m := map[string]core.Tool{}
		for _, n := range names {
			m[n] = core.Tool{Name: n}
		}
		return m
	}
	var sixty []string
	for i := range 60 {
		sixty = append(sixty, fmt.Sprintf("tool_%02d", i))
	}
	for _, c := range []struct {
		have map[string]core.Tool
		want string
	}{
		{nil, `no tool named "bash" is available in this run; no tools are available`},
		{tools("zeta", "alpha", "mid"), `no tool named "bash" is available in this run; available tools: alpha, mid, zeta`},
		{tools(sixty...), `no tool named "bash" is available in this run; available tools: ` + strings.Join(sixty[:50], ", ") + " (and 10 more)"},
	} {
		if got := unknownToolMessage("bash", c.have); got != c.want {
			t.Errorf("unknownToolMessage = %q, want %q", got, c.want)
		}
	}
}
