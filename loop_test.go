package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

func TestRun_GoesOnWhileTheResponseCallsTools(t *testing.T) {
	for _, reason := range []core.StopReason{core.StopReasonStop, core.StopReasonToolUse, core.StopReasonStopSequence, "", "unrecognized"} {
		var ran atomic.Int32
		a, fp := scripted(t, Toolset{Tools: []core.Tool{echo("echo", &ran)}},
			faux.FauxAssistantMessage(reason, call("c1", "echo", `{}`)),
			faux.FauxAssistantMessage(core.StopReasonLength, faux.FauxText("cut short, but no tool call")),
			faux.Text("never requested"))
		res, err := a.Run(context.Background(), "go")
		if err != nil || ran.Load() != 1 || res.TurnCount != 2 || fp.Calls() != 2 || res.StopReason != core.RunStopEndTurn {
			t.Errorf("stop reason %q: handler ran %d, %d turns, %d requests, %q, %v; want 1, 2, 2, end_turn",
				reason, ran.Load(), res.TurnCount, fp.Calls(), res.StopReason, err)
		}
	}
}

// TestRun_ReportsTheRunOnItsResultAndStream checks the result, the transcript
// and the event sequence of a two-turn run.
func TestRun_ReportsTheRunOnItsResultAndStream(t *testing.T) {
	u := core.Usage{InputTokens: 10, OutputTokens: 5}
	a, _ := scripted(t, Toolset{Tools: []core.Tool{echo("echo", nil)}},
		faux.Turn{Blocks: []core.ContentBlock{call("c1", "echo", `{}`)}, StopReason: core.StopReasonToolUse, Usage: u},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("all done")}, StopReason: core.StopReasonStop, Usage: u})
	evs, res, err := drain(t, context.Background(), a, "do work")
	if err != nil || res.StopReason != core.RunStopEndTurn || res.LastReason != core.StopReasonStop || res.TurnCount != 2 ||
		res.FinalText() != "all done" || len(res.Messages) != 4 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	if !reflect.DeepEqual(a.Messages(), res.Messages) || res.Usage.InputTokens != 20 || res.Usage.OutputTokens != 10 {
		t.Errorf("transcript or usage differs from the run's: %+v", res.Usage)
	}
	start, done := evs[0].(core.AgentStartEvent), evs[len(evs)-1].(core.AgentDoneEvent)
	if start != (core.AgentStartEvent{Provider: "anthropic", Model: testModel}) || done.Result.TurnCount != 2 {
		t.Errorf("first event %+v, last %+v", start, done)
	}
	var turns []string
	for _, e := range evs {
		switch v := e.(type) {
		case core.TurnStartEvent:
			turns = append(turns, fmt.Sprint("start", v.TurnIndex))
		case core.TurnEndEvent:
			turns = append(turns, fmt.Sprintf("end%d%d%v", v.TurnIndex, len(v.ToolResults), v.ToolResults != nil))
		}
	}
	if want := []string{"start0", "end01true", "start1", "end10true"}; !slices.Equal(turns, want) {
		t.Errorf("turn events %v, want %v", turns, want)
	}
}

func TestRun_EndsOnAnAnswerARefusalOrAFailedTurn(t *testing.T) {
	for _, c := range []struct {
		name   string
		msg    core.AssistantMessage
		reason core.RunStopReason
		is     error
		text   string
	}{
		{"an answer", core.AssistantMessage{StopReason: core.StopReasonStop}, core.RunStopEndTurn, nil, ""},
		{"a refusal", core.AssistantMessage{StopReason: core.StopReasonRefusal, StopDetail: "cyber: declined"},
			core.RunStopRefusal, core.ErrRefusal, core.ErrRefusal.Error() + ": cyber: declined"},
		{"a bare refusal", core.AssistantMessage{StopReason: core.StopReasonRefusal}, core.RunStopRefusal, core.ErrRefusal, core.ErrRefusal.Error()},
		{"an aborted turn on a live context", core.AssistantMessage{StopReason: core.StopReasonAborted,
			Content: core.Content{call("c1", "echo", `{}`)}}, core.RunStopAborted, core.ErrAborted, core.ErrAborted.Error()},
		{"a failed turn with no provider error", core.AssistantMessage{StopReason: core.StopReasonError, ErrorMessage: "overloaded",
			Content: core.Content{call("c1", "echo", `{}`)}}, core.RunStopError, nil, "overloaded"},
	} {
		var ran atomic.Int32
		msg := c.msg
		res, err := mustNew(t, Config{Client: answering(&msg, nil), Toolset: Toolset{Tools: []core.Tool{echo("echo", &ran)}}}).Run(context.Background(), "go")
		if res.StopReason != c.reason || res.Error != err || res.LastReason != c.msg.StopReason || (c.is != nil && !errors.Is(err, c.is)) ||
			(c.text != "" && (err == nil || err.Error() != c.text)) || (c.text == "" && err != nil) {
			t.Errorf("%s: %q, %v; want %q, %q", c.name, res.StopReason, err, c.reason, c.text)
		}
		if c.reason == core.RunStopError && errors.Unwrap(err) != nil {
			t.Errorf("%s: the error wraps %v, but the provider gave none", c.name, errors.Unwrap(err))
		}
		if ran.Load() != 0 {
			t.Errorf("%s: a tool ran from a turn that short-circuits", c.name)
		}
	}
}

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

