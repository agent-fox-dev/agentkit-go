package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// toolNames is the names of tools, in order.
func toolNames(tools []core.Tool) []string {
	out := make([]string, len(tools))
	for i, t := range tools {
		out[i] = t.Name
	}
	return out
}

// wireNames is the names of a request's tool definitions, in order.
func wireNames(wires []core.ToolWire) []string {
	out := make([]string, len(wires))
	for i, w := range wires {
		out[i] = w.Name
	}
	return out
}

// finalTurn is a scripted assistant message that ends the run.
func finalTurn(text string) faux.Turn {
	return faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(text)}, StopReason: core.StopReasonStop}
}

// callTurn is a scripted assistant message with tool calls, and the usage a
// real response would report.
func callTurn(calls ...core.ContentBlock) faux.Turn {
	return faux.Turn{Blocks: calls, StopReason: core.StopReasonToolUse, Usage: core.Usage{InputTokens: 10, OutputTokens: 5}}
}

// TS-21-3: a zero Toolset is an agent with no tools, and the first request
// carries no tool definitions.
func TestZeroToolsetIsAnAgentWithNoTools_TS21_3(t *testing.T) {
	fp := faux.New(finalTurn("hello"))
	a, err := New(Config{Client: fp, Model: testModelID})
	if err != nil {
		t.Fatalf("New with a zero Toolset: %v", err)
	}
	if n := len(a.Runner().Tools()); n != 0 {
		t.Fatalf("Runner().Tools() has %d tools, want none", n)
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if reqs := fp.Requests(); len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Fatalf("requests = %d, first request tools = %v; want one request with no tool definitions", len(reqs), reqs[0].Tools)
	}
}

// TS-21-11: Tools returns the policy-resolved set in policy order, as a fresh
// slice every time.
func TestRunnerToolsIsTheResolvedSetAndFresh_TS21_11(t *testing.T) {
	alpha := core.Tool{Name: "alpha", Handler: noopHandler}
	beta := core.Tool{Name: "beta", Handler: noopHandler}
	gamma := core.Tool{Name: "gamma", Handler: noopHandler}
	ts := Toolset{
		Tools:  []core.Tool{alpha, beta, gamma},
		Policy: core.ToolPolicy{Tools: []core.Tool{gamma, alpha}}, // a subset, in a different order
	}
	r := mustRunner(t, ts)

	want := toolNames(ts.Policy.Resolve(ts.Tools))
	if got := toolNames(r.Tools()); !slices.Equal(got, want) || !slices.Equal(got, []string{"gamma", "alpha"}) {
		t.Fatalf("Tools() = %v, want the policy's %v", got, want)
	}
	first := r.Tools()
	first[0] = core.Tool{Name: "overwritten"}
	if got := toolNames(r.Tools()); !slices.Equal(got, want) {
		t.Fatalf("Tools() = %v after a caller overwrote a returned slice, want %v", got, want)
	}
}

// TS-21-12: Reachable is core.ReachableTools over the resolved set, and the
// agent's ReachableTools is the same thing.
func TestReachableAgreesAcrossRunnerAndAgent_TS21_12(t *testing.T) {
	wrapper := wrapperTool("wrapper", func(context.Context) core.ToolResult { return core.OKResult(nil) },
		childTool("child_a"), childTool("child_b"))
	ts := Toolset{Tools: []core.Tool{wrapper, childTool("solo")}}
	r := mustRunner(t, ts)
	a, err := New(Config{Client: faux.New(), Model: testModelID, Toolset: ts})
	if err != nil {
		t.Fatal(err)
	}

	want := toolNames(core.ReachableTools(r.Tools()))
	if !slices.Equal(want, []string{"wrapper", "child_a", "child_b", "solo"}) {
		t.Fatalf("core.ReachableTools = %v; the fixture is not what the test assumes", want)
	}
	if got := toolNames(r.Reachable()); !slices.Equal(got, want) {
		t.Errorf("Runner.Reachable = %v, want %v", got, want)
	}
	if got := toolNames(a.ReachableTools()); !slices.Equal(got, want) {
		t.Errorf("Agent.ReachableTools = %v, want %v", got, want)
	}
	if got, want := toolNames(a.ReachableTools()), toolNames(a.Runner().Reachable()); !slices.Equal(got, want) {
		t.Errorf("Agent.ReachableTools = %v, Runner().Reachable() = %v; want the same", got, want)
	}
}

