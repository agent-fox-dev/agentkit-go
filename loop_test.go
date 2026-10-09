package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// ---------------------------------------------------------------- scaffolding

const testAPI core.API = "test-api"

func testModel() *core.Model {
	return &core.Model{ID: "test-model", Name: "Test", API: testAPI, Provider: "test", ContextWindow: 100000, MaxTokens: 4096}
}

// scripted is a provider that replays a predetermined sequence of assistant
// messages, one per turn. It is the executable double the loop is tested
// against; provider/faux is the shipped, supported form of the same idea
// (NFR-TEST-05).
type scripted struct {
	mu    sync.Mutex
	turns []core.AssistantMessage
	calls int
	// seen records the message list each turn was asked to complete, so a test
	// can assert what the loop actually sent and when.
	seen []core.Messages
	// systems records the assembled system prompt per turn, so a test can
	// assert what the model was actually told rather than what a builder
	// returns in isolation.
	systems [][]core.ContentBlock
}

func (s *scripted) provider() core.APIProvider {
	return core.APIProvider{API: testAPI, Stream: s.stream}
}

func (s *scripted) stream(ctx context.Context, m *core.Model, req core.Request, _ core.ProviderStreamOptions) *core.EventStream {
	st := core.NewEventStream(core.StreamOptions{})
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.seen = append(s.seen, req.Messages)
	s.systems = append(s.systems, req.System)
	var msg core.AssistantMessage
	if i < len(s.turns) {
		msg = s.turns[i]
	} else {
		msg = core.AssistantMessage{
			Content:    core.Content{core.TextBlock{Text: "done"}},
			StopReason: core.StopReasonStop,
		}
	}
	s.mu.Unlock()

	msg.Provider, msg.API, msg.Model = m.Provider, m.API, m.ID
	go func() {
		st.Push(core.MessageStartEvent{Message: msg})
		st.Push(core.MessageEndEvent{Message: msg})
		st.End(core.StreamResult{Message: &msg})
	}()
	return st
}

func (s *scripted) sentAt(turn int) core.Messages {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn >= len(s.seen) {
		return nil
	}
	return s.seen[turn]
}

func (s *scripted) turnsRun() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

func toolUse(t *testing.T, id, name, args string) core.ToolUseBlock {
	t.Helper()
	b, err := core.NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("NewToolUse: %v", err)
	}
	return b
}

func assistantWithTools(reason core.StopReason, blocks ...core.ContentBlock) core.AssistantMessage {
	return core.AssistantMessage{Content: core.Content(blocks), StopReason: reason}
}

// afterTurns ends a run at the first turn boundary at or past n turns, with
// StopReason max_turns.
func afterTurns(n int) core.StopPolicy {
	return func(sc core.StopContext) bool {
		if sc.TurnCount >= n {
			sc.SetReason(core.RunStopMaxTurns)
			return true
		}
		return false
	}
}

// testModelID is not in the catalog, so it takes the default row.
const testModelID = "test-model"

func newTestAgent(t *testing.T, s *scripted, mutate func(*Config), tools ...core.Tool) *Agent {
	t.Helper()
	cfg := Config{
		Provider: core.ClientFunc(s.stream),
		Model:    testModelID,
		Tools:    tools,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func oneToolTurn(t *testing.T) *scripted {
	t.Helper()
	return &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "echo", `{"v":"x"}`)),
		{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
	}}
}

func findToolResult(t *testing.T, msgs core.Messages, id string) core.ToolResultMessage {
	t.Helper()
	for _, m := range msgs {
		if r, ok := m.(core.ToolResultMessage); ok && r.ToolUseID == id {
			return r
		}
	}
	t.Fatalf("no tool result for %q in %d messages", id, len(msgs))
	return core.ToolResultMessage{}
}

func echoTool(name string, calls *atomic.Int32) core.Tool {
	return core.Tool{
		Name:        name,
		Description: "echo",
		InputSchema: schema.Object(schema.Opt("v", schema.String())),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			if calls != nil {
				calls.Add(1)
			}
			return json.RawMessage(`{"echoed":true}`), nil
		},
	}
}

// ------------------------------------------------------------------- REQ-LOOP-01

