package agentkit

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// runBatch runs one batch of calls through a real agent's executeBatch, the
// way the loop does, and returns the results and the termination vote.
func runBatch(t *testing.T, ctx context.Context, cfg Config, calls ...core.ToolUseBlock) ([]core.ToolResultMessage, bool) {
	t.Helper()
	cfg.Provider, cfg.Model = faux.New(), testModelID
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	blocks := make([]core.ContentBlock, len(calls))
	for i, c := range calls {
		blocks[i] = c
	}
	assistant := core.AssistantMessage{Content: blocks, StopReason: core.StopReasonToolUse}
	return a.executeBatch(ctx, core.NewEventStream(core.StreamOptions{}), &assistant, calls, 0)
}

// overlap is a handler that records how many calls run at once.
func overlap(active, peak *atomic.Int32, hold time.Duration) func(context.Context, json.RawMessage) (json.RawMessage, error) {
	return func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n := active.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(hold)
		active.Add(-1)
		return json.RawMessage(`{}`), nil
	}
}

// TS-11-26: an invalid call and a blocked call are finalized in prepare,
// as error results, and their handlers never run.
func TestPrepareFinalizesInvalidAndBlockedCalls_TS11_26(t *testing.T) {
	var ran atomic.Int32
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		ran.Add(1)
		return json.RawMessage(`{}`), nil
	}
	tools := []core.Tool{
		{Name: "schemaTool", InputSchema: schema.Object(schema.Prop("count", schema.Int())), Handler: handler},
		{Name: "blockedTool", InputSchema: schema.Object(), Handler: handler},
	}
	var order []string
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		order = append(order, in.ToolUseID) // no lock: prepare is sequential
		return core.BeforeToolCallDecision{Block: in.ToolName == "blockedTool", Reason: "disallowed"}
	}
	results, terminate := runBatch(t, context.Background(), Config{Tools: tools, Guard: guard},
		toolUse(t, "c1", "schemaTool", `{"count":"not a number"}`),
		toolUse(t, "c2", "blockedTool", `{}`),
		toolUse(t, "c3", "blockedTool", `{}`))
	if ran.Load() != 0 || terminate {
		t.Fatalf("handlers ran %d times, terminate %v; want 0 and false", ran.Load(), terminate)
	}
	if !results[0].IsError || !strings.Contains(results[0].Content.Text(), "invalid_arguments") {
		t.Fatalf("invalid call = %q", results[0].Content.Text())
	}
	for _, r := range results[1:] {
		if !r.IsError || !strings.Contains(r.Content.Text(), "disallowed") {
			t.Fatalf("blocked call = %q", r.Content.Text())
		}
	}
	if strings.Join(order, ",") != "c2,c3" {
		t.Fatalf("Guard saw %v, want the valid calls in order", order)
	}
}

// TS-11-27: a cancelled context runs no handler and aborts every call.
func TestCancelledBatchRunsNothing_TS11_27(t *testing.T) {
	var ran atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, _ := runBatch(t, ctx, Config{Tools: []core.Tool{echoTool("testTool", &ran)}},
		toolUse(t, "c1", "testTool", `{}`), toolUse(t, "c2", "testTool", `{}`))
	if ran.Load() != 0 {
		t.Fatalf("%d handlers ran under a cancelled context", ran.Load())
	}
	for _, r := range results {
		if !r.IsError || !strings.Contains(r.Content.Text(), "aborted") {
			t.Fatalf("call %s = %q, want aborted", r.ToolUseID, r.Content.Text())
		}
	}
}

// TS-11-27, continued: a cancel landing after some calls were prepared but
// before any handler starts still runs nothing — the decision is made once,
// after prepare.
func TestCancelDuringPrepareRunsNothing_TS11_27(t *testing.T) {
	var ran atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if in.ToolUseID == "c3" {
			cancel() // c1 and c2 are prepared; c3 is the last
		}
		return core.BeforeToolCallDecision{}
	}
	results, _ := runBatch(t, ctx, Config{Tools: []core.Tool{echoTool("testTool", &ran)}, Guard: guard},
		toolUse(t, "c1", "testTool", `{}`), toolUse(t, "c2", "testTool", `{}`), toolUse(t, "c3", "testTool", `{}`))
	if ran.Load() != 0 {
		t.Fatalf("%d handlers ran after the cancel", ran.Load())
	}
	for _, r := range results {
		if !r.IsError || !strings.Contains(r.Content.Text(), "aborted") {
			t.Fatalf("call %s = %q, want aborted", r.ToolUseID, r.Content.Text())
		}
	}
}

// TS-11-28: a live context runs every prepared call, concurrently.
func TestBatchRunsHandlersConcurrently_TS11_28(t *testing.T) {
	var active, peak atomic.Int32
	tool := core.Tool{Name: "parallelTool", InputSchema: schema.Object(), Handler: overlap(&active, &peak, 30*time.Millisecond)}
	results, _ := runBatch(t, context.Background(), Config{Tools: []core.Tool{tool}},
		toolUse(t, "c1", "parallelTool", `{}`), toolUse(t, "c2", "parallelTool", `{}`), toolUse(t, "c3", "parallelTool", `{}`))
	if peak.Load() < 2 || len(results) != 3 {
		t.Fatalf("peak concurrency %d with %d results; want more than 1 and 3", peak.Load(), len(results))
	}
	for _, r := range results {
		if r.IsError {
			t.Fatalf("call %s failed: %q", r.ToolUseID, r.Content.Text())
		}
	}
}

