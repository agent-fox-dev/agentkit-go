package agentkit

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testkit"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// testModel is a model id the catalog does not list: an agent on it runs on
// the fallback row of the default provider.
const testModel = "test-model"

// streamFunc is a provider client written as one function.
type streamFunc func(ctx context.Context, req core.Request) *core.EventStream

func (f streamFunc) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	return core.StreamChannel(f(ctx, req)), nil
}

// answering is a provider that ends every stream with msg and err.
func answering(msg *core.AssistantMessage, err error) streamFunc {
	return func(context.Context, core.Request) *core.EventStream {
		s := core.NewEventStream(core.StreamOptions{})
		s.End(core.StreamResult{Message: msg, Err: err})
		return s
	}
}

// mustNew builds an agent on cfg, on testModel when cfg names no model.
func mustNew(t *testing.T, cfg Config) *Agent {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = testModel
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// scripted builds an agent that replays turns over ts.
func scripted(t *testing.T, ts Toolset, turns ...faux.Turn) (*Agent, *faux.Provider) {
	t.Helper()
	fp := faux.New(turns...)
	return mustNew(t, Config{Client: fp, Toolset: ts}), fp
}

func mustRunner(t *testing.T, ts Toolset) *Runner {
	t.Helper()
	r, err := NewRunner(ts)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// calls is a turn that calls the given tools.
func calls(blocks ...core.ContentBlock) faux.Turn {
	return faux.Turn{Blocks: blocks, StopReason: core.StopReasonToolUse}
}

var call = faux.FauxToolCall

func use(t *testing.T, id, name, args string) core.ToolUseBlock {
	return testkit.ToolUse(t, id, name, args)
}

// drain streams one run of a and returns its events and its result.
func drain(t *testing.T, ctx context.Context, a *Agent, prompt string) ([]core.Event, core.RunResult, error) {
	t.Helper()
	s, err := a.Stream(ctx, prompt)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var evs []core.Event
	for e := range s.Events() {
		evs = append(evs, e)
	}
	res, err := s.RunResult()
	return evs, res, err
}

// resultOf is the tool result answering id, and whether there is one.
func resultOf(msgs core.Messages, id string) (core.ToolResultMessage, bool) {
	for _, m := range msgs {
		if r, ok := m.(core.ToolResultMessage); ok && r.ToolUseID == id {
			return r, true
		}
	}
	return core.ToolResultMessage{}, false
}

// executions counts, per call id, the start, end and result events.
func executions(evs []core.Event) (starts, ends, results map[string]int) {
	starts, ends, results = map[string]int{}, map[string]int{}, map[string]int{}
	for _, e := range evs {
		switch v := e.(type) {
		case core.ToolExecutionStartEvent:
			starts[v.ToolUseID]++
		case core.ToolExecutionEndEvent:
			ends[v.ToolUseID]++
		case core.ToolResultEvent:
			results[v.Message.ToolUseID]++
		}
	}
	return
}

var echo = testkit.EchoTool

// tool is a tool taking an optional string "v" that runs run.
func tool(name string, run func(ctx context.Context, in json.RawMessage) core.ToolResult) core.Tool {
	return core.Tool{Name: name, InputSchema: schema.Object(schema.Opt("v", schema.String())), Execute: run}
}

// returning is a tool that answers res.
func returning(name string, res core.ToolResult) core.Tool {
	return tool(name, func(context.Context, json.RawMessage) core.ToolResult { return res })
}

// voter is a tool whose result votes to end the run.
func voter(name string) core.Tool {
	return returning(name, core.ToolResult{OK: true, Terminate: true})
}

// wrapper is a tool that reaches children and runs body.
func wrapper(name string, body func(ctx context.Context) core.ToolResult, children ...core.Tool) core.Tool {
	w := tool(name, func(ctx context.Context, _ json.RawMessage) core.ToolResult { return body(ctx) })
	w.ReachableTools = children
	return w
}

func noop(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

// named is a bare tool with a handler.
func named(name string, reach ...core.Tool) core.Tool {
	return core.Tool{Name: name, Handler: noop, ReachableTools: reach}
}

// meeting measures how many handlers run at once. Each attendee waits up to
// wait for all n to be inside together, so a parallel group meets at once and
// a sequential one never does.
type meeting struct {
	n, in, arrived, peak atomic.Int32
	wait                 time.Duration
}

func meet(n int32, wait time.Duration) *meeting {
	m := &meeting{wait: wait}
	m.n.Store(n)
	return m
}

func (m *meeting) attend() {
	cur := m.in.Add(1)
	m.arrived.Add(1)
	for p := m.peak.Load(); cur > p && !m.peak.CompareAndSwap(p, cur); p = m.peak.Load() {
	}
	testutil.Within(m.wait, func() bool { return m.arrived.Load() >= m.n.Load() })
	m.in.Add(-1)
}

// attending is a tool whose handler attends m.
func attending(name string, m *meeting, mode core.ExecutionMode) core.Tool {
	t := tool(name, func(_ context.Context, in json.RawMessage) core.ToolResult {
		m.attend()
		return core.OKResult(map[string]any{"args": string(in)})
	})
	t.ExecutionMode = mode
	return t
}

// eventLog is a sink that keeps what it receives.
type eventLog struct {
	mu     sync.Mutex
	events []core.Event
}

func (l *eventLog) add(e core.Event) { l.mu.Lock(); l.events = append(l.events, e); l.mu.Unlock() }

func (l *eventLog) all() []core.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]core.Event(nil), l.events...)
}

// guardLog is a Guard that records what it sees and answers with decide.
type guardLog struct {
	mu     sync.Mutex
	seen   []core.BeforeToolCallContext
	decide func(core.BeforeToolCallContext) core.BeforeToolCallDecision
}

func (g *guardLog) guard(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
	g.mu.Lock()
	g.seen = append(g.seen, in)
	g.mu.Unlock()
	if g.decide != nil {
		return g.decide(in)
	}
	return core.BeforeToolCallDecision{}
}

func (g *guardLog) calls() []core.BeforeToolCallContext {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]core.BeforeToolCallContext(nil), g.seen...)
}

func allow(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
	return core.BeforeToolCallDecision{}
}

// blockIf is a Guard that blocks the calls cond picks.
func blockIf(cond func(core.BeforeToolCallContext) bool, terminate bool) core.BeforeToolCall {
	return func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if cond(in) {
			return core.BeforeToolCallDecision{Block: true, Terminate: terminate, Reason: "denied"}
		}
		return core.BeforeToolCallDecision{}
	}
}
