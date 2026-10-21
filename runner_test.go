package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// mustRunner builds a runner or fails the test.
func mustRunner(t *testing.T, ts Toolset) *Runner {
	t.Helper()
	r, err := NewRunner(ts)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return r
}

// eventLog is a Toolset.Events sink that keeps what it receives. It is safe
// for concurrent use, as a sink must be.
type eventLog struct {
	mu     sync.Mutex
	events []core.Event
}

func (l *eventLog) add(e core.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) all() []core.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]core.Event(nil), l.events...)
}

// guardLog is a Guard that records the context of every call it authorizes,
// and decides through decide when that is set.
type guardLog struct {
	mu     sync.Mutex
	calls  []core.BeforeToolCallContext
	decide func(core.BeforeToolCallContext) core.BeforeToolCallDecision
}

func (g *guardLog) guard(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
	g.mu.Lock()
	g.calls = append(g.calls, in)
	g.mu.Unlock()
	if g.decide != nil {
		return g.decide(in)
	}
	return core.BeforeToolCallDecision{}
}

func (g *guardLog) seen() []core.BeforeToolCallContext {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]core.BeforeToolCallContext(nil), g.calls...)
}

var callIDPattern = regexp.MustCompile(`^call_[0-9a-f]{16}$`)

// TS-21-14: Call runs a batch of one under a generated call_ id, and the id is
// the same everywhere the call is seen.
func TestCallRunsABatchOfOneWithAGeneratedID_TS21_14(t *testing.T) {
	var events eventLog
	var guards guardLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", nil)}, Guard: guards.guard, Events: events.add})

	res := r.Call(context.Background(), "echo", json.RawMessage(`{"v":"x"}`))
	if !res.OK {
		t.Fatalf("Call = %+v, want OK", res)
	}
	seen := guards.seen()
	got := events.all()
	if len(seen) != 1 || len(got) != 3 {
		t.Fatalf("Guard saw %d calls and Events %d events; want 1 and 3 (start, end, result)", len(seen), len(got))
	}
	start, ok1 := got[0].(core.ToolExecutionStartEvent)
	end, ok2 := got[1].(core.ToolExecutionEndEvent)
	result, ok3 := got[2].(core.ToolResultEvent)
	if !ok1 || !ok2 || !ok3 {
		t.Fatalf("events = %T, %T, %T; want start, end, result", got[0], got[1], got[2])
	}
	id := seen[0].ToolUseID
	if !callIDPattern.MatchString(id) {
		t.Fatalf("Guard saw id %q, want call_ and 16 lowercase hex characters", id)
	}
	if start.ToolUseID != id || end.ToolUseID != id || result.Message.ToolUseID != id {
		t.Fatalf("ids differ: guard %q, start %q, end %q, result %q", id, start.ToolUseID, end.ToolUseID, result.Message.ToolUseID)
	}

	r.Call(context.Background(), "echo", nil)
	if second := guards.seen()[1].ToolUseID; second == id || !callIDPattern.MatchString(second) {
		t.Fatalf("the second call has id %q; want a different call_ id than %q", second, id)
	}
}

// TS-21-15: an empty or whitespace-only input is {}.
func TestCallTreatsEmptyInputAsAnEmptyObject_TS21_15(t *testing.T) {
	var guards guardLog
	var ran atomic.Int32
	r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("noargs", &ran)}, Guard: guards.guard})
	for _, in := range []json.RawMessage{nil, json.RawMessage(""), json.RawMessage("  \n")} {
		if res := r.Call(context.Background(), "noargs", in); !res.OK {
			t.Fatalf("Call(%q) = %+v, want OK", string(in), res)
		}
	}
	seen := guards.seen()
	if len(seen) != 3 || ran.Load() != 3 {
		t.Fatalf("Guard saw %d calls, handler ran %d times; want 3 and 3", len(seen), ran.Load())
	}
	for i, s := range seen {
		if string(s.RawInput) != "{}" {
			t.Errorf("call %d: Guard saw RawInput %q, want {}", i, s.RawInput)
		}
	}
}