// TestIterationOnToolUsePresenceNotStopReason is the single most important
// test in the suite. Gemini and several OpenAI-compatible gateways return a
// STOP-family finish reason ALONGSIDE tool calls. A loop that gates iteration
// on stop_reason drops those calls silently and returns an empty answer — and
// passes every Anthropic-only test, because Anthropic does set "tool_use".
//
// Every row here carries tool calls. Every row must execute them.
func TestIterationOnToolUsePresenceNotStopReason(t *testing.T) {
	for _, reason := range []core.StopReason{
		core.StopReasonStop,         // Gemini's STOP alongside functionCall
		core.StopReasonToolUse,      // Anthropic's own
		core.StopReasonStopSequence, //
		"",                          // a gateway that emits nothing
		"finish_reason_unset",       // an unrecognized reason
	} {
		t.Run(string("reason="+reason), func(t *testing.T) {
			var handlerCalls atomic.Int32
			s := &scripted{turns: []core.AssistantMessage{
				assistantWithTools(reason, toolUse(t, "c1", "echo", `{"v":"x"}`)),
				{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
			}}
			a := newTestAgent(t, s, nil, echoTool("echo", &handlerCalls))
			if _, err := a.Run(context.Background(), "go"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := handlerCalls.Load(); got != 1 {
				t.Fatalf("stop_reason %q: handler ran %d times, want 1.\n"+
					"The loop gated iteration on stop_reason instead of on the presence "+
					"of tool_use blocks (REQ-LOOP-01).", reason, got)
			}
		})
	}
}

// TestErrorAndAbortedShortCircuitBeforeToolExtraction pins the other half of
// REQ-LOOP-01: Error and Aborted are the ONLY reasons that short-circuit, and
// they do so before tools are extracted.
func TestErrorAndAbortedShortCircuitBeforeToolExtraction(t *testing.T) {
	for _, reason := range []core.StopReason{core.StopReasonError, core.StopReasonAborted} {
		t.Run(string(reason), func(t *testing.T) {
			var handlerCalls atomic.Int32
			s := &scripted{turns: []core.AssistantMessage{
				assistantWithTools(reason, toolUse(t, "c1", "echo", `{}`)),
			}}
			a := newTestAgent(t, s, nil, echoTool("echo", &handlerCalls))
			_, err := a.Run(context.Background(), "go")
			if err == nil {
				t.Fatal("want an error for a short-circuiting stop reason")
			}
			if handlerCalls.Load() != 0 {
				t.Fatal("tools were extracted and run despite a short-circuiting stop reason")
			}
		})
	}
}

// ------------------------------------------------------------------- REQ-LOOP-02

// TestOneToolResultMessagePerCallInSlotOrder pins that canonical history
// carries one ToolResultMessage per call, in the order the calls appeared —
// not one user message holding all of them (which is an Anthropic wire rule),
// and not in completion order.
func TestOneToolResultMessagePerCallInSlotOrder(t *testing.T) {
	// Handlers finish in REVERSE call order, so a design that appends on
	// completion rather than by slot index produces the wrong order.
	slow := func(name string, d time.Duration) core.Tool {
		return core.Tool{
			Name: name, Description: name,
			InputSchema: schema.Object(),
			Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
				time.Sleep(d)
				return json.RawMessage(`{}`), nil
			},
		}
	}
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "slow", `{}`),
			toolUse(t, "c2", "mid", `{}`),
			toolUse(t, "c3", "fast", `{}`),
		),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil, slow("slow", 60*time.Millisecond), slow("mid", 30*time.Millisecond), slow("fast", time.Millisecond))

	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var ids []string
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok {
			ids = append(ids, tr.ToolUseID)
		}
	}
	want := []string{"c1", "c2", "c3"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("tool result order = %v, want %v (slot order, not completion order — REQ-LOOP-05)", ids, want)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ToolResultMessages, want 3 — one per call (REQ-LOOP-02)", len(ids))
	}
}

// ------------------------------------------------------------------- REQ-LOOP-10

