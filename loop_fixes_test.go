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

func (b *blocking) provider() core.APIProvider {
	return core.APIProvider{API: testAPI, Stream: b.stream}
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
			Provider:     m.Provider, API: m.API, Model: m.ID,
		}
		st.Push(core.MessageEndEvent{Message: msg})
		st.End(core.StreamResult{Message: &msg})
	}()
	return st
}

// ------------------------------------------------------------------ REQ-GO-09

// TestAbortAndCallerCancellationAreDistinguishable: ErrAborted means
// Agent.Abort(); a cancelled caller ctx reports context.Canceled. Collapsing
// them makes a server timeout read as a user action.
func TestAbortAndCallerCancellationAreDistinguishable(t *testing.T) {
	t.Run("Agent.Abort", func(t *testing.T) {
		b := &blocking{started: make(chan struct{})}
		a := newTestAgent(t, nil, nil)
		a.cfg.Providers = core.ProviderRegistry{testAPI: b.provider()}
		go func() { <-b.started; a.Abort() }()
		res, err := a.Run(context.Background(), "go")
		if !errors.Is(err, core.ErrAborted) {
			t.Fatalf("err = %v, want ErrAborted", err)
		}
		if errors.Is(err, context.Canceled) {
			t.Fatal("an Agent.Abort must not read as the caller's own cancellation")
		}
		if res.StopReason != core.RunStopAborted {
			t.Fatalf("StopReason = %q, want aborted", res.StopReason)
		}
		if !a.Idle() {
			t.Fatal("agent must be Idle after an aborted run")
		}
	})
	t.Run("caller ctx", func(t *testing.T) {
		b := &blocking{started: make(chan struct{})}
		a := newTestAgent(t, nil, nil)
		a.cfg.Providers = core.ProviderRegistry{testAPI: b.provider()}
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-b.started; cancel() }()
		_, err := a.Run(ctx, "go")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if errors.Is(err, core.ErrAborted) {
			t.Fatal("a caller cancellation must not read as Agent.Abort (REQ-GO-09)")
		}
	})
}

// ------------------------------------------------------------------ REQ-LOOP-09

// TestAnAbortedMessageKeepsItsErrorMessage: REQ-LOOP-09 requires
// error_message SET on the aborted turn and forbids rewriting it at abort
// time. The loop used to clear it for every Agent.Abort.
func TestAnAbortedMessageKeepsItsErrorMessage(t *testing.T) {
	b := &blocking{started: make(chan struct{})}
	a := newTestAgent(t, nil, nil)
	a.cfg.Providers = core.ProviderRegistry{testAPI: b.provider()}
	go func() { <-b.started; a.Abort() }()
	res, _ := a.Run(context.Background(), "go")
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
// "normal outcome of REQ-LOOP-09 cancellation" — and Continue accepts it.
func TestACancelledBatchEndsTheRunAtTheTurnBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "cancels", `{}`)),
	}}
	a := newTestAgent(t, s, nil)
	_ = a.RegisterTool(core.Tool{
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
	if role, _ := a.History().LastRole(); role != core.RoleToolResult {
		t.Fatalf("transcript ends in %q, want tool_result", role)
	}
}

// ------------------------------------------------------------------ NFR-REL-02

// TestPanicsInThirdPartyCodeDoNotCrashTheProcess covers the call sites that
// were bare: middleware, the context transform, the stop policy and a tool's
// argument shim. Each was confirmed to crash the test binary
// before the wrappers landed.
func TestPanicsInThirdPartyCodeDoNotCrashTheProcess(t *testing.T) {
	explode := func(what string) func(*core.AgentConfig) {
		return func(c *core.AgentConfig) {
			switch what {
			case "middleware":
				c.Middleware = []core.Middleware{func(core.Handler) core.Handler {
					return func(context.Context, core.Request) *core.EventStream { panic("mw exploded") }
				}}
			case "transform":
				c.TransformContext = func(context.Context, core.Messages) core.Messages { panic("tf exploded") }
			case "stoppolicy":
				c.StopPolicy = func(core.StopContext) bool { panic("policy exploded") }
			}
		}
	}

	t.Run("middleware", func(t *testing.T) {
		var errs []string
		s := &scripted{}
		a := newTestAgent(t, s, func(c *core.AgentConfig) {
			explode("middleware")(c)
			c.Hooks.OnError = func(err error) { errs = append(errs, err.Error()) }
		})
		res, err := a.Run(context.Background(), "go")
		if err == nil || res.StopReason != core.RunStopError {
			t.Fatalf("err=%v stop=%q; a middleware panic must end the run as an error", err, res.StopReason)
		}
		am := lastAssistant(res.Messages)
		if am == nil || am.StopReason != core.StopReasonError {
			t.Fatal("the terminal marker of REQ-LOOP-09 is missing after a middleware panic")
		}
		if len(errs) == 0 || !strings.Contains(errs[0], "mw exploded") {
			t.Fatalf("OnError did not see the panic: %v", errs)
		}
	})
	t.Run("transform", func(t *testing.T) {
		s := &scripted{}
		a := newTestAgent(t, s, explode("transform"))
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatalf("a panicking transform must be contained and the run continue on the raw view: %v", err)
		}
		if s.turnsRun() != 1 {
			t.Fatal("the request was not issued")
		}
	})
	t.Run("stoppolicy", func(t *testing.T) {
		s := &scripted{}
		a := newTestAgent(t, s, explode("stoppolicy"))
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatalf("a panicking stop policy stops the run cleanly, it does not error it: %v", err)
		}
		if res.StopReason != core.RunStopPolicy {
			t.Fatalf("StopReason = %q; a broken limit must fail CLOSED (ruling in consultStopPolicy)", res.StopReason)
		}
	})
	t.Run("prepare_arguments", func(t *testing.T) {
		s := oneToolTurn(t)
		a := newTestAgent(t, s, nil)
		_ = a.RegisterTool(core.Tool{
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
		a := newTestAgent(t, s, nil)
		_ = a.RegisterTool(echoTool("echo", nil))
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
		a := newTestAgent(t, s, func(c *core.AgentConfig) {
			c.BeforeToolCall = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
				return core.BeforeToolCallDecision{Block: in.ToolUseID == "b1", Reason: "no"}
			}
		})
		_ = a.RegisterTool(echoTool("echo", nil))
		st, err := a.Stream(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		check(t, drain(st), "b1", "b2", "b3")
	})
}

// ------------------------------------------------------------------ REQ-OBS-06

// TestTurnEndToolResultsAreNonNilForHooksToo: "always non-nil; [] for a
// no-tool turn" must hold for the OnTurnEnd hook and StopContext, not only
// for the stream copy that clone() rebuilds.
func TestTurnEndToolResultsAreNonNilForHooksToo(t *testing.T) {
	var hookNil, policyNil bool
	s := &scripted{}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.Hooks.OnTurnEnd = func(e core.TurnEndEvent) { hookNil = e.ToolResults == nil }
		c.StopPolicy = func(sc core.StopContext) bool { policyNil = sc.ToolResults == nil; return false }
	})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if hookNil || policyNil {
		t.Fatalf("ToolResults nil: hook=%v policy=%v (REQ-OBS-06)", hookNil, policyNil)
	}
}