// TS-21-16: an unknown tool is unknown_tool with the text a run produces, and
// neither the guard nor a handler runs.
func TestCallUnknownToolRunsNothing_TS21_16(t *testing.T) {
	var ran atomic.Int32
	var guards guardLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("alpha", &ran), echoTool("beta", &ran)}, Guard: guards.guard})

	res := r.Call(context.Background(), "missing", json.RawMessage(`{}`))
	const want = `no tool named "missing" is available in this run; available tools: alpha, beta`
	if res.OK || res.Error != "unknown_tool" || res.Detail != want {
		t.Fatalf("Call = %+v; want unknown_tool with %q", res, want)
	}
	if n := len(guards.seen()); n != 0 || ran.Load() != 0 {
		t.Fatalf("Guard saw %d calls and handlers ran %d times; want neither", n, ran.Load())
	}
}

// TS-21-17: input that is not an object, arguments that fail validation and
// arguments a guard returns that JSON cannot carry are invalid_arguments, and
// the handler never runs.
func TestCallInvalidArguments_TS21_17(t *testing.T) {
	var ran atomic.Int32
	handler := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		ran.Add(1)
		return json.RawMessage(`{}`), nil
	}
	required := core.Tool{Name: "needs_text", InputSchema: schema.Object(schema.Prop("text", schema.String())), Handler: handler}
	rewritten := core.Tool{Name: "rewritten", InputSchema: schema.Object(), Handler: handler}
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if in.ToolName == "rewritten" {
			return core.BeforeToolCallDecision{Arguments: map[string]any{"text": make(chan int)}}
		}
		return core.BeforeToolCallDecision{}
	}
	r := mustRunner(t, Toolset{Tools: []core.Tool{required, rewritten}, Guard: guard})

	for _, c := range []struct{ tool, input string }{
		{"needs_text", "not json"},
		{"needs_text", `{}`},
		{"needs_text", `["a"]`},
		{"rewritten", `{}`},
	} {
		res := r.Call(context.Background(), c.tool, json.RawMessage(c.input))
		if res.OK || res.Error != "invalid_arguments" {
			t.Errorf("Call(%s, %s) = %+v; want invalid_arguments", c.tool, c.input, res)
		}
	}
	if ran.Load() != 0 {
		t.Fatalf("the handler ran %d times for invalid arguments", ran.Load())
	}
}

// TS-21-18: a guard block is blocked_by_policy with the guard's reason, a
// default reason, and the decision's Terminate; the handler never runs.
func TestCallGuardBlock_TS21_18(t *testing.T) {
	for _, c := range []struct {
		name          string
		decision      core.BeforeToolCallDecision
		wantInDetail  string
		wantTerminate bool
	}{
		{"reason and terminate", core.BeforeToolCallDecision{Block: true, Reason: "no way", Terminate: true}, "no way", true},
		{"default reason", core.BeforeToolCallDecision{Block: true}, "blocked by policy", false},
	} {
		var ran atomic.Int32
		guards := guardLog{decide: func(core.BeforeToolCallContext) core.BeforeToolCallDecision { return c.decision }}
		r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", &ran)}, Guard: guards.guard})
		res := r.Call(context.Background(), "echo", nil)
		if res.OK || res.Error != core.BlockErrorCode || !strings.Contains(res.Detail, c.wantInDetail) {
			t.Errorf("%s: Call = %+v; want %s containing %q", c.name, res, core.BlockErrorCode, c.wantInDetail)
		}
		if res.Terminate != c.wantTerminate {
			t.Errorf("%s: Terminate = %v, want %v", c.name, res.Terminate, c.wantTerminate)
		}
		if ran.Load() != 0 {
			t.Errorf("%s: the handler ran for a blocked call", c.name)
		}
	}
}

