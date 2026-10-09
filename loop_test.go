package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// ---------------------------------------------------------------- scaffolding

const testAPI core.API = "test-api"

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

// TestBatchTerminationIsAnyExecutedVote: one executed call voting
// Terminate ends the run once the batch is done (11-REQ-8.1).
func TestBatchTerminationIsAnyExecutedVote(t *testing.T) {
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
	t.Run("one terminating tool ends a mixed batch", func(t *testing.T) {
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
		if res.StopReason != core.RunStopToolTerminate || s.turnsRun() != 1 {
			t.Fatalf("stop %q after %d turns; one executed terminate vote ends the run", res.StopReason, s.turnsRun())
		}
		if len(res.Messages) != 4 {
			t.Fatalf("%d messages; both results belong in the transcript", len(res.Messages))
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

// ------------------------------------------------------------------- 11-REQ-4

// TS-11-15: Config.Prefix is sent ahead of the prompt on every request.
func TestPrefixPrecedesThePrompt_TS11_15(t *testing.T) {
	prefix := []core.Message{
		core.UserMessage{Content: core.Content{core.TextBlock{Text: "Preamble 1"}}},
		core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "Acknowledged"}}},
	}
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c1", "echo", `{}`)),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")),
	)
	a, err := New(Config{Provider: fp, Model: testModelID, Prefix: prefix, Tools: []core.Tool{echoTool("echo", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "user question"); err != nil {
		t.Fatal(err)
	}
	reqs := fp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		if !reflect.DeepEqual(req.Prefix, core.Messages(prefix)) {
			t.Fatalf("request %d prefix = %v, want Config.Prefix", i, req.Prefix)
		}
		first, ok := req.Messages[0].(core.UserMessage)
		if !ok || first.Content.Text() != "user question" {
			t.Fatalf("request %d: first message after the prefix = %v, want the prompt", i, req.Messages[0])
		}
	}
	// The prefix is sent, never recorded.
	if got := a.Messages()[0]; !reflect.DeepEqual(got.(core.UserMessage).Content.Text(), "user question") {
		t.Fatalf("transcript starts with %v, want the prompt", got)
	}
}

// TS-11-16: the wire request carries breakpoints on the last system block,
// the last tool, the last prefix block and the last user block.
func TestFourCacheBreakpoints_TS11_16(t *testing.T) {
	fp := faux.New(faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")))
	a, err := New(Config{
		Provider: fp,
		Model:    "claude-opus-5-5",
		System:   "System instructions",
		Tools:    []core.Tool{echoTool("toolA", nil), echoTool("toolB", nil)},
		Prefix: []core.Message{
			core.UserMessage{Content: core.Content{core.TextBlock{Text: "prefix text"}}},
			core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "noted"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "user prompt"); err != nil {
		t.Fatal(err)
	}
	m, _ := catalog.Lookup("claude-opus-5-5")
	body, err := anthropic.BuildRequestJSON(fp.Requests()[0], m)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		System []map[string]any `json:"system"`
		Tools  []map[string]any `json:"tools"`
		Msgs   []struct {
			Role    string           `json:"role"`
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	ephemeral := func(b map[string]any) bool {
		cc, _ := b["cache_control"].(map[string]any)
		return cc["type"] == "ephemeral"
	}
	if len(wire.System) == 0 || !ephemeral(wire.System[len(wire.System)-1]) {
		t.Errorf("last system block has no breakpoint: %s", body)
	}
	if len(wire.Tools) != 2 || !ephemeral(wire.Tools[1]) {
		t.Errorf("last tool has no breakpoint: %s", body)
	}
	// messages: [user prefix, assistant prefix, user prompt]
	if len(wire.Msgs) != 3 || wire.Msgs[1].Role != "assistant" || !ephemeral(wire.Msgs[1].Content[len(wire.Msgs[1].Content)-1]) {
		t.Errorf("last prefix block has no breakpoint: %s", body)
	}
	last := wire.Msgs[len(wire.Msgs)-1]
	if last.Role != "user" || !ephemeral(last.Content[len(last.Content)-1]) {
		t.Errorf("last user block has no breakpoint: %s", body)
	}
}

// TS-11-14: the system prompt is Config.System followed by the active
// tools' guidelines, deduplicated in first-seen order.
func TestSystemPromptDeduplicatesGuidelines_TS11_14(t *testing.T) {
	t1 := core.Tool{Name: "t1", Handler: noopHandler, PromptGuidelines: []string{"Rule A", "Rule B"}}
	t2 := core.Tool{Name: "t2", Handler: noopHandler, PromptGuidelines: []string{"Rule B", "Rule C"}}
	sys := prompt.Build("Base instruction", []core.Tool{t1, t2})
	if !strings.HasPrefix(sys, "Base instruction") {
		t.Fatalf("prompt does not start with Config.System:\n%s", sys)
	}
	a, b, c := strings.Index(sys, "Rule A"), strings.Index(sys, "Rule B"), strings.Index(sys, "Rule C")
	if a < 0 || !(a < b && b < c) || strings.Count(sys, "Rule B") != 1 {
		t.Fatalf("guidelines not deduplicated in first-seen order:\n%s", sys)
	}
	// And it is what the provider is sent.
	fp := faux.New()
	ag, err := New(Config{Provider: fp, Model: testModelID, System: "Base instruction", Tools: []core.Tool{t1, t2}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := fp.Requests()[0].System; len(got) != 1 || got[0].(core.TextBlock).Text != sys {
		t.Fatalf("system sent = %v, want the assembled prompt", got)
	}
}

// ------------------------------------------------------------------- 11-REQ-5

// TS-11-17: PruneOptions carries the threshold and the turns kept whole.
func TestPruneOptionsFields_TS11_17(t *testing.T) {
	opts := PruneOptions{Threshold: 0.35, KeepTurns: 2}
	if opts.Threshold != 0.35 || opts.KeepTurns != 2 {
		t.Fatalf("opts = %+v", opts)
	}
}

// bigResult is a tool whose result text is n bytes long.
func bigResult(name string, n int) core.Tool {
	return core.Tool{Name: name, InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.ToolResult{OK: true, Text: strings.Repeat("x", n)}
		}}
}

// heavyTurn is a tool-call turn whose usage says the context is already
// large, so the next request's anchored estimate is too.
func heavyTurn(tokens int64, calls ...core.ContentBlock) faux.Turn {
	return faux.Turn{Blocks: calls, StopReason: core.StopReasonToolUse, Usage: core.Usage{InputTokens: tokens}}
}

func toolResultIn(msgs core.Messages, id string) (core.ToolResultMessage, bool) {
	for _, m := range msgs {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == id {
			return tr, true
		}
	}
	return core.ToolResultMessage{}, false
}

// TS-11-18: past the threshold, results older than KeepTurns are elided in
// the request; recent ones are sent whole.
func TestPruningElidesOldToolResults_TS11_18(t *testing.T) {
	fp := faux.New(
		heavyTurn(400_000, faux.FauxToolCall("call_1", "toolA", `{}`)),
		heavyTurn(400_000, faux.FauxToolCall("call_2", "toolA", `{}`)),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{bigResult("toolA", 1500)},
		Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "run query"); err != nil {
		t.Fatal(err)
	}
	reqs := fp.Requests()
	last := reqs[len(reqs)-1].Messages
	old, ok := toolResultIn(last, "call_1")
	if !ok || old.Content.Text() != "[result of toolA (1500 bytes) elided; call again if needed]" {
		t.Fatalf("call_1 sent as %q, want the elision notice", old.Content.Text())
	}
	recent, ok := toolResultIn(last, "call_2")
	if !ok || recent.Content.Text() != strings.Repeat("x", 1500) {
		t.Fatalf("call_2 sent as %q, want it whole", recent.Content.Text())
	}
	// Below the threshold nothing is pruned: the second request's estimate
	// is anchored at 400k of a 1M window too, but there is no older turn.
	if tr, _ := toolResultIn(reqs[1].Messages, "call_1"); tr.Content.Text() != strings.Repeat("x", 1500) {
		t.Fatalf("call_1 was pruned while it was within KeepTurns: %q", tr.Content.Text())
	}
}

// TS-11-19: whatever is pruned, every result in a request still pairs with
// the tool_use before it.
func TestPruningKeepsToolUsePairing_TS11_19(t *testing.T) {
	r := rand.New(rand.NewSource(19))
	for trial := 0; trial < 10; trial++ {
		var turns []faux.Turn
		n := 2 + r.Intn(5)
		for i := 0; i < n; i++ {
			var calls []core.ContentBlock
			for j := 0; j <= r.Intn(3); j++ {
				calls = append(calls, faux.FauxToolCall(fmt.Sprintf("t%d_c%d", i, j), "toolA", `{}`))
			}
			turns = append(turns, heavyTurn(400_000, calls...))
		}
		fp := faux.New(turns...)
		a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{bigResult("toolA", 100+r.Intn(2000))},
			Prune: PruneOptions{Threshold: 0.35, KeepTurns: r.Intn(3)}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		elided := 0
		for _, req := range fp.Requests() {
			pending := map[string]bool{}
			for _, m := range req.Messages {
				switch v := m.(type) {
				case core.AssistantMessage:
					for _, b := range v.Content {
						if tu, ok := b.(core.ToolUseBlock); ok {
							pending[tu.ID] = true
						}
					}
				case core.ToolResultMessage:
					if !pending[v.ToolUseID] {
						t.Fatalf("trial %d: result %s has no tool_use before it", trial, v.ToolUseID)
					}
					delete(pending, v.ToolUseID)
					if strings.Contains(v.Content.Text(), "elided") {
						elided++
					}
				}
			}
			if len(pending) > 0 {
				t.Fatalf("trial %d: tool_use without a result: %v", trial, pending)
			}
		}
		if n > 2 && elided == 0 {
			t.Fatalf("trial %d: %d turns and nothing was pruned", trial, n)
		}
	}
}

// TS-11-20: pruning changes the request, never the transcript.
func TestPruningLeavesTheTranscriptIntact_TS11_20(t *testing.T) {
	fp := faux.New(
		heavyTurn(400_000, faux.FauxToolCall("c1", "toolA", `{}`)),
		heavyTurn(400_000, faux.FauxToolCall("c2", "toolA", `{}`)),
		heavyTurn(400_000, faux.FauxToolCall("c3", "toolA", `{}`)),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{bigResult("toolA", 800)},
		Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "long workflow")
	if err != nil {
		t.Fatal(err)
	}
	if tr, _ := toolResultIn(fp.Requests()[3].Messages, "c1"); !strings.Contains(tr.Content.Text(), "elided") {
		t.Fatal("nothing was pruned; the test proves nothing")
	}
	for _, m := range a.Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.Content.Text() != strings.Repeat("x", 800) {
			t.Fatalf("transcript result %s = %q, want it whole", tr.ToolUseID, tr.Content.Text())
		}
	}
	if !reflect.DeepEqual(a.Messages(), res.Messages) {
		t.Fatal("RunResult.Messages differs from the transcript")
	}
}

// TS-11-21: a request still larger than the context window after pruning
// ends the run with an error naming the model and the window.
func TestContextOverflowEndsTheRun_TS11_21(t *testing.T) {
	fp := faux.New(
		heavyTurn(2_000_000, faux.FauxToolCall("c1", "toolA", `{}`)),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("never")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{bigResult("toolA", 10)},
		Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "prompt")
	if res.StopReason != core.RunStopError || res.Error == nil || !errors.Is(err, res.Error) {
		t.Fatalf("result = %q, %v; want an error stop", res.StopReason, res.Error)
	}
	if msg := res.Error.Error(); !strings.Contains(msg, driverModel) || !strings.Contains(msg, "context window") {
		t.Fatalf("error %q does not name the model and its context window", msg)
	}
	if fp.Calls() != 1 {
		t.Fatalf("%d requests; the oversized one must not be sent", fp.Calls())
	}
}

// ------------------------------------------------------------------- 11-REQ-6

// TS-11-22: tool_use blocks, not the stop reason, decide whether the run
// goes on.
func TestContinuationIsToolUsePresence_TS11_22(t *testing.T) {
	var ran atomic.Int32
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxToolCall("c1", "toolA", "{}")),
		faux.FauxAssistantMessage(core.StopReasonLength, faux.FauxText("truncated text")),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("never requested")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{echoTool("toolA", &ran)}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "test continuation")
	if err != nil {
		t.Fatal(err)
	}
	if res.TurnCount != 2 || ran.Load() != 1 || fp.Calls() != 2 {
		t.Fatalf("turns = %d, handler ran %d, requests %d; want 2, 1, 2", res.TurnCount, ran.Load(), fp.Calls())
	}
}

// TS-11-23: a truncated response's calls are not run; each gets the fixed
// notice and the run goes on.
func TestTruncatedCallsGetTheNotice_TS11_23(t *testing.T) {
	var ran atomic.Int32
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonLength, faux.FauxToolCall("call_1", "my_tool", `{"partial":1}`)),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("finished")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{echoTool("my_tool", &ran)}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "trigger")
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Fatal("a truncated call ran")
	}
	want := "Tool call \"my_tool\" was not executed: the response hit the output token limit,\n" +
		"so its arguments may be truncated. Re-issue the tool call with complete arguments."
	tr := findToolResult(t, res.Messages, "call_1")
	if tr.Content.Text() != want || !tr.IsError {
		t.Fatalf("notice = %q (error %v), want %q", tr.Content.Text(), tr.IsError, want)
	}
	if res.TurnCount != 2 || res.StopReason != core.RunStopEndTurn {
		t.Fatalf("run = %q after %d turns, want end_turn after 2", res.StopReason, res.TurnCount)
	}
}

