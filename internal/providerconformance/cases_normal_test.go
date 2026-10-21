package providerconformance

import (
	"math"
	"slices"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// What the cases script and expect, written once.
const (
	greeting          = "Hello from the conformance suite."
	toolCallID        = "call_conformance_1"
	toolArgs          = `{"v":"conformance"}`
	thinkingText      = "Plan: call echo once."
	thinkingSignature = "sig-conformance"
	finalAnswer       = "All done."
)

// lastAssistant is the last assistant message of the run.
func (r *caseRun) lastAssistant() (core.AssistantMessage, bool) {
	for i := len(r.res.Messages) - 1; i >= 0; i-- {
		if a, ok := r.res.Messages[i].(core.AssistantMessage); ok {
			return a, true
		}
	}
	return core.AssistantMessage{}, false
}

// assistant is the i-th assistant message of the run, from 0.
func (r *caseRun) assistant(i int) (core.AssistantMessage, bool) {
	for _, m := range r.res.Messages {
		if a, ok := m.(core.AssistantMessage); ok {
			if i == 0 {
				return a, true
			}
			i--
		}
	}
	return core.AssistantMessage{}, false
}

// turnUsageOf is the usage the TurnEndEvent of turn i reported.
func (r *caseRun) turnUsageOf(i int) (core.Usage, bool) {
	for _, e := range r.events {
		if te, ok := e.(core.TurnEndEvent); ok && te.TurnIndex == i {
			return te.Usage, true
		}
	}
	return core.Usage{}, false
}

// checkEndTurn requires the run to have ended normally after turns turns.
func (r *caseRun) checkEndTurn(turns int) {
	r.tb.Helper()
	if r.res.StopReason != core.RunStopEndTurn || r.runErr != nil || r.res.Error != nil {
		r.fail("the run ended %q with error %v (result error %v), want %q and none", r.res.StopReason, r.runErr, r.res.Error, core.RunStopEndTurn)
	}
	if r.res.TurnCount != turns {
		r.fail("the run took %d turns, want %d", r.res.TurnCount, turns)
	}
}

// textCase is one text turn with the usage that shows the cache netting.
var textCase = conformanceCase{
	name:   "text",
	prompt: "Say hello.",
	turns: func(providerInfo) []logicalTurn {
		return []logicalTurn{{
			blocks: []block{textBlock(greeting)},
			end:    endNormal,
			// 100 prompt tokens, 60 of them cache reads, and 20 output tokens; a
			// vendor that reports a cost reports this one.
			usage: &turnUsage{prompt: 100, cacheRead: 60, output: 20, cost: 0.00042},
		}}
	},
	check: checkText,
}

func checkText(r *caseRun) {
	r.tb.Helper()
	final, ok := r.lastAssistant()
	if !ok || final.StopReason != core.StopReasonStop {
		r.fail("the assistant message ends %q, want %q", final.StopReason, core.StopReasonStop)
	}
	r.checkEndTurn(1)
	if got := r.res.FinalText(); got != greeting {
		r.fail("the final text is %q, want %q", got, greeting)
	}
	if len(r.summaries) != 1 {
		r.fail("%d requests were decoded, want 1", len(r.summaries))
		return
	}
	if !slices.Contains(r.summaries[0].UserTexts, r.c.prompt) {
		r.fail("the request carries the user texts %v, want %q among them", r.summaries[0].UserTexts, r.c.prompt)
	}

	u, ok := r.turnUsageOf(0)
	if !ok {
		r.fail("no TurnEndEvent for the turn")
		return
	}
	if u.InputTokens != 40 || u.CacheReadTokens != 60 || u.OutputTokens != 20 {
		r.fail("the turn's usage is input %d, cache read %d, output %d, want 40, 60 and 20 (the prompt of 100 net of the 60 read from the cache)",
			u.InputTokens, u.CacheReadTokens, u.OutputTokens)
	}
	if u.ReasoningTokens > u.OutputTokens {
		r.fail("the turn reports %d reasoning tokens of %d output tokens", u.ReasoningTokens, u.OutputTokens)
	}
	if !u.Has(core.UsageCostUSD) {
		r.fail("the turn's usage carries no cost")
	}
	want := provider.ComputeCost(&r.sel.Model, u)
	if r.p.vendor.reportsCost {
		want = r.script[0].usage.cost
	}
	if math.Abs(u.CostUSD-want) > 1e-12 {
		r.fail("the turn costs $%g, want $%g", u.CostUSD, want)
	}
	if r.res.Usage.Unpriced {
		r.fail("the run's usage is flagged unpriced")
	}
}

// toolRound is the two turns of a tool call: turn 1 calls echo, turn 2 answers.
func toolRound(first ending) func(providerInfo) []logicalTurn {
	return func(providerInfo) []logicalTurn {
		return []logicalTurn{
			{blocks: []block{toolBlock(toolCallID, "echo", toolArgs)}, end: first},
			oneText(finalAnswer),
		}
	}
}

// toolCallCase ends its first turn with the vendor's tool-call stop.
var toolCallCase = conformanceCase{
	name:   "tool_call",
	prompt: "Call echo.",
	turns:  toolRound(endToolStop),
	check:  checkToolRound,
}

// toolCallStopCase is toolCallCase with the vendor's plain stop, which the loop
// must not mistake for the end: it continues on a tool call whatever the stop
// reason.
var toolCallStopCase = conformanceCase{
	name:   "tool_call_stop",
	prompt: "Call echo.",
	turns:  toolRound(endNormal),
	check:  checkToolRound,
}

// checkToolRound requires echo to have run once, and the second request to carry
// the call on the assistant message and, after it, the call's result.
func checkToolRound(r *caseRun) {
	r.tb.Helper()
	if got := r.echoCalls.Load(); got != 1 {
		r.fail("echo ran %d times, want once", got)
	}
	if first, ok := r.assistant(0); !ok {
		r.fail("no assistant message in the transcript")
	} else if calls := core.ExtractToolUse(&first); len(calls) != 1 || calls[0].ID != toolCallID || calls[0].Name != "echo" ||
		!jsonEqual(calls[0].Input, []byte(toolArgs)) {
		r.fail("the first assistant message calls %+v, want %s to echo with %s", calls, toolCallID, toolArgs)
	}
	if len(r.summaries) != 2 {
		r.fail("%d requests were decoded, want 2", len(r.summaries))
		return
	}
	first, second := r.summaries[0], r.summaries[1]
	if !slices.Contains(first.Tools, "echo") {
		r.fail("the first request declares the tools %v, want echo among them", first.Tools)
	}
	if len(second.Assistants) != 1 {
		r.fail("the second request carries %d assistant messages, want 1", len(second.Assistants))
	} else if calls := second.Assistants[0].ToolCalls; len(calls) != 1 || calls[0].ID != toolCallID || calls[0].Name != "echo" ||
		!jsonEqual(calls[0].Args, []byte(toolArgs)) {
		r.fail("the second request replays the calls %+v, want %s to echo with %s", calls, toolCallID, toolArgs)
	}
	if len(second.Results) != 1 || second.Results[0].CallID != toolCallID || !strings.Contains(second.Results[0].Text, "echoed") {
		r.fail("the second request carries the results %+v, want one for %s whose text contains \"echoed\"", second.Results, toolCallID)
	}
	if want := []string{"user", "assistant", "tool"}; !slices.Equal(second.Order, want) {
		r.fail("the second request's messages are %v, want %v: the result follows the call", second.Order, want)
	}
	r.checkEndTurn(2)
	if got := r.res.FinalText(); got != finalAnswer {
		r.fail("the final text is %q, want %q", got, finalAnswer)
	}
}

// thinkingCase has the model think before it calls echo, and requires the
// thinking to be kept as thinking, never as text, and to be replayed to the
// provider in the vendor's raw form.
var thinkingCase = conformanceCase{
	name:   "thinking",
	prompt: "Think, then call echo.",
	turns: func(providerInfo) []logicalTurn {
		return []logicalTurn{
			{
				blocks: []block{
					thinkingBlock(thinkingText, thinkingSignature),
					toolBlock(toolCallID, "echo", toolArgs),
				},
				end: endToolStop,
			},
			oneText(finalAnswer),
		}
	},
	check: checkThinking,
}

func checkThinking(r *caseRun) {
	r.tb.Helper()
	checkToolRound(r)

	var thinking, text int
	for _, e := range providerEvents(r.events, 0) {
		switch e.(type) {
		case core.ThinkingStartEvent, core.ThinkingDeltaEvent, core.ThinkingEndEvent:
			thinking++
		case core.TextStartEvent, core.TextDeltaEvent, core.TextEndEvent:
			text++
		}
	}
	if thinking == 0 {
		r.fail("turn 1 emitted no thinking event")
	}
	if text != 0 {
		r.fail("turn 1 emitted %d text events for a turn that has none: the thinking leaked into text", text)
	}
	for _, e := range r.events {
		var s string
		switch v := e.(type) {
		case core.TextDeltaEvent:
			s = v.Delta
		case core.TextEndEvent:
			s = v.Text
		default:
			continue
		}
		if s != "" && (strings.Contains(thinkingText, s) || strings.Contains(s, thinkingText)) {
			r.fail("a text event carries %q, which is part of the thinking", s)
		}
	}

	first, ok := r.assistant(0)
	if !ok {
		return
	}
	if len(first.Content) != 2 {
		r.fail("the first assistant message has %d blocks, want a thinking block, then the tool use", len(first.Content))
	} else {
		if tb, ok := first.Content[0].(core.ThinkingBlock); !ok || tb.Thinking != thinkingText {
			r.fail("the first block is %#v, want a ThinkingBlock holding %q", first.Content[0], thinkingText)
		}
		if _, ok := first.Content[1].(core.ToolUseBlock); !ok {
			r.fail("the second block is %#v, want the tool use", first.Content[1])
		}
	}
	for _, b := range first.Content {
		if tb, ok := b.(core.TextBlock); ok && strings.Contains(tb.Text, thinkingText) {
			r.fail("a text block holds the thinking: %q", tb.Text)
		}
	}

	if len(r.summaries) != 2 || len(r.summaries[1].Assistants) != 1 {
		return // checkToolRound has said so
	}
	a := r.summaries[1].Assistants[0]
	if want := r.p.vendor.rawThinking(thinkingBlock(thinkingText, thinkingSignature)); !jsonEqual(a.RawThinking, want) {
		r.fail("the second request replays the thinking as %s, want it as received: %s", a.RawThinking, want)
	}
	if a.Thinking != thinkingText {
		r.fail("the second request replays the thinking text %q, want %q", a.Thinking, thinkingText)
	}
	if strings.Contains(a.Text, thinkingText) {
		r.fail("the second request sends the thinking as visible content: %q", a.Text)
	}
}
