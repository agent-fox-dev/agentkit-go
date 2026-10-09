package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// blocking is a provider that honours ctx: it holds the stream open until the
// context is cancelled and then resolves it to an ABORTED message, the way a
// real adapter does when the caller pulls the plug mid-body. The scripted
// double ignores ctx, which is why the original abort test could never
// observe an abort and always skipped.
type blocking struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
}

func (b *blocking) agent(t *testing.T) *Agent {
	t.Helper()
	a, err := New(Config{Provider: core.ClientFunc(b.stream), Model: testModelID})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (b *blocking) stream(ctx context.Context, m *core.Model, _ core.Request, _ core.ProviderStreamOptions) *core.EventStream {
	st := core.NewEventStream(core.StreamOptions{})
	b.mu.Lock()
	b.calls++
	first := b.calls == 1
	b.mu.Unlock()
	go func() {
		if first && b.started != nil {
			close(b.started)
		}
		<-ctx.Done()
		msg := core.AssistantMessage{
			Content:      core.Content{core.TextBlock{Text: "partial"}},
			StopReason:   core.StopReasonAborted,
			ErrorMessage: ctx.Err().Error(),
			Model:        m.ID,
		}
		st.Push(core.MessageEndEvent{Message: msg})
		st.End(core.StreamResult{Message: &msg})
	}()
	return st
}

// ------------------------------------------------------------------ REQ-GO-09

// TestCallerCancellationAbortsTheRun: a cancelled caller ctx ends the run as
// aborted, and the error says it was the caller's context.
func TestCallerCancellationAbortsTheRun(t *testing.T) {
	b := &blocking{started: make(chan struct{})}
	a := b.agent(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-b.started; cancel() }()
	res, err := a.Run(ctx, "go")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res.StopReason != core.RunStopAborted {
		t.Fatalf("StopReason = %q, want aborted", res.StopReason)
	}
}

// ------------------------------------------------------------------ REQ-LOOP-09

// TestAnAbortedMessageKeepsItsErrorMessage: REQ-LOOP-09 requires
// error_message SET on the aborted turn and forbids rewriting it at abort
// time.
func TestAnAbortedMessageKeepsItsErrorMessage(t *testing.T) {
	b := &blocking{started: make(chan struct{})}
	a := b.agent(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-b.started; cancel() }()
	res, _ := a.Run(ctx, "go")
	am := lastAssistant(res.Messages)
	if am == nil || am.StopReason != core.StopReasonAborted {
		t.Fatalf("no aborted assistant message in %v", res.Messages)
	}
	if am.ErrorMessage == "" {
		t.Fatal("the aborted message's error_message was cleared; REQ-LOOP-09 says it is set " +
			"and the message is appended verbatim")
	}
	if am.Content.Text() != "partial" {
		t.Fatalf("partial content %q was not kept", am.Content.Text())
	}
}

// ------------------------------------------------------------------ REQ-LOOP-16

// TestACancelledBatchEndsTheRunAtTheTurnBoundary: a cancellation that lands
// during a tool batch must not be followed by a provider call on a dead
// context. The transcript then ends in a tool_result — REQ-LOOP-16's
// "normal outcome of REQ-LOOP-09 cancellation".
func TestACancelledBatchEndsTheRunAtTheTurnBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "cancels", `{}`)),
	}}
	a := newTestAgent(t, s, nil, core.Tool{
		Name: "cancels", Description: "cancels the run", InputSchema: schema.Object(),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			cancel()
			return json.RawMessage(`{}`), nil
		},
	})
	res, err := a.Run(ctx, "go")
	if s.turnsRun() != 1 {
		t.Fatalf("provider was called %d times; the loop issued a request on a cancelled context", s.turnsRun())
	}
	if !errors.Is(err, context.Canceled) || res.StopReason != core.RunStopAborted {
		t.Fatalf("err=%v stop=%q; want context.Canceled / aborted", err, res.StopReason)
	}
	msgs := a.Messages()
	if _, ok := msgs[len(msgs)-1].(core.ToolResultMessage); !ok {
		t.Fatalf("transcript ends in %T, want a tool result", msgs[len(msgs)-1])
	}
}

// ------------------------------------------------------------------ NFR-REL-02

// TestPanicsInThirdPartyCodeDoNotCrashTheProcess covers the call sites
// outside a tool handler: the provider and a tool's argument shim.
func TestPanicsInThirdPartyCodeDoNotCrashTheProcess(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		a, err := New(Config{Model: testModelID, Provider: core.ClientFunc(
			func(context.Context, *core.Model, core.Request, core.ProviderStreamOptions) *core.EventStream {
				panic("provider exploded")
			})})
		if err != nil {
			t.Fatal(err)
		}
		st, err := a.Stream(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		var errs []string
		for e := range st.Events() {
			if ev, ok := e.(core.ErrorEvent); ok {
				errs = append(errs, ev.Message)
			}
		}
		res, err := st.RunResult()
		if err == nil || res.StopReason != core.RunStopError {
			t.Fatalf("err=%v stop=%q; a provider panic must end the run as an error", err, res.StopReason)
		}
		am := lastAssistant(res.Messages)
		if am == nil || am.StopReason != core.StopReasonError {
			t.Fatal("the terminal marker of REQ-LOOP-09 is missing after a provider panic")
		}
		if len(errs) == 0 || !strings.Contains(errs[0], "provider exploded") {
			t.Fatalf("no error event carried the panic: %v", errs)
		}
	})
	t.Run("prepare_arguments", func(t *testing.T) {
		s := oneToolTurn(t)
		a := newTestAgent(t, s, nil, core.Tool{
			Name: "echo", Description: "echo", InputSchema: schema.Object(schema.Opt("v", schema.String())),
			PrepareArguments: func(map[string]any) map[string]any { panic("shim exploded") },
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return json.RawMessage(`{}`), nil
			},
		})
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		tr := findToolResult(t, res.Messages, "c1")
		if !tr.IsError || !strings.Contains(tr.Content.Text(), "panicked") {
			t.Fatalf("a panicking PrepareArguments must become an error tool result (REQ-TOOL-11): %v", tr.Content.Text())
		}
	})
}