func TestRun_AFailedTurnsErrorReadsAsItsMessageAndWrapsTheProviders(t *testing.T) {
	errModel := errors.New("no such model")
	cause := fmt.Errorf("%w: %w", errModel, &statusError{404})
	for _, c := range []struct {
		name   string
		client core.ProviderClient
		text   string
	}{
		{"a half-streamed turn", faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("partial")}, Err: cause}), cause.Error()},
		{"a message beside the error", answering(&core.AssistantMessage{StopReason: core.StopReasonError, ErrorMessage: "not on the server"}, cause), "not on the server"},
		{"no message", answering(nil, cause), cause.Error()},
	} {
		a := mustNew(t, Config{Client: c.client})
		evs, res, err := drain(t, context.Background(), a, "go")
		var se *statusError
		if !errors.Is(err, errModel) || !errors.As(err, &se) || se.code != 404 || err.Error() != c.text || res.StopReason != core.RunStopError {
			t.Errorf("%s: Run = %q, %v; want %q over the provider's error", c.name, res.StopReason, err, c.text)
		}
		if done := evs[len(evs)-1].(core.AgentDoneEvent); !errors.Is(done.Result.Error, errModel) {
			t.Errorf("%s: AgentDoneEvent error = %v", c.name, done.Result.Error)
		}
		if am := lastAssistant(res.Messages); am == nil || am.StopReason != core.StopReasonError || am.ErrorMessage != c.text || am.Model != testModel {
			t.Errorf("%s: last message = %+v, want an error turn reading %q", c.name, am, c.text)
		}
	}
	_, err := mustNew(t, Config{Client: answering(nil, nil)}).Run(context.Background(), "go")
	if err == nil || err.Error() != "provider ended the stream with no assistant message" {
		t.Errorf("a stream with neither message nor error: %v", err)
	}
}

func TestRun_StopsOnItsBounds(t *testing.T) {
	heavy := core.Usage{InputTokens: 100_000, OutputTokens: 100_000}
	loop := []faux.Turn{calls(call("c1", "echo", "{}")), calls(call("c2", "echo", "{}")), calls(call("c3", "echo", "{}"))}
	for _, c := range []struct {
		name     string
		cfg      Config
		turns    []faux.Turn
		reason   core.RunStopReason
		is       error
		requests int
	}{
		{"MaxTurns, after the turn's results", Config{MaxTurns: 2}, loop, core.RunStopMaxTurns, core.ErrMaxTurns, 2},
		{"MaxCostUSD, before the next request", Config{Model: "claude-opus-5-5", MaxCostUSD: 0.05},
			[]faux.Turn{{Blocks: []core.ContentBlock{call("c1", "echo", "{}")}, StopReason: core.StopReasonToolUse, Usage: heavy}}, core.RunStopBudgetExceeded, core.ErrBudgetExceeded, 1},
		{"MaxCostUSD, never by an unpriced turn", Config{Model: "claude-3-5-sonnet", MaxCostUSD: 0.01},
			[]faux.Turn{{Blocks: []core.ContentBlock{call("c1", "echo", "{}")}, StopReason: core.StopReasonToolUse, Usage: reported(100_000, 100_000)}}, core.RunStopEndTurn, nil, 2},
		{"Timeout", Config{Timeout: 20 * time.Millisecond},
			[]faux.Turn{{Blocks: []core.ContentBlock{faux.FauxText("slow")}, Delay: time.Minute}}, core.RunStopTimeout, context.DeadlineExceeded, 1},
	} {
		fp := faux.New(c.turns...)
		cfg := c.cfg
		cfg.Client, cfg.Toolset = fp, Toolset{Tools: []core.Tool{echo("echo", nil)}}
		res, err := mustNew(t, cfg).Run(context.Background(), "go")
		if res.StopReason != c.reason || (c.is != nil && !errors.Is(err, c.is)) || (c.is == nil && err != nil) || fp.Calls() != c.requests {
			t.Errorf("%s: %q after %d requests, %v; want %q after %d", c.name, res.StopReason, fp.Calls(), err, c.reason, c.requests)
		}
		if c.reason == core.RunStopMaxTurns {
			if _, ok := res.Messages[len(res.Messages)-1].(core.ToolResultMessage); !ok || res.TurnCount != 2 {
				t.Errorf("%s: the run left the last turn unanswered", c.name)
			}
		}
	}
}