// TS-21-13: *Agent exports exactly Run, Stream, Messages, Usage,
// ReachableTools and Runner, and (since 29-REQ-6.4) Resolution.
func TestAgentExportsSevenMethods_TS21_13(t *testing.T) {
	typ := reflect.TypeOf(&Agent{})
	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	sort.Strings(got)
	want := []string{"Messages", "ReachableTools", "Resolution", "Run", "Runner", "Stream", "Usage"}
	if !slices.Equal(got, want) {
		t.Fatalf("*Agent exports %v, want %v", got, want)
	}
}

// TS-21-41: during a run the run's stream receives the events of its calls,
// and Toolset.Events is not called for them.
func TestRunEventsGoToTheRunsStreamNotToolsetEvents_TS21_41(t *testing.T) {
	var sinkCalls atomic.Int32
	fp := faux.New(callTurn(faux.FauxToolCall("c1", "echo", "{}")), finalTurn("done"))
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{
		Tools:  []core.Tool{echoTool("echo", nil)},
		Events: func(core.Event) { sinkCalls.Add(1) },
	}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var start, end, result int
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.ToolUseID == "c1" {
				start++
			}
		case core.ToolExecutionEndEvent:
			if ev.ToolUseID == "c1" {
				end++
			}
		case core.ToolResultEvent:
			if ev.Message.ToolUseID == "c1" {
				result++
			}
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatal(err)
	}
	if start != 1 || end != 1 || result != 1 {
		t.Errorf("the run's stream carried %d start, %d end and %d result events for c1, want one of each", start, end, result)
	}
	if n := sinkCalls.Load(); n != 0 {
		t.Errorf("Toolset.Events was called %d times for a run's calls, want 0", n)
	}
}

// TS-21-42: New checks the client and model first, then returns NewRunner's
// error unchanged, and sends no request.
func TestNewReturnsNewRunnersErrorAndSendsNothing_TS21_42(t *testing.T) {
	unguarded := Toolset{Tools: []core.Tool{{Name: "run_command", Handler: noopHandler}}}
	_, want := NewRunner(unguarded)
	if want == nil {
		t.Fatal("NewRunner accepted an unguarded shell tool")
	}
	fp := faux.New()
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: unguarded})
	if a != nil || err == nil || err.Error() != want.Error() || !errors.Is(err, core.ErrUnguardedExecute) {
		t.Fatalf("New = %v, %v; want a nil agent and NewRunner's error %q", a, err, want)
	}
	if n := fp.Calls(); n != 0 {
		t.Fatalf("the provider received %d requests, want none", n)
	}

	// With no model and a bad toolset, the model is what New complains about.
	if _, err := New(Config{Client: fp, Toolset: unguarded}); err == nil || errors.Is(err, core.ErrUnguardedExecute) {
		t.Fatalf("New without a model = %v; want the model error, not the toolset's", err)
	}
}

