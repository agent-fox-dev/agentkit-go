package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// TS-21-10: a runner is safe for concurrent Call, Batch, Tools and Reachable.
// Run under the race detector, sixteen goroutines hammer one runner whose
// Guard, After and Events are concurrency-safe.
func TestRunnerIsSafeForConcurrentUse_TS21_10(t *testing.T) {
	wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		res, err := core.CallNested(ctx, nestedCalls("child", "child")...)
		if err != nil || len(res) != 2 {
			return core.ErrResult("nested_failed", fmt.Sprint(err))
		}
		return core.OKResult(nil)
	}, childTool("child"))
	var guards guardLog
	var afters afterLog
	var events eventLog
	r := mustRunner(t, Toolset{
		Tools: []core.Tool{echoTool("echo", nil), wrapper},
		Guard: guards.guard, After: afters.after, Events: events.add,
	})

	var wg sync.WaitGroup
	var bad atomic.Int32
	for g := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if res := r.Call(context.Background(), "echo", json.RawMessage(`{"v":"x"}`)); !res.OK {
					bad.Add(1)
				}
				res := r.Batch(context.Background(),
					toolUse(t, fmt.Sprintf("g%d_a", g), "echo", `{}`),
					toolUse(t, fmt.Sprintf("g%d_b", g), "wrapper", `{}`),
					toolUse(t, fmt.Sprintf("g%d_c", g), "missing", `{}`))
				if len(res) != 3 || !res[0].OK || !res[1].OK || res[2].Error != "unknown_tool" {
					bad.Add(1)
				}
				if len(r.Tools()) != 2 || len(r.Reachable()) != 3 {
					bad.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d calls returned an unexpected shape under concurrent use", bad.Load())
	}
}

// panickingToolset is a toolset in which each of the four user-code hooks
// panics for a tool of its own, and one more tool has After edit its result.
func panickingToolset(events func(core.Event)) Toolset {
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }
	return Toolset{
		Tools: []core.Tool{
			{Name: "prepare_panic", InputSchema: schema.Object(), Handler: handler,
				PrepareArguments: func(map[string]any) map[string]any { panic("prepare broke") }},
			{Name: "guard_panic", InputSchema: schema.Object(), Handler: handler},
			{Name: "handler_panic", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				panic("handler broke")
			}},
			{Name: "after_panic", InputSchema: schema.Object(), Handler: handler},
			{Name: "edited", InputSchema: schema.Object(), Handler: handler},
		},
		Guard: func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			if in.ToolName == "guard_panic" {
				panic("guard broke")
			}
			return core.BeforeToolCallDecision{}
		},
		After: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			switch in.ToolName {
			case "after_panic":
				panic("after broke")
			case "edited":
				in.Result.Content = core.Content{core.TextBlock{Text: "edited by After"}}
			}
			return core.AfterToolCallDecision{}
		},
		Events: events,
	}
}

// TS-21-36: outside a run, Events receives a start and an end event for every
// call, a ToolResultEvent for each top-level call carrying the message as
// After left it, and an ErrorEvent for each panic the pipeline recovered.
func TestEventsReceiveTheFullSequenceOutsideARun_TS21_36(t *testing.T) {
	var events eventLog
	r := mustRunner(t, panickingToolset(events.add))

	names := []string{"prepare_panic", "guard_panic", "handler_panic", "after_panic", "edited"}
	for _, n := range names {
		r.Call(context.Background(), n, nil)
	}

	ids := map[string]string{} // tool name -> call id, from the start events
	started, ended := map[string]int{}, map[string]int{}
	resulted := map[string]core.ToolResultMessage{}
	var errorEvents int
	for _, e := range events.all() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			ids[ev.Name] = ev.ToolUseID
			started[ev.ToolUseID]++
		case core.ToolExecutionEndEvent:
			ended[ev.ToolUseID]++
		case core.ToolResultEvent:
			resulted[ev.Message.ToolUseID] = ev.Message
		case core.ErrorEvent:
			errorEvents++
		}
	}
	for _, n := range names {
		id := ids[n]
		if id == "" || started[id] != 1 || ended[id] != 1 {
			t.Errorf("%s: id %q has %d start and %d end events, want one of each", n, id, started[id], ended[id])
		}
		if _, ok := resulted[id]; !ok {
			t.Errorf("%s: no ToolResultEvent", n)
		}
	}
	if errorEvents != 4 {
		t.Errorf("%d ErrorEvents, want 4: one for each of the prepare, guard, handler and After panics", errorEvents)
	}
	if got := resulted[ids["edited"]].Content.Text(); got != "edited by After" {
		t.Errorf("the ToolResultEvent for the edited call carries %q, want the message as After left it", got)
	}
}