// TestDeferredResponseStillEmitsTurnEnd: a turn that started owes its
// TurnEndEvent whichever way it ended.
func TestDeferredResponseStillEmitsTurnEnd(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{{StopReason: core.StopReasonDeferred}}}
	a := newTestAgent(t, s, nil)
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	starts, ends := 0, 0
	for e := range st.Events() {
		switch e.(type) {
		case core.TurnStartEvent:
			starts++
		case core.TurnEndEvent:
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("turn events: %d start / %d end; a deferred turn must close", starts, ends)
	}
	if _, err := st.RunResult(); !errors.Is(err, core.ErrDeferredUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

// ------------------------------------------------------------------ REQ-LIFE-02

// TestSnapshotRevisionMatchesItsMessages: Messages and Revision are read
// under ONE lock, so a concurrent append cannot land between them.
func TestSnapshotRevisionMatchesItsMessages(t *testing.T) {
	h := core.NewConversationHistory()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			h.Record(core.NullLeaf, user("m"))
		}
	}()
	for i := 0; i < 2000; i++ {
		msgs, rev := h.SnapshotBranch()
		if uint64(len(msgs)) != rev {
			// Every Record appends exactly one message and bumps the revision
			// by one, so a consistent read has them equal.
			close(stop)
			wg.Wait()
			t.Fatalf("snapshot has %d messages at revision %d", len(msgs), rev)
		}
	}
	close(stop)
	wg.Wait()
}

// ------------------------------------------------------------------ REQ-LIFE-03

// TestSetModelDuringARunDoesNotRace is a race-detector test: SetModel writes
// cfg.Model under the lock while the run reads it for its start event.
func TestSetModelDuringARunDoesNotRace(t *testing.T) {
	for i := 0; i < 50; i++ {
		s := &scripted{}
		a := newTestAgent(t, s, nil)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = a.SetModel(&core.Model{ID: "other", API: testAPI, Provider: "test", ContextWindow: 1000, MaxTokens: 10})
		}()
		_, _ = a.Run(context.Background(), "go")
		<-done
	}
}

// ------------------------------------------------------------------ OQ-8

// TestAShellToolWithNoInterceptorFailsTheRun: construction of a run fails
// loudly when execute is registered and nothing guards it.
func TestAShellToolWithNoInterceptorFailsTheRun(t *testing.T) {
	shell := func(name string) core.Tool {
		return core.Tool{Name: name, Description: "shell", InputSchema: schema.Object(schema.Prop("command", schema.String())),
			Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}
	}
	for _, name := range guard.ShellToolNames {
		s := &scripted{}
		a := newTestAgent(t, s, nil)
		_ = a.RegisterTool(shell(name))
		if _, err := a.Run(context.Background(), "go"); !errors.Is(err, core.ErrUnguardedExecute) {
			t.Fatalf("%s: err = %v, want ErrUnguardedExecute", name, err)
		}
		if s.turnsRun() != 0 {
			t.Fatal("a request was issued before the guard fired")
		}
	}
	// The explicit opt-out is an interceptor, so passing it is an act.
	s := &scripted{}
	a := newTestAgent(t, s, func(c *core.AgentConfig) { c.BeforeToolCall = guard.AllowAll })
	_ = a.RegisterTool(shell("execute"))
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("guard.AllowAll: %v", err)
	}
	// And a policy that excludes the shell tool from the run needs no guard.
	s = &scripted{}
	a = newTestAgent(t, s, func(c *core.AgentConfig) { c.ToolPolicy.ExcludeTools = []string{"execute"} })
	_ = a.RegisterTool(shell("execute"))
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("excluded shell tool: %v", err)
	}
}
