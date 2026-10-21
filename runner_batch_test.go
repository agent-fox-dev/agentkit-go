package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// timeline is an ordered, concurrency-safe log of what happened during a batch.
type timeline struct {
	mu      sync.Mutex
	entries []string
}

func (l *timeline) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, fmt.Sprintf(format, args...))
}

func (l *timeline) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.entries)
}

// firstIndex is the index of the first entry with prefix, or -1.
func (l *timeline) firstIndex(prefix string) int {
	return slices.IndexFunc(l.all(), func(e string) bool { return strings.HasPrefix(e, prefix) })
}

// lastIndex is the index of the last entry with prefix, or -1.
func (l *timeline) lastIndex(prefix string) int {
	e := l.all()
	for i := len(e) - 1; i >= 0; i-- {
		if strings.HasPrefix(e[i], prefix) {
			return i
		}
	}
	return -1
}

// TS-21-22: Batch emits start events in call order, runs preparation and every
// Guard before any handler, and finalizes unknown, invalid and blocked calls
// inline with the codes of a single call.
func TestBatchPreparesInOrderBeforeAnyHandler_TS21_22(t *testing.T) {
	var tl timeline
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		tl.add("handler")
		return json.RawMessage(`{}`), nil
	}
	tools := []core.Tool{
		{Name: "ok", InputSchema: schema.Object(), Handler: handler},
		{Name: "needs_text", InputSchema: schema.Object(schema.Prop("text", schema.String())), Handler: handler},
		{Name: "blocked_tool", InputSchema: schema.Object(), Handler: handler},
	}
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		tl.add("guard:%s", in.ToolUseID)
		return core.BeforeToolCallDecision{Block: in.ToolName == "blocked_tool", Reason: "no"}
	}
	sink := func(e core.Event) {
		if s, ok := e.(core.ToolExecutionStartEvent); ok {
			tl.add("start:%s", s.ToolUseID)
		}
	}
	r := mustRunner(t, Toolset{Tools: tools, Guard: guard, Events: sink})

	res := r.Batch(context.Background(),
		toolUse(t, "c1", "ok", `{}`),
		toolUse(t, "c2", "missing", `{}`),
		toolUse(t, "c3", "needs_text", `{}`),
		toolUse(t, "c4", "blocked_tool", `{}`),
		toolUse(t, "c5", "ok", `{}`))

	wantCodes := []string{"", "unknown_tool", "invalid_arguments", core.BlockErrorCode, ""}
	for i, want := range wantCodes {
		if res[i].Error != want || res[i].OK != (want == "") {
			t.Errorf("result %d = %+v; want code %q", i, res[i], want)
		}
	}
	var starts []string
	for _, e := range tl.all() {
		if id, ok := strings.CutPrefix(e, "start:"); ok {
			starts = append(starts, id)
		}
	}
	if !slices.Equal(starts, []string{"c1", "c2", "c3", "c4", "c5"}) {
		t.Errorf("start events arrived as %v, want call order", starts)
	}
	firstHandler := tl.firstIndex("handler")
	if firstHandler < 0 {
		t.Fatalf("no handler ran: %v", tl.all())
	}
	if g, s := tl.lastIndex("guard:"), tl.lastIndex("start:"); g > firstHandler || s > firstHandler {
		t.Errorf("a guard (%d) or start event (%d) came after the first handler (%d): %v", g, s, firstHandler, tl.all())
	}
	if n := strings.Count(strings.Join(tl.all(), ","), "guard:"); n != 3 {
		t.Errorf("Guard ran %d times, want 3 (c1, c4, c5: c2 is unknown and c3 is invalid)", n)
	}
}

// countEvents counts the events of one kind a sink received for a call id.
func countEvents[E core.Event](events []core.Event, id string, idOf func(E) string) int {
	n := 0
	for _, e := range events {
		if ev, ok := e.(E); ok && idOf(ev) == id {
			n++
		}
	}
	return n
}