func TestRun_CancellationAbortsTheRun(t *testing.T) {
	t.Run("during a model call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		fp := faux.New(faux.Turn{Delay: time.Minute})
		a := mustNew(t, Config{Client: fp})
		go func() { testutil.Within(5*time.Second, func() bool { return fp.Calls() == 1 }); cancel() }()
		res, err := a.Run(ctx, "go")
		am := lastAssistant(res.Messages)
		if res.StopReason != core.RunStopAborted || !errors.Is(err, core.ErrAborted) || !errors.Is(err, context.Canceled) ||
			am == nil || am.StopReason != core.StopReasonAborted || am.ErrorMessage == "" {
			t.Fatalf("Run = %q, %v, last %+v; want aborted, the marker kept", res.StopReason, err, am)
		}
	})
	t.Run("during a tool batch, at the turn boundary", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancels := tool("cancels", func(context.Context, json.RawMessage) core.ToolResult { cancel(); return core.OKResult(nil) })
		a, fp := scripted(t, Toolset{Tools: []core.Tool{cancels}}, calls(call("c1", "cancels", "{}")), faux.Text("never"))
		res, err := a.Run(ctx, "go")
		if res.StopReason != core.RunStopAborted || !errors.Is(err, context.Canceled) || fp.Calls() != 1 {
			t.Fatalf("Run = %q, %v after %d requests; want aborted after 1", res.StopReason, err, fp.Calls())
		}
		if _, ok := res.Messages[len(res.Messages)-1].(core.ToolResultMessage); !ok {
			t.Fatal("the transcript does not end with the batch's result")
		}
	})
}

func TestRun_ATruncatedResponseRunsNoToolAndAnswersEachCall(t *testing.T) {
	var ran atomic.Int32
	a, fp := scripted(t, Toolset{Tools: []core.Tool{echo("echo", &ran)}},
		faux.FauxAssistantMessage(core.StopReasonLength, call("c1", "echo", `{"v":"/et`+`"}`), call("c2", "echo", `{}`)),
		calls(call("c3", "echo", `{"v":"/etc/hosts"}`)), faux.Text("done"))
	evs, res, err := drain(t, context.Background(), a, "go")
	if err != nil || ran.Load() != 1 || res.TurnCount != 3 {
		t.Fatalf("Run = %v; handler ran %d over %d turns, want only the re-issued call", err, ran.Load(), res.TurnCount)
	}
	starts, ends, results := executions(evs)
	for _, id := range []string{"c1", "c2"} {
		want := "Tool call \"echo\" was not executed: the response hit the output token limit,\n" +
			"so its arguments may be truncated. Re-issue the tool call with complete arguments."
		if r, _ := resultOf(fp.Requests()[1].Messages, id); !r.IsError || r.Content.Text() != want {
			t.Errorf("%s was answered %q, want the notice", id, r.Content.Text())
		}
		if starts[id] != 1 || ends[id] != 1 || results[id] != 1 {
			t.Errorf("%s: %d/%d/%d start/end/result events, want one each", id, starts[id], ends[id], results[id])
		}
	}
}