// TS-21-37: a runner with a nil Events runs calls to the same results as one
// with a sink, and does not panic.
func TestNilEventsDiscardsEvents_TS21_37(t *testing.T) {
	tools := []core.Tool{echoTool("echo", nil), wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
		res, _ := core.CallNested(ctx, nestedCalls("child")...)
		return core.OKResult(map[string]any{"nested_ok": len(res) == 1 && res[0].OK})
	}, childTool("child"))}
	var events eventLog
	bare := mustRunner(t, Toolset{Tools: tools})
	logged := mustRunner(t, Toolset{Tools: tools, Events: events.add})

	for _, name := range []string{"echo", "wrapper", "missing"} {
		a := bare.Call(context.Background(), name, nil)
		b := logged.Call(context.Background(), name, nil)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("Call(%s): %+v without a sink, %+v with one", name, a, b)
		}
	}
	calls := []core.ToolUseBlock{toolUse(t, "a", "echo", `{}`), toolUse(t, "b", "wrapper", `{}`), toolUse(t, "c", "missing", `{}`)}
	if a, b := bare.Batch(context.Background(), calls...), logged.Batch(context.Background(), calls...); !reflect.DeepEqual(a, b) {
		t.Errorf("Batch: %+v without a sink, %+v with one", a, b)
	}
	if len(events.all()) == 0 {
		t.Error("the recording sink received nothing")
	}
}

// eventPositions is where each event of one call appears in a log.
type eventPositions struct{ start, end, result int }

// TS-21-38: for any concurrent batch with nested calls, every call's start
// event precedes its end event, and a top-level call's end event precedes its
// ToolResultEvent.
func TestEventOrderPerCall_TS21_38(t *testing.T) {
	rng := rand.New(rand.NewSource(38))
	for iter := range 30 {
		fan := 1 + rng.Intn(3)
		wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
			names := make([]string, fan)
			for i := range names {
				names[i] = "child"
			}
			_, _ = core.CallNested(ctx, nestedCalls(names...)...)
			return core.OKResult(nil)
		}, childTool("child"))
		var events eventLog
		r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", nil), wrapper}, Events: events.add})

		n := 2 + rng.Intn(5)
		calls := make([]core.ToolUseBlock, n)
		for i := range calls {
			name := "echo"
			if rng.Intn(2) == 0 {
				name = "wrapper"
			}
			calls[i] = toolUse(t, fmt.Sprintf("c%d", i), name, `{}`)
		}
		r.Batch(context.Background(), calls...)

		pos := map[string]*eventPositions{}
		at := func(id string) *eventPositions {
			if pos[id] == nil {
				pos[id] = &eventPositions{-1, -1, -1}
			}
			return pos[id]
		}
		for i, e := range events.all() {
			switch ev := e.(type) {
			case core.ToolExecutionStartEvent:
				at(ev.ToolUseID).start = i
			case core.ToolExecutionEndEvent:
				at(ev.ToolUseID).end = i
			case core.ToolResultEvent:
				at(ev.Message.ToolUseID).result = i
			}
		}
		top := map[string]bool{}
		for _, c := range calls {
			top[c.ID] = true
		}
		for id, p := range pos {
			if p.start < 0 || p.end < 0 || p.start > p.end {
				t.Fatalf("iteration %d: %s has start at %d and end at %d", iter, id, p.start, p.end)
			}
			if top[id] && (p.result < 0 || p.end > p.result) {
				t.Fatalf("iteration %d: top-level %s has end at %d and result at %d", iter, id, p.end, p.result)
			}
			if !top[id] && p.result >= 0 {
				t.Fatalf("iteration %d: nested %s has a ToolResultEvent", iter, id)
			}
		}
	}
}