// TS-21-23: a context that is done when the prepare phase ends aborts every
// call not yet finalized, including the call the prepare loop never reached,
// and no handler runs.
func TestBatchDoneContextAbortsEveryUnfinalizedCall_TS21_23(t *testing.T) {
	var ran atomic.Int32
	var events eventLog
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if in.ToolUseID == "c1" {
			cancel()
		}
		return core.BeforeToolCallDecision{}
	}
	r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", &ran)}, Guard: guard, Events: events.add})

	res := r.Batch(ctx, toolUse(t, "c0", "echo", `{}`), toolUse(t, "c1", "echo", `{}`), toolUse(t, "c2", "echo", `{}`))

	if ran.Load() != 0 {
		t.Fatalf("%d handlers ran after the context was done", ran.Load())
	}
	got := events.all()
	for i, id := range []string{"c0", "c1", "c2"} {
		if res[i].OK || res[i].Error != "aborted" || res[i].Detail != "Operation aborted" {
			t.Errorf("%s = %+v; want aborted with %q", id, res[i], "Operation aborted")
		}
		starts := countEvents(got, id, func(e core.ToolExecutionStartEvent) string { return e.ToolUseID })
		ends := countEvents(got, id, func(e core.ToolExecutionEndEvent) string { return e.ToolUseID })
		if starts != 1 || ends != 1 {
			t.Errorf("%s has %d start and %d end events, want one of each", id, starts, ends)
		}
	}
}

// TS-21-24: handlers overlap when a batch is concurrent, never overlap and run
// in call order when a Sequential tool is among the calls, and After never
// overlaps itself.
func TestBatchConcurrencyRules_TS21_24(t *testing.T) {
	t.Run("handlers overlap", func(t *testing.T) {
		var arrived atomic.Int32
		both := make(chan struct{})
		var overlapped atomic.Int32
		handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if arrived.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
				overlapped.Add(1)
			case <-time.After(5 * time.Second):
			}
			return json.RawMessage(`{}`), nil
		}
		tool := core.Tool{Name: "meet", InputSchema: schema.Object(), Handler: handler}
		r := mustRunner(t, Toolset{Tools: []core.Tool{tool}})
		r.Batch(context.Background(), toolUse(t, "c1", "meet", `{}`), toolUse(t, "c2", "meet", `{}`))
		if overlapped.Load() != 2 {
			t.Fatalf("%d of 2 handlers saw the other running; a concurrent batch overlaps its handlers", overlapped.Load())
		}
	})

	t.Run("a Sequential tool serializes the batch", func(t *testing.T) {
		var active, peak atomic.Int32
		var mu sync.Mutex
		var order []string
		record := func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			var a struct{ V string }
			_ = json.Unmarshal(in, &a)
			mu.Lock()
			order = append(order, a.V)
			mu.Unlock()
			return overlap(&active, &peak, 10*time.Millisecond)(ctx, in)
		}
		schemaV := schema.Object(schema.Opt("v", schema.String()))
		seq := core.Tool{Name: "seq", InputSchema: schemaV, ExecutionMode: core.Sequential, Handler: record}
		par := core.Tool{Name: "par", InputSchema: schemaV, Handler: record}
		r := mustRunner(t, Toolset{Tools: []core.Tool{seq, par}})
		r.Batch(context.Background(),
			toolUse(t, "c1", "par", `{"v":"1"}`), toolUse(t, "c2", "seq", `{"v":"2"}`), toolUse(t, "c3", "par", `{"v":"3"}`))
		if peak.Load() != 1 || strings.Join(order, "") != "123" {
			t.Fatalf("peak concurrency %d, order %v; want 1 and call order", peak.Load(), order)
		}
	})

	t.Run("After never overlaps", func(t *testing.T) {
		var inAfter, peak atomic.Int32
		after := func(context.Context, core.AfterToolCallContext) core.AfterToolCallDecision {
			n := inAfter.Add(1)
			if n > peak.Load() {
				peak.Store(n)
			}
			time.Sleep(5 * time.Millisecond)
			inAfter.Add(-1)
			return core.AfterToolCallDecision{}
		}
		r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", nil)}, After: after})
		r.Batch(context.Background(), toolUse(t, "c1", "echo", `{}`), toolUse(t, "c2", "echo", `{}`), toolUse(t, "c3", "echo", `{}`))
		if peak.Load() != 1 {
			t.Fatalf("%d After invocations overlapped, want them serialized", peak.Load())
		}
	})
}