// TestRun_SendsTheConfiguredRequest checks the prefix, prompt, system prompt,
// tools, output bound and effort every request carries.
func TestRun_SendsTheConfiguredRequest(t *testing.T) {
	alpha, beta, gamma := echo("alpha", nil), echo("beta", nil), echo("gamma", nil)
	alpha.PromptGuidelines = []string{"Use alpha first."}
	prefix := []core.Message{core.UserMessage{Content: core.Content{core.TextBlock{Text: "preamble"}}}}
	for _, c := range []struct {
		cfg       Config
		maxTokens int
		effort    Effort
	}{
		{Config{System: "You are a test.", Prefix: prefix}, DefaultMaxTokens, ""},
		{Config{Provider: "openrouter", Model: "STANDARD", MaxTokens: 512}, 512, EffortMedium},
		{Config{Provider: "openrouter", Model: "STANDARD", Effort: "HIGH"}, DefaultMaxTokens, EffortHigh},
	} {
		fp := faux.New(calls(call("c1", "alpha", "{}")), faux.Text("done"))
		cfg := c.cfg
		cfg.Client = fp
		cfg.Toolset = Toolset{Tools: []core.Tool{alpha, beta, gamma}, Policy: core.ToolPolicy{Tools: []core.Tool{gamma, alpha}}}
		a := mustNew(t, cfg)
		if _, err := a.Run(context.Background(), "the question"); err != nil {
			t.Fatal(err)
		}
		system := prompt.Build(c.cfg.System, a.Runner().Tools())
		for i, req := range fp.Requests() {
			var names []string
			for _, w := range req.Tools {
				names = append(names, w.Name)
			}
			if !reflect.DeepEqual(req.Prefix, core.Messages(c.cfg.Prefix)) || req.Messages[0].(core.UserMessage).Content.Text() != "the question" ||
				len(req.System) != 1 || req.System[0].(core.TextBlock).Text != system || !slices.Equal(names, []string{"gamma", "alpha"}) ||
				*req.MaxTokens != c.maxTokens || req.Effort != c.effort {
				t.Errorf("%+v: request %d = %+v", c.cfg, i, req)
			}
		}
		if !strings.Contains(system, "Use alpha first.") || !strings.HasPrefix(system, c.cfg.System) {
			t.Errorf("the system prompt %q lacks the tool's guideline", system)
		}
	}
}

func TestRun_StampsTheModelOnUnstampedMessages(t *testing.T) {
	for _, served := range []string{"", "served-model"} {
		fp := faux.New(faux.Text("hi"))
		fp.ModelID = served
		evs, res, _ := drain(t, context.Background(), mustNew(t, Config{Client: fp}), "go")
		want := served
		if want == "" {
			want = testModel
		}
		for _, e := range evs {
			var m core.AssistantMessage
			switch v := e.(type) {
			case core.MessageStartEvent:
				m = v.Message
			case core.MessageUpdateEvent:
				m = v.Message
			case core.MessageEndEvent:
				m = v.Message
			default:
				continue
			}
			if m.Model != want {
				t.Errorf("served %q: %T names %q, want %q", served, e, m.Model, want)
			}
		}
		if am := lastAssistant(res.Messages); am.Model != want {
			t.Errorf("served %q: the transcript names %q, want %q", served, am.Model, want)
		}
	}
}

// reported is a usage the provider reported, as a provider decodes one.
func reported(in, out int64) core.Usage {
	var u core.Usage
	core.UsageWire{InputTokens: &in, OutputTokens: &out}.Into(&u)
	return u
}