// TS-11-24: a refusal with no tool calls ends the run as a refusal.
func TestRefusalEndsTheRun_TS11_24(t *testing.T) {
	fp := faux.New(faux.FauxAssistantMessage(core.StopReasonRefusal, faux.FauxText("I cannot fulfill this request")))
	a, err := New(Config{Provider: fp, Model: driverModel})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "unsafe prompt")
	if res.StopReason != core.RunStopRefusal || !errors.Is(res.Error, core.ErrRefusal) || !errors.Is(err, core.ErrRefusal) {
		t.Fatalf("run = %q, %v / %v; want refusal wrapping ErrRefusal", res.StopReason, res.Error, err)
	}
}

// TS-11-25: an answer with no tool calls ends the run normally.
func TestAnswerEndsTheRun_TS11_25(t *testing.T) {
	fp := faux.New(faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("Answer completed.")))
	a, err := New(Config{Provider: fp, Model: driverModel})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "regular query")
	if err != nil || res.StopReason != core.RunStopEndTurn || res.Error != nil {
		t.Fatalf("run = %q, %v / %v; want end_turn and no error", res.StopReason, res.Error, err)
	}
}

// ------------------------------------------------------------------ 11-REQ-10

// TS-11-38: Config.Timeout ends the run with RunStopTimeout.
func TestTimeoutEndsTheRun_TS11_38(t *testing.T) {
	fp := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("slow turn")}, StopReason: core.StopReasonStop, Delay: 2 * time.Second})
	a, err := New(Config{Provider: fp, Model: driverModel, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err := a.Run(context.Background(), "test timeout")
	if res.StopReason != core.RunStopTimeout || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("run = %q, %v; want timeout wrapping DeadlineExceeded", res.StopReason, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the run outlived its timeout")
	}
}

// TS-11-39: MaxTurns ends a run that would go on, with its results in the
// transcript.
func TestMaxTurnsEndsTheRun_TS11_39(t *testing.T) {
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c1", "toolA", "{}")),
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c2", "toolA", "{}")),
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c3", "toolA", "{}")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, MaxTurns: 2, Tools: []core.Tool{echoTool("toolA", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "test max turns")
	if res.StopReason != core.RunStopMaxTurns || !errors.Is(res.Error, core.ErrMaxTurns) || !errors.Is(err, core.ErrMaxTurns) {
		t.Fatalf("run = %q, %v; want max_turns wrapping ErrMaxTurns", res.StopReason, res.Error)
	}
	if res.TurnCount != 2 || fp.Calls() != 2 {
		t.Fatalf("%d turns, %d requests; want 2 and 2", res.TurnCount, fp.Calls())
	}
	if _, ok := res.Messages[len(res.Messages)-1].(core.ToolResultMessage); !ok {
		t.Fatal("the run stopped before the last turn's results were recorded")
	}
}

// TS-11-40: MaxCostUSD stops the run before a request once the run's cost
// reaches it.
func TestBudgetStopsBeforeTheRequest_TS11_40(t *testing.T) {
	fp := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("c1", "toolA", "{}")}, StopReason: core.StopReasonToolUse,
			Usage: core.Usage{InputTokens: 100_000, OutputTokens: 100_000}},
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("t2")),
	)
	a, err := New(Config{Provider: fp, Model: "claude-opus-5-5", MaxCostUSD: 0.05, Tools: []core.Tool{echoTool("toolA", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "test budget")
	if res.StopReason != core.RunStopBudgetExceeded || !errors.Is(res.Error, core.ErrBudgetExceeded) || !errors.Is(err, core.ErrBudgetExceeded) {
		t.Fatalf("run = %q, %v; want budget_exceeded wrapping ErrBudgetExceeded", res.StopReason, res.Error)
	}
	if fp.Calls() != 1 || res.Usage.CostUSD < 0.05 {
		t.Fatalf("%d requests at $%.4f; want the second request not sent", fp.Calls(), res.Usage.CostUSD)
	}
}

// TS-11-41: a caller cancelling between turns aborts the run with
// ErrAborted.
func TestExternalCancelAbortsTheRun_TS11_41(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tool := core.Tool{Name: "cancelTool", InputSchema: schema.Object(),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			cancel()
			return json.RawMessage(`{}`), nil
		}}
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c1", "cancelTool", "{}")),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("should not reach")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(ctx, "test abort")
	if res.StopReason != core.RunStopAborted || !errors.Is(res.Error, core.ErrAborted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %q, %v; want aborted wrapping ErrAborted and the context's error", res.StopReason, res.Error)
	}
	if fp.Calls() != 1 {
		t.Fatalf("%d requests after the cancel, want 1", fp.Calls())
	}
}

// TS-11-42: a panicking handler becomes an error result, the run goes on,
// and the panic is reported on the stream.
func TestHandlerPanicIsContained_TS11_42(t *testing.T) {
	tool := core.Tool{Name: "panickyTool", InputSchema: schema.Object(),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { panic("unexpected explosion") }}
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c1", "panickyTool", "{}")),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("recovered")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Stream(context.Background(), "trigger panic")
	if err != nil {
		t.Fatal(err)
	}
	var reported []string
	for e := range st.Events() {
		if ev, ok := e.(core.ErrorEvent); ok {
			reported = append(reported, ev.Message)
		}
	}
	res, err := st.RunResult()
	if err != nil || res.StopReason != core.RunStopEndTurn {
		t.Fatalf("run = %q, %v; a panicking handler must not end the run", res.StopReason, err)
	}
	tr := findToolResult(t, res.Messages, "c1")
	if !tr.IsError || !strings.Contains(tr.Content.Text(), "panicked: unexpected explosion") {
		t.Fatalf("result = %q, want an error describing the panic", tr.Content.Text())
	}
	if len(reported) != 1 || !strings.Contains(reported[0], "unexpected explosion") {
		t.Fatalf("error events = %v, want the panic reported once", reported)
	}
}

// TS-11-43: a consumer that never reads the stream does not hold up the
// run.
func TestUnreadStreamDoesNotBlockTheRun_TS11_43(t *testing.T) {
	var turns []faux.Turn
	for i := 0; i < 20; i++ {
		turns = append(turns, faux.FauxAssistantMessage(core.StopReasonToolUse,
			faux.FauxText(strings.Repeat("streamed text ", 200)), faux.FauxToolCall(fmt.Sprintf("c%d", i), "toolA", "{}")))
	}
	fp := faux.New(turns...)
	fp.ChunkSize = 8
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{echoTool("toolA", nil)}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Stream(context.Background(), "test non-blocking")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan core.RunResult, 1)
	go func() { res, _ := st.RunResult(); done <- res }() // never reads Events
	select {
	case res := <-done:
		if res.TurnCount != 21 {
			t.Fatalf("run ended after %d turns, want 21", res.TurnCount)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the run blocked on a consumer that never read the stream")
	}
}

// TS-11-45 (smoke, 11-PATH-2): a long run past the prune threshold sends
// old results elided and keeps them whole in the transcript.
func TestSmokePruningUnderPressure_TS11_45(t *testing.T) {
	var turns []faux.Turn
	for i := 1; i <= 4; i++ {
		turns = append(turns, heavyTurn(int64(300_000+i*20_000), faux.FauxToolCall(fmt.Sprintf("c%d", i), "toolA", `{}`)))
	}
	turns = append(turns, faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("done")))
	fp := faux.New(turns...)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{bigResult("toolA", 3000)},
		Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "start multi-turn pruning")
	if err != nil || res.StopReason != core.RunStopEndTurn {
		t.Fatalf("run = %q, %v", res.StopReason, err)
	}
	reqs := fp.Requests()
	last := reqs[len(reqs)-1].Messages
	for _, id := range []string{"c1", "c2", "c3"} {
		if tr, _ := toolResultIn(last, id); tr.Content.Text() != "[result of toolA (3000 bytes) elided; call again if needed]" {
			t.Fatalf("%s sent as %.60q, want elided", id, tr.Content.Text())
		}
	}
	if tr, _ := toolResultIn(last, "c4"); tr.Content.Text() != strings.Repeat("x", 3000) {
		t.Fatal("the latest result was pruned")
	}
	for _, m := range a.Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && strings.Contains(tr.Content.Text(), "elided") {
			t.Fatalf("the transcript holds an elided result: %s", tr.ToolUseID)
		}
	}
}

