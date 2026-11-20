// Package testkit holds the Agent-free test doubles the root package's and
// the runner package's tests use, exported so a test in a package ABOVE the
// root (such as tools_test) can script a provider the same way. It imports core and schema only; a helper that
// constructed an Agent would import the root, and the root's own internal
// tests could not then import it.
package testkit

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// TestAPI is the wire API every scripted provider registers under.
const TestAPI core.API = "test-api"

// TestModel is a resolved model on TestAPI.
func TestModel() *core.Model {
	return &core.Model{ID: "test-model", ContextWindow: 100000, MaxOutputTokens: 4096}
}

// Scripted is a provider that replays a predetermined sequence of assistant
// messages, one per turn, and answers "done" once the script is exhausted.
// It is the executable double the loop is tested against; provider/faux is
// the shipped, supported form of the same idea.
type Scripted struct {
	mu    sync.Mutex
	Turns []core.AssistantMessage
	calls int
	// Seen records the message list each turn was asked to complete, so a
	// test can assert what the loop actually sent and when.
	Seen []core.Messages
	// Systems records the assembled system prompt per turn.
	Systems [][]core.ContentBlock
}

// Stream implements core.ProviderClient.
func (s *Scripted) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	st := core.NewEventStream(core.StreamOptions{})
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.Seen = append(s.Seen, req.Messages)
	s.Systems = append(s.Systems, req.System)
	var msg core.AssistantMessage
	if i < len(s.Turns) {
		msg = s.Turns[i]
	} else {
		msg = core.AssistantMessage{
			Content:    core.Content{core.TextBlock{Text: "done"}},
			StopReason: core.StopReasonStop,
		}
	}
	s.mu.Unlock()

	go func() {
		st.Push(core.MessageStartEvent{Message: msg})
		st.Push(core.MessageEndEvent{Message: msg})
		st.End(core.StreamResult{Message: &msg})
	}()
	return core.StreamChannel(st), nil
}

// SentAt returns the messages the provider was asked to complete on turn.
func (s *Scripted) SentAt(turn int) core.Messages {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn >= len(s.Seen) {
		return nil
	}
	return s.Seen[turn]
}

// TurnsRun reports how many requests the double has answered.
func (s *Scripted) TurnsRun() int { s.mu.Lock(); defer s.mu.Unlock(); return s.calls }

// ToolUse builds a tool_use block from raw argument bytes.
func ToolUse(t *testing.T, id, name, args string) core.ToolUseBlock {
	t.Helper()
	b, err := core.NewToolUse(id, name, json.RawMessage(args))
	if err != nil {
		t.Fatalf("NewToolUse: %v", err)
	}
	return b
}

// AssistantWithTools is an assistant message carrying the given blocks.
func AssistantWithTools(reason core.StopReason, blocks ...core.ContentBlock) core.AssistantMessage {
	return core.AssistantMessage{Content: core.Content(blocks), StopReason: reason}
}