// TS-21-25: Batch returns one result per call, in call order, whatever order
// the handlers finish in; it uses ids verbatim and shows Guard the documented
// context; zero calls return an empty slice and emit nothing.
func TestBatchOrderIDsAndZeroCalls_TS21_25(t *testing.T) {
	reverse := core.Tool{Name: "reverse", InputSchema: schema.Object(schema.Prop("n", schema.Int())),
		Handler: func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
			var a struct{ N int }
			_ = json.Unmarshal(in, &a)
			time.Sleep(time.Duration(3-a.N) * 20 * time.Millisecond) // call 0 finishes last
			return json.RawMessage(fmt.Sprintf(`{"n":%d}`, a.N)), nil
		}}
	var guards guardLog
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{reverse}, Guard: guards.guard, Events: events.add})

	calls := []core.ToolUseBlock{
		toolUse(t, "a", "reverse", `{"n":0}`),
		toolUse(t, "", "reverse", `{"n":1}`),
		toolUse(t, "c", "reverse", `{"n":2}`),
	}
	res := r.Batch(context.Background(), calls...)
	if len(res) != len(calls) {
		t.Fatalf("%d results for %d calls", len(res), len(calls))
	}
	for i := range calls {
		if want := fmt.Sprintf(`"n":%d`, i); !strings.Contains(res[i].LLMText(), want) {
			t.Errorf("result %d = %q, want it to belong to call %d (%s)", i, res[i].LLMText(), i, want)
		}
	}

	seen := guards.seen()
	if len(seen) != len(calls) {
		t.Fatalf("Guard saw %d calls, want %d", len(seen), len(calls))
	}
	for _, g := range seen {
		i := g.Index
		if i < 0 || i >= len(calls) || g.ToolUseID != calls[i].ID {
			t.Fatalf("Guard saw id %q at index %d; want the id of call %d", g.ToolUseID, i, i)
		}
		if !reflect.DeepEqual(g.Batch, calls) || g.Assistant != nil || g.TurnCount != 0 {
			t.Errorf("Guard for call %d saw Batch %v, Assistant %v, TurnCount %d; want the calls, nil and 0", i, g.Batch, g.Assistant, g.TurnCount)
		}
	}
	for i, c := range calls {
		starts := countEvents(events.all(), c.ID, func(e core.ToolExecutionStartEvent) string { return e.ToolUseID })
		if starts != 1 {
			t.Errorf("call %d (id %q) has %d start events, want 1: events carry the id verbatim, an empty one included", i, c.ID, starts)
		}
	}

	before := len(events.all())
	if got := r.Batch(context.Background()); len(got) != 0 {
		t.Errorf("zero calls returned %d results", len(got))
	}
	if after := len(events.all()); after != before {
		t.Errorf("zero calls emitted %d events", after-before)
	}
}

// TS-21-26: each result's Terminate is the call's final vote.
func TestBatchTerminateIsTheFinalVote_TS21_26(t *testing.T) {
	voter := terminating("voter", 0)
	quiet := echoTool("quiet", nil)
	inner := echoTool("inner", nil)
	wrapper := core.Tool{Name: "wrapper", InputSchema: schema.Object(), ReachableTools: []core.Tool{inner},
		Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			_, err := core.CallNested(ctx, core.ToolUseBlock{Name: "inner", Input: json.RawMessage(`{}`)})
			if err == nil {
				return nil, fmt.Errorf("nested call was not terminated")
			}
			return json.RawMessage(`{}`), nil
		}}
	vote := func(b bool) func(context.Context, core.AfterToolCallContext) core.AfterToolCallDecision {
		return func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == "inner" {
				return core.AfterToolCallDecision{}
			}
			return core.AfterToolCallDecision{Terminate: &b}
		}
	}
	allow := func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{}
	}
	cases := []struct {
		name  string
		ts    Toolset
		call  string
		wants bool
	}{
		{"the handler's vote stands", Toolset{Tools: []core.Tool{voter}}, "voter", true},
		{"After replaces the handler's vote", Toolset{Tools: []core.Tool{voter}, After: vote(false)}, "voter", false},
		{"After votes for a quiet handler", Toolset{Tools: []core.Tool{quiet}, After: vote(true)}, "quiet", true},
		{"a wrapper whose nested call an interceptor terminated", Toolset{Tools: []core.Tool{wrapper},
			Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
				return core.BeforeToolCallDecision{Block: in.ToolName == "inner", Terminate: true, Reason: "stop"}
			}}, "wrapper", true},
		{"a blocked call takes the guard's vote", Toolset{Tools: []core.Tool{quiet},
			Guard: func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
				return core.BeforeToolCallDecision{Block: true, Terminate: true}
			}}, "quiet", true},
		{"a blocked call without the guard's vote", Toolset{Tools: []core.Tool{voter}, Guard: func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Block: true}
		}}, "voter", false},
	}
	for _, c := range cases {
		if c.ts.Guard == nil {
			c.ts.Guard = allow
		}
		r := mustRunner(t, c.ts)
		res := r.Batch(context.Background(), toolUse(t, "c1", c.call, `{}`))
		if got := res[0].Terminate; got != c.wants {
			t.Errorf("%s: Terminate = %v, want %v (result %+v)", c.name, got, c.wants, res[0])
		}
	}
}

