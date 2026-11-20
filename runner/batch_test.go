package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestBatch_PreparesInCallOrderBeforeAnyHandler(t *testing.T) {
	var events eventLog
	var log []string // appended by the prepare phase and the handlers; the claim is that they do not interleave
	var handlers atomic.Int32
	h := func(context.Context, json.RawMessage) core.ToolResult { handlers.Add(1); return core.OKResult(nil) }
	strict := core.Tool{Name: "strict", InputSchema: schema.Object(schema.Prop("text", schema.String())), Execute: h}
	guards := guardLog{Decide: func(in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		log = append(log, fmt.Sprintf("guard %s handlers=%d", in.ToolUseID, handlers.Load()))
		return core.BeforeToolCallDecision{Block: in.ToolName == "blocked", Reason: "no"}
	}}
	r := mustRunner(t, Toolset{Tools: []core.Tool{tool("ok", h), strict, tool("blocked", h)}, Guard: guards.Guard, Events: events.Add})
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
	for _, e := range events.All() {
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
		r := mustRunner(t, Toolset{Tools: []core.Tool{echo("echo", &ran)}, Guard: guard, Events: events.Add})
		res := r.Batch(ctx, use(t, "c1", "echo", `{}`), use(t, "c2", "echo", `{}`), use(t, "c3", "echo", `{}`))
		cancel()
		starts, ends, results := executions(events.All())
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
		wait := 5 * time.Second
		if c.peak == 1 {
			wait = 20 * time.Millisecond
		}
		m := meet(3, wait)
		var ts Toolset
		var batch []core.ToolUseBlock
		for i, mode := range c.modes {
			name := fmt.Sprint("t", i)
			ts.Tools = append(ts.Tools, attending(name, m, mode))
			batch = append(batch, use(t, fmt.Sprint("c", i), name, fmt.Sprintf(`{"v":"%d"}`, i)))
		}
		res := mustRunner(t, ts).Batch(context.Background(), batch...)
		if m.Peak() != c.peak {
			t.Errorf("%s: %d handlers ran at once, want %d", c.name, m.Peak(), c.peak)
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

// RunTurn is the batch a model's turn makes: the caller names the sink, the
// assistant message and the turn, and gets the transcript messages and the
// batch's vote beside the results.
func TestRunTurn_RunsAModelsBatchWithTheCallersSinkAssistantAndTurn(t *testing.T) {
	var own, toolset eventLog
	var guards guardLog
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		if in.ToolName == "probe" {
			in.Result.Content = core.Content{core.TextBlock{Text: "edited"}}
		}
		return core.AfterToolCallDecision{}
	}
	probe := returning("probe", core.ToolResult{OK: true, Text: "original"})
	r := mustRunner(t, Toolset{Tools: []core.Tool{probe, voter("voter")}, Guard: guards.Guard, After: after, Events: toolset.Add})
	assistant := &core.AssistantMessage{StopReason: core.StopReasonToolUse}
	calls := []core.ToolUseBlock{use(t, "a", "probe", "{}"), use(t, "b", "voter", "{}")}

	out := r.RunTurn(context.Background(), own.Add, assistant, calls, 3)
	if len(out.Messages) != 2 || len(out.Results) != 2 || out.Messages[0].ToolUseID != "a" || out.Messages[1].ToolUseID != "b" {
		t.Fatalf("Outcome = %+v, want one message and one result per call, in call order", out)
	}
	if !out.Terminate || out.Results[0].Terminate || !out.Results[1].Terminate {
		t.Errorf("Terminate = %v with votes %v, %v; want the OR of the calls' votes", out.Terminate, out.Results[0].Terminate, out.Results[1].Terminate)
	}
	if got := out.Messages[0].Content.Text(); got != "edited" || out.Results[0].LLMText() != "original" {
		t.Errorf("message %q, result %q; want After's edit in the message and not in the result", got, out.Results[0].LLMText())
	}
	seen := guards.Calls()
	if len(seen) != 2 || seen[0].Assistant != assistant || seen[1].TurnCount != 3 || seen[1].Index != 1 || len(seen[1].Batch) != 2 {
		t.Errorf("the guard saw %+v, want the assistant message, turn 3 and the batch", seen)
	}
	starts, ends, results := executions(own.All())
	if starts["a"] != 1 || ends["a"] != 1 || results["a"] != 1 || starts["b"] != 1 || ends["b"] != 1 || results["b"] != 1 || len(toolset.All()) != 0 {
		t.Errorf("the caller's sink got %d events and the Toolset's %d; want every event on the caller's and none on the Toolset's", len(own.All()), len(toolset.All()))
	}

	// A nil sink discards the events, a panicking one drops the event, and no
	// calls are no work.
	if got := r.RunTurn(context.Background(), nil, nil, calls[:1], 0); len(got.Results) != 1 || !got.Results[0].OK {
		t.Errorf("with a nil sink: %+v", got)
	}
	if got := r.RunTurn(context.Background(), func(core.Event) { panic("sink") }, nil, calls[:1], 0); len(got.Results) != 1 || !got.Results[0].OK {
		t.Errorf("with a panicking sink: %+v", got)
	}
	n := len(own.All())
	if got := r.RunTurn(context.Background(), own.Add, assistant, nil, 4); len(got.Messages) != 0 || len(got.Results) != 0 || got.Terminate || len(own.All()) != n {
		t.Errorf("zero calls gave %+v and %d events", got, len(own.All())-n)
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