// TS-11-29: a Sequential tool runs the batch one call at a time, in order.
func TestSequentialToolSerializesTheBatch_TS11_29(t *testing.T) {
	var active, peak atomic.Int32
	var order []string
	seq := core.Tool{Name: "seqTool", InputSchema: schema.Object(schema.Opt("v", schema.String())), ExecutionMode: core.Sequential,
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			var a struct{ V string }
			_ = json.Unmarshal(in, &a)
			order = append(order, a.V) // no lock: sequential is the claim under test
			return overlap(&active, &peak, 10*time.Millisecond)(ctx, in)
		}}
	runBatch(t, context.Background(), Config{Tools: []core.Tool{seq}},
		toolUse(t, "c1", "seqTool", `{"v":"1"}`), toolUse(t, "c2", "seqTool", `{"v":"2"}`), toolUse(t, "c3", "seqTool", `{"v":"3"}`))
	if peak.Load() != 1 || strings.Join(order, "") != "123" {
		t.Fatalf("peak %d, order %v; want 1 and call order", peak.Load(), order)
	}
}

// TS-11-30: After is serialized, and results land in call order whatever
// order the handlers finish in.
func TestFinalizeIsSerializedAndSlotOrdered_TS11_30(t *testing.T) {
	sleepy := func(name string, d time.Duration) core.Tool {
		return core.Tool{Name: name, InputSchema: schema.Object(),
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				time.Sleep(d)
				return json.RawMessage(`{}`), nil
			}}
	}
	var inAfter, peak atomic.Int32
	var finished []string
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		n := inAfter.Add(1)
		if n > peak.Load() {
			peak.Store(n)
		}
		finished = append(finished, in.ToolUseID) // safe only if serialized
		time.Sleep(5 * time.Millisecond)
		inAfter.Add(-1)
		return core.AfterToolCallDecision{}
	}
	results, _ := runBatch(t, context.Background(),
		Config{Tools: []core.Tool{sleepy("slowTool", 50*time.Millisecond), sleepy("fastTool", 5*time.Millisecond)}, After: after},
		toolUse(t, "slot0", "slowTool", `{}`), toolUse(t, "slot1", "fastTool", `{}`), toolUse(t, "slot2", "fastTool", `{}`))
	if results[0].ToolUseID != "slot0" || results[1].ToolUseID != "slot1" || results[2].ToolUseID != "slot2" {
		t.Fatalf("results out of call order: %s %s %s", results[0].ToolUseID, results[1].ToolUseID, results[2].ToolUseID)
	}
	if peak.Load() != 1 || len(finished) != 3 || finished[len(finished)-1] != "slot0" {
		t.Fatalf("After peak %d, finish order %v; want serialized and the slow call last", peak.Load(), finished)
	}
}

// terminating votes to end the run after hold.
func terminating(name string, hold time.Duration) core.Tool {
	return core.Tool{Name: name, InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			time.Sleep(hold)
			r := core.OKResult(nil)
			r.Terminate = true
			return r
		}}
}

// TS-11-31: one executed call voting Terminate ends the run, after its
// siblings finish.
func TestATerminateVoteEndsTheRunAfterTheBatch_TS11_31(t *testing.T) {
	var siblingDone atomic.Bool
	slow := core.Tool{Name: "slowTool", InputSchema: schema.Object(),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			time.Sleep(20 * time.Millisecond)
			siblingDone.Store(true)
			return json.RawMessage(`{"ok":true}`), nil
		}}
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c1", "termTool", "{}"), faux.FauxToolCall("c2", "slowTool", "{}")),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("never requested")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{terminating("termTool", 0), slow}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "run both")
	if err != nil {
		t.Fatal(err)
	}
	if !siblingDone.Load() || res.StopReason != core.RunStopToolTerminate || fp.Calls() != 1 {
		t.Fatalf("sibling done %v, stop %q, requests %d; want true, tool_terminate, 1",
			siblingDone.Load(), res.StopReason, fp.Calls())
	}
	if _, ok := toolResultIn(res.Messages, "c2"); !ok {
		t.Fatal("the sibling's result is missing from the transcript")
	}
}

// TS-11-32: a call blocked in prepare casts no termination vote of its own,
// whatever its tool would have voted, and neither does an invalid one. Only
// the Guard's own Terminate, beside Block, ends the run.
func TestBlockedCallsDoNotTerminate_TS11_32(t *testing.T) {
	for _, terminate := range []bool{false, true} {
		fp := faux.New(
			faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c1", "termTool", "{}")),
			faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("continue")),
		)
		a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{terminating("termTool", 0)},
			Guard: func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
				return core.BeforeToolCallDecision{Block: true, Terminate: terminate, Reason: "denied"}
			}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := a.Run(context.Background(), "test guard")
		if err != nil {
			t.Fatal(err)
		}
		want, calls := core.RunStopEndTurn, 2
		if terminate {
			want, calls = core.RunStopToolTerminate, 1
		}
		if res.StopReason != want || fp.Calls() != calls {
			t.Fatalf("guard terminate=%v: stop %q after %d requests; want %q after %d",
				terminate, res.StopReason, fp.Calls(), want, calls)
		}
	}
	typed := terminating("termTool", 0)
	typed.InputSchema = schema.Object(schema.Prop("n", schema.Int()))
	results, vote := runBatch(t, context.Background(), Config{Tools: []core.Tool{typed}},
		toolUse(t, "c1", "termTool", `{"n":"not a number"}`))
	if !results[0].IsError || vote {
		t.Fatalf("invalid call = %q, vote %v; want an error result and no vote", results[0].Content.Text(), vote)
	}
}