// TS-21-43: requests, the system prompt and the fixed-token estimate are built
// from Runner.Tools().
func TestRequestsPromptAndEstimateUseRunnerTools_TS21_43(t *testing.T) {
	alpha, beta, gamma := echoTool("alpha", nil), echoTool("beta", nil), echoTool("gamma", nil)
	alpha.PromptGuidelines, gamma.PromptGuidelines = []string{"Use alpha first."}, []string{"Use gamma last."}
	fp := faux.New(finalTurn("done"))
	a, err := New(Config{Client: fp, Model: testModelID, System: "You are a test.", Toolset: Toolset{
		Tools:  []core.Tool{alpha, beta, gamma},
		Policy: core.ToolPolicy{Tools: []core.Tool{gamma, alpha}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	tools := a.Runner().Tools()
	req := fp.Requests()[0]
	if got, want := wireNames(req.Tools), toolNames(tools); !slices.Equal(got, want) || !slices.Equal(got, []string{"gamma", "alpha"}) {
		t.Errorf("request tools = %v, want Runner().Tools() = %v", got, want)
	}
	wantSystem := prompt.Build("You are a test.", tools)
	if len(req.System) != 1 || req.System[0].(core.TextBlock).Text != wantSystem {
		t.Errorf("the system prompt is not prompt.Build over Runner().Tools()")
	}
	wires, _ := json.Marshal(core.ToolWires(tools))
	if want := int64(len(wantSystem)+len(wires)) / charsPerToken; a.fixedTokens != want {
		t.Errorf("fixedTokens = %d, want %d: the system prompt and tool definitions of Runner().Tools()", a.fixedTokens, want)
	}
}

// holdTool is a tool whose handler reports that it started and then waits to
// be released, so a test can act while a call is in flight.
func holdTool(name string, started chan<- struct{}, release <-chan struct{}) core.Tool {
	return core.Tool{Name: name, InputSchema: schema.Object(),
		Handler: func(context.Context, json.RawMessage) (json.RawMessage, error) {
			started <- struct{}{}
			<-release
			return json.RawMessage(`{}`), nil
		}}
}

// TS-21-44: Agent.Runner is the same pointer every time, and calls made
// through it stay outside every run, during one or not.
func TestAgentRunnerCallsStayOutsideTheRun_TS21_44(t *testing.T) {
	ctx := context.Background()

	t.Run("a runner call in flight does not claim the run slot", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		fp := faux.New(finalTurn("hello"))
		a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{Tools: []core.Tool{holdTool("hold", started, release)}}})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan core.ToolResult, 1)
		go func() { done <- a.Runner().Call(ctx, "hold", nil) }()
		<-started
		_, err = a.Run(ctx, "hello")
		close(release)
		if errors.Is(err, core.ErrBusy) || err != nil {
			t.Fatalf("Run while a runner call is in flight = %v, want it to run", err)
		}
		if res := <-done; !res.OK {
			t.Fatalf("the runner call = %+v", res)
		}
		if n := len(a.Messages()); n != 2 {
			t.Fatalf("the transcript holds %d messages, want the prompt and the reply: the runner call added nothing", n)
		}
	})

	t.Run("runner calls during a run add nothing to it", func(t *testing.T) {
		started, release := make(chan struct{}, 1), make(chan struct{})
		var events eventLog
		fp := faux.New(callTurn(faux.FauxToolCall("c1", "hold", "{}")), finalTurn("done"), finalTurn("again"))
		a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{
			Tools:  []core.Tool{holdTool("hold", started, release), echoTool("echo", nil)},
			Events: events.add,
		}})
		if err != nil {
			t.Fatal(err)
		}
		r := a.Runner()
		st, err := a.Stream(ctx, "go")
		if err != nil {
			t.Fatal(err)
		}
		<-started // the run is inside its tool call

		before, usage := len(a.Messages()), a.Usage()
		if a.Runner() != r {
			t.Error("Agent.Runner returned a different pointer during a run")
		}
		if res := r.Call(ctx, "echo", nil); !res.OK {
			t.Errorf("Call during a run = %+v", res)
		}
		if res := r.Batch(ctx, toolUse(t, "b1", "echo", `{}`), toolUse(t, "b2", "echo", `{}`)); len(res) != 2 || !res[0].OK || !res[1].OK {
			t.Errorf("Batch during a run = %+v", res)
		}
		if n := len(a.Messages()); n != before {
			t.Errorf("the transcript grew from %d to %d messages", before, n)
		}
		if after := a.Usage(); !reflect.DeepEqual(after, usage) {
			t.Errorf("Usage changed from %+v to %+v", usage, after)
		}
		if _, err := a.Stream(ctx, "second"); !errors.Is(err, core.ErrBusy) {
			t.Errorf("a second Stream during the run = %v, want ErrBusy: the run still holds the slot", err)
		}

		close(release)
		if _, err := st.RunResult(); err != nil {
			t.Fatal(err)
		}

		// And again once the run has finished: the same runner, still no
		// effect on the transcript or Usage, and no run slot held.
		after, usageAfter := len(a.Messages()), a.Usage()
		if a.Runner() != r {
			t.Error("Agent.Runner returned a different pointer after the run")
		}
		if res := r.Call(ctx, "echo", nil); !res.OK {
			t.Errorf("Call after the run = %+v", res)
		}
		if res := r.Batch(ctx, toolUse(t, "b3", "echo", `{}`)); len(res) != 1 || !res[0].OK {
			t.Errorf("Batch after the run = %+v", res)
		}
		if n := len(a.Messages()); n != after {
			t.Errorf("the transcript grew from %d to %d messages after the run", after, n)
		}
		if u := a.Usage(); !reflect.DeepEqual(u, usageAfter) {
			t.Errorf("Usage changed from %+v to %+v by calls made after the run", usageAfter, u)
		}
		if _, err := a.Run(ctx, "once more"); err != nil {
			t.Errorf("a Run after runner calls = %v, want it to run", err)
		}
		var names []string
		for _, e := range events.all() {
			if s, ok := e.(core.ToolExecutionStartEvent); ok {
				names = append(names, s.Name)
			}
		}
		if want := []string{"echo", "echo", "echo", "echo", "echo"}; !slices.Equal(names, want) {
			t.Errorf("Toolset.Events received start events for %v, want only the runner calls %v (the run's own hold call goes to the run's stream)", names, want)
		}
	})
}