// TS-11-46 (smoke, 11-PATH-3): a truncated tool call is answered with the
// notice, the model re-issues it whole, and the run ends normally.
func TestSmokeTruncationRecovery_TS11_46(t *testing.T) {
	var args []string
	tool := core.Tool{Name: "my_tool", InputSchema: schema.Object(schema.Opt("path", schema.String())),
		Handler: func(_ context.Context, in json.RawMessage) (json.RawMessage, error) {
			args = append(args, string(in))
			return json.RawMessage(`{"ok":true}`), nil
		}}
	fp := faux.New(
		faux.FauxAssistantMessage(core.StopReasonLength, faux.FauxToolCall("c1", "my_tool", `{"path":"/et"}`)),
		faux.FauxAssistantMessage(core.StopReasonToolUse, faux.FauxToolCall("c2", "my_tool", `{"path":"/etc/hosts"}`)),
		faux.FauxAssistantMessage(core.StopReasonStop, faux.FauxText("reissued and finished")),
	)
	a, err := New(Config{Provider: fp, Model: driverModel, Tools: []core.Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "run recovery")
	if err != nil || res.StopReason != core.RunStopEndTurn || res.TurnCount != 3 {
		t.Fatalf("run = %q after %d turns, %v", res.StopReason, res.TurnCount, err)
	}
	if len(args) != 1 || args[0] != `{"path":"/etc/hosts"}` {
		t.Fatalf("handler saw %v, want only the re-issued call", args)
	}
	if tr := findToolResult(t, res.Messages, "c1"); !strings.Contains(tr.Content.Text(), "was not executed: the response hit the output token limit") {
		t.Fatalf("truncated call answered with %q", tr.Content.Text())
	}
	// The notice reached the model with the request that re-issued the call.
	if tr, ok := toolResultIn(fp.Requests()[1].Messages, "c1"); !ok || !tr.IsError {
		t.Fatal("the notice was not sent to the model")
	}
}

// sizedProvider answers with one tool call per turn for turns turns, then
// stops, and reports usage the way a real API does: the size of what it was
// sent (at the estimator's own rate), or nothing when silent.
func sizedProvider(turns int, silent bool, sent *[]core.Request) core.ProviderClient {
	var mu sync.Mutex
	return core.ClientFunc(func(_ context.Context, m *core.Model, req core.Request, _ core.ProviderStreamOptions) *core.EventStream {
		mu.Lock()
		i := len(*sent)
		*sent = append(*sent, req)
		mu.Unlock()
		msg := core.AssistantMessage{StopReason: core.StopReasonStop, Content: core.Content{core.TextBlock{Text: "done"}},
			Provider: m.Provider, API: m.API, Model: m.ID}
		if i < turns {
			msg.StopReason = core.StopReasonToolUse
			msg.Content = core.Content{faux.FauxToolCall(fmt.Sprintf("c%d", i), "toolA", `{}`)}
		}
		if !silent {
			msg.Usage = core.Usage{InputTokens: charTokens(req.Prefix) + charTokens(req.Messages)}
		}
		st := core.NewEventStream(core.StreamOptions{})
		st.End(core.StreamResult{Message: &msg})
		return st
	})
}

func elidedIn(msgs core.Messages) int {
	n := 0
	for _, m := range msgs {
		if tr, ok := m.(core.ToolResultMessage); ok && strings.Contains(tr.Content.Text(), "elided; call again") {
			n++
		}
	}
	return n
}

// Pruning stays on once the transcript is past the threshold: the usage of a
// pruned request measures the pruned size, and deciding on it would send the
// whole transcript every other turn.
func TestPruningDoesNotOscillate(t *testing.T) {
	var sent []core.Request
	a, err := New(Config{Provider: sizedProvider(6, false, &sent), Model: "claude-opus-4-5",
		Tools: []core.Tool{bigResult("toolA", 100_000)}, Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.StopReason != core.RunStopEndTurn {
		t.Fatalf("run = %q, %v", res.StopReason, err)
	}
	first := -1
	for i, req := range sent {
		n := elidedIn(req.Messages)
		if n > 0 && first < 0 {
			first = i
		}
		if first >= 0 && n == 0 {
			t.Fatalf("request %d was pruned and request %d sent the transcript whole", first, i)
		}
		if size := charTokens(req.Messages); size > 200_000 {
			t.Fatalf("request %d is ~%d tokens, past the 200k window", i, size)
		}
	}
	if first < 0 {
		t.Fatal("nothing was pruned; the test proves nothing")
	}
}

// With no usage reported, what pruning removes still counts: a transcript
// over the window that fits once pruned is sent, not refused.
func TestPruningWithoutUsageLowersTheEstimate(t *testing.T) {
	var sent []core.Request
	a, err := New(Config{Provider: sizedProvider(3, true, &sent), Model: "claude-opus-4-5",
		Tools: []core.Tool{bigResult("toolA", 300_000)}, Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.StopReason != core.RunStopEndTurn {
		t.Fatalf("run = %q, %v; a transcript that fits once pruned must be sent", res.StopReason, err)
	}
	if last := sent[len(sent)-1].Messages; elidedIn(last) != 2 {
		t.Fatalf("last request elided %d results, want 2", elidedIn(last))
	}
}

// The run slot is free by the time Run returns, so back-to-back runs never
// see ErrBusy.
func TestBackToBackRunsAreNotBusy(t *testing.T) {
	a, err := New(Config{Provider: faux.New(), Model: driverModel})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
}