// TestMaxTokensWithToolCallsExecutesZeroHandlers pins the failure that
// silently corrupts files: streamed arguments are salvage-repaired into valid
// JSON, so a truncated edit passes schema validation and applies cleanly. Only
// the stop reason can catch it, and the response is to execute NOTHING while
// still producing a well-formed result for every call.
func TestMaxTokensWithToolCallsExecutesZeroHandlers(t *testing.T) {
	var handlerCalls atomic.Int32
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonLength,
			toolUse(t, "c1", "echo", `{"v":"a"}`),
			toolUse(t, "c2", "echo", `{"v":"b"}`),
		),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil, echoTool("echo", &handlerCalls))

	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := handlerCalls.Load(); got != 0 {
		t.Fatalf("handlers ran %d times on a max_tokens turn, want 0 (REQ-LOOP-10)", got)
	}

	var results []core.ToolResultMessage
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok {
			results = append(results, tr)
		}
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 — every call in the batch still needs one", len(results))
	}
	for _, r := range results {
		if !r.IsError {
			t.Error("a synthesized max_tokens result must be an error result")
		}
		if !strings.Contains(r.Content.Text(), "output token limit") {
			t.Errorf("result text does not carry the pinned REQ-LOOP-10 message: %q", r.Content.Text())
		}
	}
	// The loop must CONTINUE so the model can re-issue.
	if s.turnsRun() < 2 {
		t.Fatalf("loop ran %d turns; a max_tokens turn with tool calls must not terminate the run", s.turnsRun())
	}
}

// ------------------------------------------------------------------- REQ-LOOP-11

// TestBatchAbortIsAllOrNothing pins that a cancelled batch runs NO handler,
// rather than whichever ones the scheduler happened to start. Per-goroutine
// ctx.Err() checks — the obvious Go idiom — let the scheduler split the batch,
// which shows up in production as phantom side effects after Ctrl-C.
//
// Repeated, because a split batch is a race: a single run proves nothing.
func TestBatchAbortIsAllOrNothing(t *testing.T) {
	const runs = 200
	for i := 0; i < runs; i++ {
		var ran atomic.Int32
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "c1", "echo", `{}`),
				toolUse(t, "c2", "echo", `{}`),
				toolUse(t, "c3", "echo", `{}`),
				toolUse(t, "c4", "echo", `{}`),
			),
		}}
		ctx, cancel := context.WithCancel(context.Background())
		a := newTestAgent(t, s, nil, echoTool("echo", &ran))
		cancel() // already cancelled when the batch is reached
		_, _ = a.Run(ctx, "go")

		if got := ran.Load(); got != 0 {
			t.Fatalf("run %d: %d handlers ran under a cancelled context, want 0.\n"+
				"The abort decision must be made ONCE on the loop goroutine before any "+
				"handler starts, not re-checked per goroutine (REQ-LOOP-11).", i, got)
		}
	}
}

// TestAbortDuringBatchDoesNotSplitIt is the test that actually discriminates.
//
// Cancelling BEFORE the batch is the easy case: every implementation runs zero
// handlers. The bug REQ-LOOP-11 exists to prevent is a batch SPLIT — the abort
// landing while the batch is in progress, so whichever calls had already been
// scheduled run and the rest do not. That is nondeterministic in production
// and shows up as phantom side effects after the user pressed Ctrl-C.
//
// The first handler cancels the context itself, so the abort lands strictly
// after the batch has started. Because the decision was made ONCE, before any
// handler ran, all three must still run. An implementation that re-checks
// ctx.Err() per call — the obvious Go idiom, and what REQ-GO-05 alone implies
// — runs the first and aborts the rest.
func TestAbortDuringBatchDoesNotSplitIt(t *testing.T) {
	var ran atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "cancels", `{}`),
			toolUse(t, "c2", "echo", `{}`),
			toolUse(t, "c3", "echo", `{}`),
		),
	}}
	// Sequential execution makes the ordering deterministic: thunk 1 runs to
	// completion (cancelling as it goes) before thunk 2 is reached.
	a := newTestAgent(t, s, nil, core.Tool{
		Name: "cancels", Description: "cancels the run", InputSchema: schema.Object(),
		ExecutionMode: core.Sequential,
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			ran.Add(1)
			cancel()
			return json.RawMessage(`{}`), nil
		},
	}, echoTool("echo", &ran))

	_, _ = a.Run(ctx, "go")

	if got := ran.Load(); got != 3 {
		t.Fatalf("%d of 3 handlers ran after an abort landed mid-batch, want all 3.\n"+
			"The batch was SPLIT: the abort decision is being re-checked per call "+
			"instead of being made once, on the loop goroutine, before any handler "+
			"starts (REQ-LOOP-11).", got)
	}
}