// batchKinds are the kinds of call the property test mixes. Each is a tool
// name and the input to call it with.
var batchKinds = []struct{ tool, input string }{
	{"ok", `{"n":1}`},
	{"ok", `{"n":2}`},
	{"fail", `{}`},
	{"boom", `{}`},
	{"missing", `{}`},
	{"needs_text", `{}`},
	{"blocked_tool", `{}`},
}

// batchToolset is a toolset with a deterministic outcome for every kind of
// call, and no After.
func batchToolset() Toolset {
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	return Toolset{
		Tools: []core.Tool{
			{Name: "ok", InputSchema: schema.Object(schema.Opt("n", schema.Int())), Handler: func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
				return in, nil
			}},
			{Name: "fail", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return nil, fmt.Errorf("it broke")
			}},
			{Name: "boom", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				panic("kaput")
			}},
			{Name: "needs_text", InputSchema: schema.Object(schema.Prop("text", schema.String())), Handler: handler},
			{Name: "blocked_tool", InputSchema: schema.Object(), Handler: handler},
		},
		Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Block: in.ToolName == "blocked_tool", Reason: "not allowed"}
		},
	}
}

// TS-21-27: with a nil After, the text of each result Batch returns equals the
// text of the ToolResultMessage an agent run records for the same call.
func TestBatchTextsEqualTheRunsMessageTexts_TS21_27(t *testing.T) {
	rng := rand.New(rand.NewSource(27))
	for iter := range 40 {
		n := 1 + rng.Intn(6)
		var calls []core.ToolUseBlock
		var blocks []core.ContentBlock
		for i := range n {
			k := batchKinds[rng.Intn(len(batchKinds))]
			id := fmt.Sprintf("c%d", i)
			calls = append(calls, toolUse(t, id, k.tool, k.input))
			blocks = append(blocks, faux.FauxToolCall(id, k.tool, k.input))
		}

		ts := batchToolset()
		got := mustRunner(t, ts).Batch(context.Background(), calls...)

		fp := faux.New(
			faux.Turn{Blocks: blocks, StopReason: core.StopReasonToolUse},
			faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, StopReason: core.StopReasonStop},
		)
		a, err := New(Config{Client: fp, Model: testModelID, Toolset: batchToolset()})
		if err != nil {
			t.Fatal(err)
		}
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatalf("iteration %d: %v", iter, err)
		}
		for i, c := range calls {
			msg, ok := toolResultIn(res.Messages, c.ID)
			if !ok {
				t.Fatalf("iteration %d: no tool result for %s in the run", iter, c.ID)
			}
			if want := msg.Content.Text(); got[i].LLMText() != want {
				t.Errorf("iteration %d, call %d (%s %s): Batch says %q, the run recorded %q",
					iter, i, c.Name, c.Input, got[i].LLMText(), want)
			}
		}
	}
}

// TS-21-28: an After that edits the result message in place changes the
// ToolResultEvent and the run's transcript, and leaves the ToolResult the
// runner returns alone.
func TestAfterEditsReachTheMessageNotTheResult_TS21_28(t *testing.T) {
	orig := core.Tool{Name: "orig", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.ToolResult{OK: true, Text: "original text"}
		}}
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		in.Result.Content = core.Content{core.TextBlock{Text: "edited"}}
		return core.AfterToolCallDecision{}
	}
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{orig}, After: after, Events: events.add})

	res := r.Call(context.Background(), "orig", nil)
	if res.LLMText() != "original text" {
		t.Errorf("the returned result says %q, want the handler's %q", res.LLMText(), "original text")
	}
	var resultEvents []core.ToolResultEvent
	for _, e := range events.all() {
		if re, ok := e.(core.ToolResultEvent); ok {
			resultEvents = append(resultEvents, re)
		}
	}
	if len(resultEvents) != 1 || resultEvents[0].Message.Content.Text() != "edited" {
		t.Errorf("ToolResultEvents = %+v, want one carrying the edit", resultEvents)
	}

	fp := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("c1", "orig", "{}")}, StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, StopReason: core.StopReasonStop},
	)
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{Tools: []core.Tool{orig}, After: after}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if msg, ok := toolResultIn(run.Messages, "c1"); !ok || msg.Content.Text() != "edited" {
		t.Errorf("the run's transcript holds %q, want the edit", msg.Content.Text())
	}
}