// ------------------------------------------------------------------ REQ-LOOP-11.3

// TestEveryCallOpensAndClosesExactlyOnce pins the execution event pairing
// for calls that never reach a handler: blocked, invalid, unknown, and
// aborted. The original test asserted results only, which is why an End
// without a Start went unnoticed.
func TestEveryCallOpensAndClosesExactlyOnce(t *testing.T) {
	count := func(evs []core.Event) (starts, ends map[string]int) {
		starts, ends = map[string]int{}, map[string]int{}
		for _, e := range evs {
			switch v := e.(type) {
			case core.ToolExecutionStartEvent:
				starts[v.ToolUseID]++
			case core.ToolExecutionEndEvent:
				ends[v.ToolUseID]++
			}
		}
		return
	}
	check := func(t *testing.T, evs []core.Event, ids ...string) {
		t.Helper()
		starts, ends := count(evs)
		for _, id := range ids {
			if starts[id] != 1 || ends[id] != 1 {
				t.Fatalf("call %s: %d start / %d end events, want 1/1 (REQ-LOOP-11.3)", id, starts[id], ends[id])
			}
		}
	}
	drain := func(st *core.EventStream) []core.Event {
		var evs []core.Event
		for e := range st.Events() {
			evs = append(evs, e)
		}
		return evs
	}

	t.Run("aborted", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "c1", "echo", `{}`), toolUse(t, "c2", "echo", `{}`)),
		}}
		ctx, cancel := context.WithCancel(context.Background())
		a := newTestAgent(t, s, nil, echoTool("echo", nil))
		cancel()
		st, err := a.Stream(ctx, "go")
		if err != nil {
			t.Fatal(err)
		}
		check(t, drain(st), "c1", "c2")
	})
	t.Run("blocked, invalid, unknown", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "b1", "echo", `{}`),
				toolUse(t, "b2", "echo", `{"v":{"not":"a string"}}`),
				toolUse(t, "b3", "nope", `{}`)),
		}}
		a := newTestAgent(t, s, func(c *Config) {
			c.Guard = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
				return core.BeforeToolCallDecision{Block: in.ToolUseID == "b1", Reason: "no"}
			}
		}, echoTool("echo", nil))
		st, err := a.Stream(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		check(t, drain(st), "b1", "b2", "b3")
	})
}

// ------------------------------------------------------------------ REQ-OBS-06

// TestTurnEndToolResultsAreNonNil: "always non-nil; [] for a no-tool turn".
func TestTurnEndToolResultsAreNonNil(t *testing.T) {
	s := &scripted{}
	a := newTestAgent(t, s, nil)
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	ends := 0
	for e := range st.Events() {
		if te, ok := e.(core.TurnEndEvent); ok {
			ends++
			if te.ToolResults == nil {
				t.Fatal("TurnEndEvent.ToolResults is nil (REQ-OBS-06)")
			}
		}
	}
	if ends != 1 {
		t.Fatalf("%d turn end events, want 1", ends)
	}
}

// ------------------------------------------------------------------ OQ-8

// TestAShellToolWithNoInterceptorIsRefused: construction fails loudly when
// a shell tool is configured and nothing guards it.
func TestAShellToolWithNoInterceptorIsRefused(t *testing.T) {
	shell := func(name string) core.Tool {
		return core.Tool{Name: name, Description: "shell", InputSchema: schema.Object(schema.Prop("command", schema.String())),
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}
	}
	for _, name := range guard.ShellToolNames {
		if _, err := New(agentCfg(shell(name))); !errors.Is(err, core.ErrUnguardedExecute) {
			t.Fatalf("%s: err = %v, want ErrUnguardedExecute", name, err)
		}
	}
	// The explicit opt-out is an interceptor, so passing it is an act.
	cfg := agentCfg(shell("execute"))
	cfg.Guard = guard.AllowAll
	if _, err := New(cfg); err != nil {
		t.Fatalf("guard.AllowAll: %v", err)
	}
	// And a policy that excludes the shell tool from the run needs no guard.
	cfg = agentCfg(shell("execute"))
	cfg.Policy.ExcludeTools = []string{"execute"}
	if _, err := New(cfg); err != nil {
		t.Fatalf("excluded shell tool: %v", err)
	}
}