// TestAbortedBatchStillProducesAResultPerCall: an aborted call still emits its
// events and a result, so the transcript stays well-formed and resumable.
func TestAbortedBatchStillProducesAResultPerCall(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "echo", `{}`), toolUse(t, "c2", "echo", `{}`)),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	a := newTestAgent(t, s, nil, echoTool("echo", nil))
	cancel()
	res, _ := a.Run(ctx, "go")

	n := 0
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok {
			n++
			if !tr.IsError {
				t.Error("an aborted call must yield an error result")
			}
		}
	}
	if n != 2 {
		t.Fatalf("got %d results for an aborted batch of 2, want 2 (REQ-LOOP-11.3)", n)
	}
}

// ------------------------------------------------------------------- REQ-LOOP-05

// TestOneSequentialToolDemotesTheWholeBatch pins REQ-LOOP-05a. It detects
// concurrency directly: a parallel batch would observe overlap.
func TestOneSequentialToolDemotesTheWholeBatch(t *testing.T) {
	var inFlight, maxInFlight atomic.Int32
	mk := func(name string, mode core.ExecutionMode) core.Tool {
		return core.Tool{
			Name: name, Description: name, InputSchema: schema.Object(), ExecutionMode: mode,
			Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
				cur := inFlight.Add(1)
				for {
					old := maxInFlight.Load()
					if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				inFlight.Add(-1)
				return json.RawMessage(`{}`), nil
			},
		}
	}
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "par1", `{}`),
			toolUse(t, "c2", "seq", `{}`),
			toolUse(t, "c3", "par2", `{}`),
		),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil, mk("par1", core.Parallel), mk("seq", core.Sequential), mk("par2", core.Parallel))

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := maxInFlight.Load(); got != 1 {
		t.Fatalf("max concurrent handlers = %d, want 1: one Sequential tool must demote "+
			"the WHOLE batch (REQ-LOOP-05a)", got)
	}
}

// TestPanickingAfterToolCallDoesNotDeadlockPeers pins NFR-REL-02.1 with a hard
// deadline. A panicking interceptor inside a manual Lock/emit/Unlock leaks the
// mutex and hangs every peer at the join — a DEADLOCK, not a crash, so it
// produces no stack trace and no error. A test without a deadline would hang
// the suite instead of failing it.
func TestPanickingAfterToolCallDoesNotDeadlockPeers(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "echo", `{}`), toolUse(t, "c2", "echo", `{}`),
			toolUse(t, "c3", "echo", `{}`), toolUse(t, "c4", "echo", `{}`)),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.After = func(ctx context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			panic("interceptor exploded")
		}
	}, echoTool("echo", nil))

	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.Run(context.Background(), "go") }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish within 5s: a panicking AfterToolCall leaked the " +
			"finalize mutex and deadlocked the peer tool goroutines (NFR-REL-02.1)")
	}
}

// TestPanickingHandlerBecomesErrorResult: NFR-REL-02.2.
func TestPanickingHandlerBecomesErrorResult(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "boom", `{}`)),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil, core.Tool{
		Name: "boom", Description: "panics", InputSchema: schema.Object(),
		Handler: func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
			panic("handler exploded")
		},
	})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("a panicking handler must not fail the run: %v", err)
	}
	found := false
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.IsError {
			found = true
		}
	}
	if !found {
		t.Fatal("a panicking handler must become a tool result with is_error set")
	}
}

// ------------------------------------------------------------------- REQ-LOOP-15

// TestConcurrentRunReturnsErrBusy pins REQ-LOOP-15: conflicting operations
// fail rather than queue, and never block.
func TestConcurrentRunReturnsErrBusy(t *testing.T) {
	b := &blocking{started: make(chan struct{})}
	a := b.agent(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = a.Run(ctx, "first") }()

	// The first run holds the slot until its provider call is cancelled.
	<-b.started
	_, err := a.Run(context.Background(), "second")
	cancel()
	<-done
	if !errors.Is(err, core.ErrBusy) {
		t.Fatalf("second Run returned %v, want ErrBusy", err)
	}
}