func TestRun_PricesEachTurnAndFlagsTheUnpriced(t *testing.T) {
	vendor := reported(10, 5)
	vendor.SetCost(0.0001)
	flagged := reported(10, 5)
	flagged.Unpriced = true
	for _, c := range []struct {
		name, provider, model string
		usage                 core.Usage
		unpriced              bool
		cost                  float64 // -1: more than zero
	}{
		{"a listed Anthropic row", "anthropic", "claude-opus-5-5", reported(10, 5), false, -1},
		{"an unlisted Anthropic id", "anthropic", "claude-3-5-sonnet", reported(10, 5), true, 0},
		{"no reported usage", "anthropic", "claude-3-5-sonnet", core.Usage{}, false, 0},
		{"OpenRouter without a cost", "openrouter", "some/unlisted", reported(10, 5), true, 0},
		{"OpenRouter with the vendor's cost", "openrouter", "some/unlisted", vendor, false, 0.0001},
		{"Ollama, whose price is zero", "ollama", "an-unlisted:7b", reported(10, 5), false, 0},
		{"a flag the client set", "ollama", "STANDARD", flagged, true, 0},
	} {
		fp := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}, Usage: c.usage})
		a := mustNew(t, Config{Client: fp, Provider: c.provider, Model: c.model})
		evs, res, err := drain(t, context.Background(), a, "go")
		if err != nil {
			t.Fatal(err)
		}
		done := evs[len(evs)-1].(core.AgentDoneEvent)
		u := res.Usage
		if u.Unpriced != c.unpriced || done.Usage.Unpriced != c.unpriced || a.Usage().Unpriced != c.unpriced ||
			(c.cost < 0 && u.CostUSD <= 0) || (c.cost >= 0 && u.CostUSD != c.cost) || (u.Reported() && !u.Has(core.UsageCostUSD)) {
			t.Errorf("%s: usage %+v; want unpriced %v, cost %v", c.name, u, c.unpriced, c.cost)
		}
		for _, e := range evs {
			if te, ok := e.(core.TurnEndEvent); ok && te.Usage.Unpriced != c.usage.Unpriced {
				t.Errorf("%s: the turn's own usage was flagged", c.name)
			}
		}
	}
}

// heavy is a tool-calling turn that reports tokens of context.
func heavy(tokens int64, blocks ...core.ContentBlock) faux.Turn {
	return faux.Turn{Blocks: blocks, StopReason: core.StopReasonToolUse, Usage: core.Usage{InputTokens: tokens}}
}

func big(name string, n int) core.Tool {
	return returning(name, core.ToolResult{OK: true, Text: strings.Repeat("x", n)})
}

func elided(msgs core.Messages) (ids []string) {
	for _, m := range msgs {
		if r, ok := m.(core.ToolResultMessage); ok && strings.HasSuffix(r.Content.Text(), "elided; call again if needed]") {
			ids = append(ids, r.ToolUseID)
		}
	}
	return ids
}