// TS-21-19: a handler panic is panic and a Handler error is handler_error;
// Call returns normally for both.
func TestCallHandlerFailures_TS21_19(t *testing.T) {
	boom := core.Tool{Name: "boom", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("kaput")
	}}
	fail := core.Tool{Name: "fail", InputSchema: schema.Object(), Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("it broke")
	}}
	r := mustRunner(t, Toolset{Tools: []core.Tool{boom, fail}})
	if res := r.Call(context.Background(), "boom", nil); res.OK || res.Error != "panic" {
		t.Errorf("boom = %+v, want panic", res)
	}
	if res := r.Call(context.Background(), "fail", nil); res.OK || res.Error != "handler_error" || res.Detail != "it broke" {
		t.Errorf("fail = %+v, want handler_error", res)
	}
}

// TS-21-20: a context that is done before the handler would start gives
// aborted, and the handler never runs.
func TestCallDoneContextIsAborted_TS21_20(t *testing.T) {
	var ran atomic.Int32
	r := mustRunner(t, Toolset{Tools: []core.Tool{echoTool("echo", &ran)}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.Call(ctx, "echo", nil)
	if res.OK || res.Error != "aborted" || res.Detail != "Operation aborted" {
		t.Fatalf("Call = %+v; want aborted with %q", res, "Operation aborted")
	}
	if ran.Load() != 0 {
		t.Fatalf("the handler ran %d times under a done context", ran.Load())
	}
}

// TS-21-21: a Guard sees the documented context for a Call.
func TestCallGuardContext_TS21_21(t *testing.T) {
	var guards guardLog
	tool := echoTool("echo", nil)
	r := mustRunner(t, Toolset{Tools: []core.Tool{tool}, Guard: guards.guard})
	r.Call(context.Background(), "echo", json.RawMessage(`{"v":"x"}`))

	seen := guards.seen()
	if len(seen) != 1 {
		t.Fatalf("Guard saw %d calls, want 1", len(seen))
	}
	g := seen[0]
	if g.ToolName != "echo" || !callIDPattern.MatchString(g.ToolUseID) || g.Tool.Name != tool.Name ||
		g.Arguments["v"] != "x" || string(g.RawInput) != `{"v":"x"}` {
		t.Errorf("Guard context = %+v; want the echo call with its id, tool, arguments and raw input", g)
	}
	if len(g.Batch) != 1 || g.Batch[0].ID != g.ToolUseID || g.Index != 0 {
		t.Errorf("Batch = %v, Index = %d; want the one call at 0", g.Batch, g.Index)
	}
	if g.Assistant != nil || g.TurnCount != 0 || g.ParentToolUseID != "" || g.ParentToolName != "" {
		t.Errorf("Assistant %v, TurnCount %d, parent %q/%q; want nil, 0 and no parent", g.Assistant, g.TurnCount, g.ParentToolUseID, g.ParentToolName)
	}
}

// TS-21-48: outside a run the runner attaches no usage reporter of its own and
// passes through the caller's.
func TestRunnerOutsideARunPassesTheCallersUsageReporter_TS21_48(t *testing.T) {
	spend := core.Tool{Name: "spend", InputSchema: schema.Object(), Handler: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		core.ReportUsage(ctx, core.Usage{InputTokens: 7})
		return json.RawMessage(`{}`), nil
	}}
	a, err := New(Config{Client: faux.New(), Model: testModelID, Toolset: Toolset{Tools: []core.Tool{spend}}})
	if err != nil {
		t.Fatal(err)
	}
	before := a.Usage()

	var reported []core.Usage
	ctx := core.WithUsageReporter(context.Background(), func(u core.Usage) { reported = append(reported, u) })
	if res := a.Runner().Call(ctx, "spend", nil); !res.OK {
		t.Fatalf("Call = %+v", res)
	}
	if len(reported) != 1 || reported[0].InputTokens != 7 {
		t.Fatalf("the caller's reporter received %+v, want one report of 7 input tokens", reported)
	}
	if res := a.Runner().Call(context.Background(), "spend", nil); !res.OK {
		t.Fatalf("Call under a plain context = %+v", res)
	}
	if after := a.Usage(); !reflect.DeepEqual(after, before) {
		t.Fatalf("Agent.Usage changed from %+v to %+v by calls made outside a run", before, after)
	}
}