// ------------------------------------------------------------------- REQ-TOOL-13

func TestBatchTerminationIsAnAndNotAnOr(t *testing.T) {
	finish := func(name string, terminate bool) core.Tool {
		return core.Tool{
			Name: name, Description: name, InputSchema: schema.Object(),
			Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
				r := core.OKResult(map[string]any{"n": name})
				r.Terminate = terminate
				return r
			},
		}
	}
	t.Run("one terminating tool does not end a mixed batch", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "c1", "finish", `{}`), toolUse(t, "c2", "keep", `{}`)),
			{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
		}}
		a := newTestAgent(t, s, nil, finish("finish", true), finish("keep", false))
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		if res.StopReason == core.RunStopToolTerminate {
			t.Fatal("a single terminating tool ended a mixed batch: the vote is an AND, " +
				"not an OR (REQ-TOOL-13.1). Under OR the other results are computed, " +
				"written to history, and never shown to the model.")
		}
	})

	t.Run("a unanimous batch terminates", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "c1", "finish", `{}`), toolUse(t, "c2", "finish", `{}`)),
		}}
		a := newTestAgent(t, s, nil, finish("finish", true))
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		if res.StopReason != core.RunStopToolTerminate {
			t.Fatalf("StopReason = %q, want %q", res.StopReason, core.RunStopToolTerminate)
		}
	})

	t.Run("an empty batch never terminates", func(t *testing.T) {
		if core.BatchTerminates(nil) {
			t.Fatal("an empty batch must not terminate")
		}
	})
}

// ------------------------------------------------------------------- REQ-SEC-03

func TestBlockedCallProducesAnErrorResultAndTheLoopContinues(t *testing.T) {
	var ran atomic.Int32
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "echo", `{}`)),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.Guard = func(ctx context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Block: true, Reason: "not allowed here"}
		}
	}, echoTool("echo", &ran))
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Fatal("a blocked call must not reach the handler")
	}
	found := false
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.IsError &&
			strings.Contains(tr.Content.Text(), "not allowed here") {
			found = true
		}
	}
	if !found {
		t.Fatal("a blocked call must produce an error result carrying the policy's reason")
	}
}

// TestPanickingInterceptorFailsClosed: a security boundary that opens on panic
// is not a boundary.
func TestPanickingInterceptorFailsClosed(t *testing.T) {
	var ran atomic.Int32
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "echo", `{}`)),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, func(c *Config) {
		c.Guard = func(ctx context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			panic("policy exploded")
		}
	}, echoTool("echo", &ran))
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Fatal("a panicking BeforeToolCall must fail CLOSED: the handler ran anyway")
	}
}

// ------------------------------------------------------------------- REQ-GO-08

func TestStreamResultAvailableWithoutReadingAnyEvent(t *testing.T) {
	// REQ-GO-08: the result is fed by the terminal event, not by consumption,
	// and that is what makes abandoning a stream safe.
	s := &scripted{turns: []core.AssistantMessage{
		{Content: core.Content{core.TextBlock{Text: "hello"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil)
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	res, err := st.RunResult() // no event ever read
	if err != nil {
		t.Fatal(err)
	}
	if res.FinalText() != "hello" {
		t.Fatalf("FinalText = %q", res.FinalText())
	}
}

func TestUnknownToolYieldsAnErrorResultNotACrash(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "nope", `{}`)),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil)
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.IsError &&
			strings.Contains(tr.Content.Text(), "nope") {
			found = true
		}
	}
	if !found {
		t.Fatal("an unknown tool must produce an error result the model can see")
	}
}

var _ = fmt.Sprintf

func user(s string) core.Message {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

func assistantSaying(s string, tokens int64) core.Message {
	m := core.AssistantMessage{
		Content:    core.Content{core.TextBlock{Text: s}},
		StopReason: core.StopReasonStop,
	}
	if tokens > 0 {
		m.Usage.SetField(core.UsageInputTokens, tokens)
	}
	return m
}