// EchoTool is a tool that answers {"echoed":true} and counts its calls.
func EchoTool(name string, calls *atomic.Int32) core.Tool {
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

// Tool is a tool taking an optional string "v" that runs run.
func Tool(name string, run func(ctx context.Context, in json.RawMessage) core.ToolResult) core.Tool {
	return core.Tool{Name: name, InputSchema: schema.Object(schema.Opt("v", schema.String())), Execute: run}
}

// Returning is a tool that answers res.
func Returning(name string, res core.ToolResult) core.Tool {
	return Tool(name, func(context.Context, json.RawMessage) core.ToolResult { return res })
}

// Voter is a tool whose result votes to end the run.
func Voter(name string) core.Tool {
	return Returning(name, core.ToolResult{OK: true, Terminate: true})
}

// Wrapper is a tool that reaches children and runs body.
func Wrapper(name string, body func(ctx context.Context) core.ToolResult, children ...core.Tool) core.Tool {
	w := Tool(name, func(ctx context.Context, _ json.RawMessage) core.ToolResult { return body(ctx) })
	w.ReachableTools = children
	return w
}

// Noop is a handler that does nothing.
func Noop(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

// Named is a bare tool with a handler.
func Named(name string, reach ...core.Tool) core.Tool {
	return core.Tool{Name: name, Handler: Noop, ReachableTools: reach}
}

// Meeting measures how many handlers run at once. Each attendee waits up to
// wait for all n to be inside together, so a parallel group meets at once and
// a sequential one never does.
type Meeting struct {
	n, in, arrived, peak atomic.Int32
	wait                 time.Duration
}

// Meet is a meeting of n attendees, each waiting up to wait for the others.
func Meet(n int32, wait time.Duration) *Meeting {
	m := &Meeting{wait: wait}
	m.n.Store(n)
	return m
}

// Attend enters the meeting, waits for the others, and leaves.
func (m *Meeting) Attend() {
	cur := m.in.Add(1)
	m.arrived.Add(1)
	for p := m.peak.Load(); cur > p && !m.peak.CompareAndSwap(p, cur); p = m.peak.Load() {
	}
	testutil.Within(m.wait, func() bool { return m.arrived.Load() >= m.n.Load() })
	m.in.Add(-1)
}

// Peak is the most attendees that were inside at once.
func (m *Meeting) Peak() int32 { return m.peak.Load() }

// Attending is a tool whose handler attends m.
func Attending(name string, m *Meeting, mode core.ExecutionMode) core.Tool {
	t := Tool(name, func(_ context.Context, in json.RawMessage) core.ToolResult {
		m.Attend()
		return core.OKResult(map[string]any{"args": string(in)})
	})
	t.ExecutionMode = mode
	return t
}

// EventLog is a sink that keeps what it receives.
type EventLog struct {
	mu     sync.Mutex
	events []core.Event
}

// Add is the sink: it records e.
func (l *EventLog) Add(e core.Event) { l.mu.Lock(); l.events = append(l.events, e); l.mu.Unlock() }

// All is a copy of what the sink received, in order.
func (l *EventLog) All() []core.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]core.Event(nil), l.events...)
}

// GuardLog is a Guard that records what it sees and answers with Decide.
type GuardLog struct {
	mu   sync.Mutex
	seen []core.BeforeToolCallContext
	// Decide answers each call; nil allows it.
	Decide func(core.BeforeToolCallContext) core.BeforeToolCallDecision
}

// Guard is the core.BeforeToolCall of the log.
func (g *GuardLog) Guard(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
	g.mu.Lock()
	g.seen = append(g.seen, in)
	g.mu.Unlock()
	if g.Decide != nil {
		return g.Decide(in)
	}
	return core.BeforeToolCallDecision{}
}

// Calls is a copy of what the guard saw, in order.
func (g *GuardLog) Calls() []core.BeforeToolCallContext {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]core.BeforeToolCallContext(nil), g.seen...)
}

// Allow is a Guard that allows every call.
func Allow(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
	return core.BeforeToolCallDecision{}
}

// BlockIf is a Guard that blocks the calls cond picks.
func BlockIf(cond func(core.BeforeToolCallContext) bool, terminate bool) core.BeforeToolCall {
	return func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if cond(in) {
			return core.BeforeToolCallDecision{Block: true, Terminate: terminate, Reason: "denied"}
		}
		return core.BeforeToolCallDecision{}
	}
}

// Executions counts, per call id, the start, end and result events.
func Executions(evs []core.Event) (starts, ends, results map[string]int) {
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

// Uses is one tool_use block per name, all with the id "same" and no input:
// a group of nested calls whose ids the caller does not choose.
func Uses(names ...string) []core.ToolUseBlock {
	out := make([]core.ToolUseBlock, len(names))
	for i, n := range names {
		out[i] = core.ToolUseBlock{ID: "same", Name: n}
	}
	return out
}

// Child is a tool that answers with its own name and arguments.
func Child(name string) core.Tool {
	return Tool(name, func(_ context.Context, in json.RawMessage) core.ToolResult {
		return core.OKResult(map[string]any{"tool": name, "args": string(in)})
	})
}

// NoopAfter is an after-interceptor with no opinion.
func NoopAfter(context.Context, core.AfterToolCallContext) core.AfterToolCallDecision {
	return core.AfterToolCallDecision{}
}