// TS-21-45: the loop hands a turn's calls to the runner with the assistant
// message and the turn count, records the messages as After left them, and
// ends the run on any final terminate vote.
func TestLoopHandsTurnsToTheRunner_TS21_45(t *testing.T) {
	var guards guardLog
	var edited atomic.Int32
	after := func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
		if in.ToolName == "echo" {
			edited.Add(1)
			in.Result.Content = core.Content{core.TextBlock{Text: "after left this"}}
		}
		return core.AfterToolCallDecision{}
	}
	fp := faux.New(
		callTurn(faux.FauxToolCall("a1", "echo", "{}")),
		callTurn(faux.FauxToolCall("b1", "voter", "{}"), faux.FauxToolCall("b2", "echo", "{}")),
		finalTurn("never requested"),
	)
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{
		Tools: []core.Tool{echoTool("echo", nil), terminating("voter", 0)},
		Guard: guards.guard, After: after,
	}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}

	turnOf := map[string]int{}
	for _, g := range guards.seen() {
		if g.Assistant == nil {
			t.Errorf("Guard saw a nil Assistant for %s in a run", g.ToolUseID)
		}
		turnOf[g.ToolUseID] = g.TurnCount
	}
	// TurnCount is the zero-based index of the turn whose calls are running.
	if turnOf["a1"] != 0 || turnOf["b1"] != 1 || turnOf["b2"] != 1 {
		t.Errorf("Guard saw turn counts %v, want a1 at 0 and b1, b2 at 1", turnOf)
	}
	if res.StopReason != core.RunStopToolTerminate || fp.Calls() != 2 {
		t.Errorf("stop %q after %d requests; want tool_terminate after 2", res.StopReason, fp.Calls())
	}
	var results int
	for _, m := range res.Messages {
		if r, ok := m.(core.ToolResultMessage); ok {
			results++
			if r.ToolName == "echo" && r.Content.Text() != "after left this" {
				t.Errorf("the transcript holds %q for %s, want the message as After left it", r.Content.Text(), r.ToolUseID)
			}
		}
	}
	if results != 3 {
		t.Errorf("the transcript holds %d tool results, want one per call (3)", results)
	}
}

// TS-21-46: a response that hit the output token limit has its calls answered
// with the fixed notice and none of them run; that decision stays in the loop.
// The rest of the loop's behaviour is pinned by the root module's loop, batch,
// nested, golden and scripted tests, which pass with only their Config
// literals migrated.
func TestMaxTokensResponseRunsNoTool_TS21_46(t *testing.T) {
	var ran atomic.Int32
	fp := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("c1", "echo", `{"v":"x"}`)}, StopReason: core.StopReasonLength},
		finalTurn("retry later"),
	)
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{Tools: []core.Tool{echoTool("echo", &ran)}}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			kinds = append(kinds, "start:"+ev.ToolUseID)
		case core.ToolExecutionEndEvent:
			kinds = append(kinds, fmt.Sprintf("end:%s:error=%v", ev.ToolUseID, ev.IsError))
		case core.ToolResultEvent:
			kinds = append(kinds, "result:"+ev.Message.ToolUseID)
		}
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}
	if ran.Load() != 0 {
		t.Fatalf("the handler ran %d times for a truncated response", ran.Load())
	}
	if want := []string{"start:c1", "end:c1:error=true", "result:c1"}; !slices.Equal(kinds, want) {
		t.Errorf("events = %v, want %v", kinds, want)
	}
	msg, ok := toolResultIn(res.Messages, "c1")
	if want := fmt.Sprintf(maxTokensToolText, "echo"); !ok || !msg.IsError || msg.Content.Text() != want {
		t.Errorf("the truncated call's result = %q, want the fixed notice", msg.Content.Text())
	}
}