func TestRun_PrunesOldToolResultsFromTheRequestOnly(t *testing.T) {
	fp := faux.New(heavy(400_000, call("c1", "big", `{}`)), heavy(400_000, call("c2", "big", `{}`)),
		heavy(400_000, call("c3", "big", `{}`), call("c4", "big", `{}`)), faux.Text("done"))
	a := mustNew(t, Config{Client: fp, Toolset: Toolset{Tools: []core.Tool{big("big", 1500)}}, Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	reqs := fp.Requests()
	if got := elided(reqs[1].Messages); len(got) != 0 {
		t.Errorf("request 1 elided %v, within KeepTurns", got)
	}
	last := reqs[len(reqs)-1].Messages
	if got := elided(last); !slices.Equal(got, []string{"c1", "c2"}) {
		t.Errorf("the last request elided %v, want c1 and c2", got)
	}
	if r, _ := resultOf(last, "c1"); r.Content.Text() != "[result of big (1500 bytes) elided; call again if needed]" || r.ToolName != "big" {
		t.Errorf("c1 was sent as %q", r.Content.Text())
	}
	if r, _ := resultOf(last, "c4"); r.Content.Text() != strings.Repeat("x", 1500) {
		t.Error("the latest turn's result was pruned")
	}
	if got := elided(a.Messages()); len(got) != 0 || !reflect.DeepEqual(a.Messages(), res.Messages) {
		t.Errorf("the transcript holds elided results %v", got)
	}
}

// sized is a provider whose usage reports exactly the request's estimate, or
// nothing when silent, and that calls a tool for the first turns.
func sized(turns int, silent bool, sent *[]core.Request) core.ProviderClient {
	return streamFunc(func(_ context.Context, req core.Request) *core.EventStream {
		i := len(*sent)
		*sent = append(*sent, req)
		msg := core.AssistantMessage{StopReason: core.StopReasonStop, Content: core.Content{core.TextBlock{Text: "done"}}}
		if i < turns {
			msg.StopReason, msg.Content = core.StopReasonToolUse, core.Content{call(fmt.Sprint("c", i), "big", `{}`)}
		}
		if !silent {
			msg.Usage = core.Usage{InputTokens: charTokens(req.Messages)}
		}
		s := core.NewEventStream(core.StreamOptions{})
		s.End(core.StreamResult{Message: &msg})
		return s
	})
}

func TestRun_PruningOnceStartedStaysOnAndFitsTheWindow(t *testing.T) {
	for _, c := range []struct {
		name   string
		silent bool
		turns  int
		size   int
	}{
		{"anchored on usage", false, 6, 100_000},
		{"estimated without usage", true, 3, 300_000},
	} {
		var sent []core.Request
		a := mustNew(t, Config{Client: sized(c.turns, c.silent, &sent), Model: "claude-opus-4-5",
			Toolset: Toolset{Tools: []core.Tool{big("big", c.size)}}, Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
		if res, err := a.Run(context.Background(), "go"); err != nil || res.StopReason != core.RunStopEndTurn {
			t.Fatalf("%s: Run = %q, %v", c.name, res.StopReason, err)
		}
		first := -1
		for i, req := range sent {
			n := len(elided(req.Messages))
			if n > 0 && first < 0 {
				first = i
			}
			if first >= 0 && n == 0 {
				t.Errorf("%s: request %d was pruned and request %d was not", c.name, first, i)
			}
		}
		if first < 0 {
			t.Errorf("%s: nothing was pruned", c.name)
		}
	}
}

func TestRun_RefusesARequestLargerThanTheContextWindow(t *testing.T) {
	fp := faux.New(heavy(2_000_000, call("c1", "big", `{}`)), faux.Text("never"))
	a := mustNew(t, Config{Client: fp, Toolset: Toolset{Tools: []core.Tool{big("big", 10)}}, Prune: PruneOptions{Threshold: 0.35, KeepTurns: 1}})
	res, err := a.Run(context.Background(), "go")
	if res.StopReason != core.RunStopError || err == nil || fp.Calls() != 1 ||
		!strings.Contains(err.Error(), `model "test-model"`) || !strings.Contains(err.Error(), "1000000-token context window") {
		t.Fatalf("Run = %q, %v after %d requests; want the window error and no second request", res.StopReason, err, fp.Calls())
	}
}

// explosive marshals by panicking: a bug the loop's own code can trip over.
type explosive struct{ core.TextBlock }

func (explosive) MarshalJSON() ([]byte, error) { panic("marshal exploded") }

func TestRun_ContainsPanicsInTheProviderAndTheLoop(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"the provider", Config{Client: streamFunc(func(context.Context, core.Request) *core.EventStream { panic("provider exploded") })},
			"agentkit: panic in provider: provider exploded"},
		{"the loop", Config{Client: faux.New(), Prefix: []core.Message{core.UserMessage{Content: core.Content{explosive{}}}}},
			"agentkit: panic in run: marshal exploded"},
	} {
		evs, res, err := drain(t, context.Background(), mustNew(t, c.cfg), "go")
		am := lastAssistant(res.Messages)
		if err == nil || err.Error() != c.want || res.StopReason != core.RunStopError || am == nil || am.StopReason != core.StopReasonError {
			t.Errorf("%s: Run = %q, %v, last %+v; want %q and an error marker", c.name, res.StopReason, err, am, c.want)
		}
		if _, ok := evs[len(evs)-1].(core.AgentDoneEvent); !ok {
			t.Errorf("%s: the stream did not end with AgentDoneEvent", c.name)
		}
	}
}

func TestStream_NeverBlocksOnAConsumerThatDoesNotRead(t *testing.T) {
	var turns []faux.Turn
	for i := range 20 {
		turns = append(turns, calls(faux.FauxText(strings.Repeat("streamed ", 200)), call(fmt.Sprint("c", i), "echo", "{}")))
	}
	a, fp := scripted(t, Toolset{Tools: []core.Tool{echo("echo", nil)}}, turns...)
	fp.ChunkSize = 8
	s, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res, err := s.RunResult(); err != nil || res.TurnCount != 21 {
		t.Fatalf("RunResult = %d turns, %v; want 21", res.TurnCount, err)
	}
}