// TS-21-39: a sink that panics on every event is recovered. Call returns the
// call's normal result, in a concurrent batch too, and the panic is not
// reported again: no ErrorEvent about it reaches the sink.
func TestPanickingEventsSinkIsRecovered_TS21_39(t *testing.T) {
	var mu sync.Mutex
	var seen []core.Event
	sink := func(e core.Event) {
		mu.Lock()
		seen = append(seen, e)
		mu.Unlock()
		panic("sink broke")
	}
	r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", nil)}, Events: sink})

	if res := r.Call(context.Background(), "echo", nil); !res.OK {
		t.Fatalf("Call = %+v, want the normal OK result", res)
	}
	res := r.Batch(context.Background(), toolUse(t, "a", "echo", `{}`), toolUse(t, "b", "echo", `{}`))
	if len(res) != 2 || !res[0].OK || !res[1].OK {
		t.Fatalf("Batch = %+v, want two OK results", res)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range seen {
		if _, ok := e.(core.ErrorEvent); ok {
			t.Fatalf("the sink's own panic was reported to it as %+v", e)
		}
	}
	// One Call and a batch of two: a start, an end and a result event each.
	if want := 3 + 2*3; len(seen) != want {
		t.Fatalf("the sink received %d events, want %d", len(seen), want)
	}
}

// TS-21-40: no runner-wide lock is held while Events runs, so a sink that
// calls Runner.Call completes, and (*core.EventStream).Push works as a sink
// under the race detector.
func TestEventsSinkMayReenterTheRunner_TS21_40(t *testing.T) {
	t.Run("a sink calls Runner.Call", func(t *testing.T) {
		var r *Runner
		var reentered atomic.Int32
		sink := func(e core.Event) {
			var name string
			switch ev := e.(type) {
			case core.ToolExecutionStartEvent:
				name = ev.Name
			case core.ToolExecutionEndEvent:
				name = ev.Name
			case core.ToolResultEvent:
				name = ev.Message.ToolName
			}
			if name == "first" {
				if res := r.Call(context.Background(), "second", nil); res.OK {
					reentered.Add(1)
				}
			}
		}
		wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
			_, _ = core.CallNested(ctx, nestedCalls("first")...)
			return core.OKResult(nil)
		}, echoTool("first", nil))
		r = mustRunner(t, Toolset{Tools: []core.Tool{echoTool("first", nil), echoTool("second", nil), wrapper}, Events: sink})

		done := make(chan struct{})
		go func() {
			defer close(done)
			r.Call(context.Background(), "first", nil)   // start, end and result events
			r.Call(context.Background(), "wrapper", nil) // nested start and end events
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatal("a sink that calls Runner.Call did not complete: a lock is held while Events runs")
		}
		if got := reentered.Load(); got != 5 {
			t.Fatalf("the sink re-entered the runner %d times, want 5 (three events of a top-level call, two of a nested one)", got)
		}
	})

	t.Run("EventStream.Push is a sink", func(t *testing.T) {
		s := core.NewEventStream(core.StreamOptions{})
		wrapper := wrapperTool("wrapper", func(ctx context.Context) core.ToolResult {
			_, _ = core.CallNested(ctx, nestedCalls("child")...)
			return core.OKResult(nil)
		}, childTool("child"))
		r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", nil), wrapper}, Events: s.Push})

		var wg sync.WaitGroup
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.Batch(context.Background(),
					toolUse(t, fmt.Sprintf("g%d_a", g), "echo", `{}`),
					toolUse(t, fmt.Sprintf("g%d_b", g), "wrapper", `{}`))
			}()
		}
		wg.Wait()
		s.End(core.StreamResult{})

		var starts, ends, results int
		for e := range s.Events() {
			switch e.(type) {
			case core.ToolExecutionStartEvent:
				starts++
			case core.ToolExecutionEndEvent:
				ends++
			case core.ToolResultEvent:
				results++
			}
		}
		// Four batches of an echo call and a wrapper that makes one nested call.
		if starts != 12 || ends != 12 || results != 8 {
			t.Fatalf("the stream received %d start, %d end and %d result events; want 12, 12 and 8", starts, ends, results)
		}
	})
}
