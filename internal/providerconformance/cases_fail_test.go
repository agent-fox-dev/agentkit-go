package providerconformance

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// What the failing cases script and expect.
const (
	partialAnswer   = "Partial answer"
	badRequest      = "conformance-bad-request"
	workingOnIt     = "Working on it"
	upstreamFailure = "conformance-upstream-failure"
)

// terminalError is the run's last terminal ErrorEvent.
func (r *caseRun) terminalError() (core.ErrorEvent, bool) {
	for i := len(r.events) - 1; i >= 0; i-- {
		if ee, ok := r.events[i].(core.ErrorEvent); ok && ee.Terminal {
			return ee, true
		}
	}
	return core.ErrorEvent{}, false
}

// checkFailedRun requires the run to have stopped on the error, after one
// request, without a tool having run: no echo call, and no
// ToolExecutionStartEvent.
func (r *caseRun) checkFailedRun() {
	r.tb.Helper()
	if r.res.StopReason != core.RunStopError {
		r.fail("the run ended %q, want %q", r.res.StopReason, core.RunStopError)
	}
	if r.runErr == nil || r.res.Error == nil {
		r.fail("the run ended with the error %v (result error %v), want one", r.runErr, r.res.Error)
	}
	if len(r.requests) != 1 {
		r.fail("the fake recorded %d requests, want exactly 1: a failure is retried by no provider here", len(r.requests))
	}
	if n := r.echoCalls.Load(); n != 0 {
		r.fail("echo ran %d times, want never", n)
	}
	for _, e := range r.events {
		if _, ok := e.(core.ToolExecutionStartEvent); ok {
			r.fail("a ToolExecutionStartEvent was emitted for a turn that failed")
		}
	}
}

// checkFailedMessage requires the last assistant message to be the failed one
// and to keep the text it had streamed.
func (r *caseRun) checkFailedMessage(keeps string) {
	r.tb.Helper()
	last, ok := r.lastAssistant()
	switch {
	case !ok:
		r.fail("the transcript has no assistant message")
	case last.StopReason != core.StopReasonError:
		r.fail("the last assistant message ends %q, want %q", last.StopReason, core.StopReasonError)
	case !strings.Contains(last.Content.Text(), keeps):
		r.fail("the last assistant message holds %q, want it to keep %q", last.Content.Text(), keeps)
	}
}

// cutOffCase has the body end in the middle of a tool call, without the final
// signal of the vendor.
var cutOffCase = conformanceCase{
	name:   "cut_off",
	prompt: "Call echo.",
	turns: func(providerInfo) []logicalTurn {
		return []logicalTurn{{
			blocks: []block{textBlock(partialAnswer), toolBlock(toolCallID, "echo", toolArgs)},
			end:    endCutOff,
		}}
	},
	check: checkCutOff,
}

// checkCutOff is TS-30-23 and TS-30-24 (30-REQ-5.1, 30-REQ-5.2): the provider
// says the stream was cut, by its own ErrStreamTruncated; the run stops after one
// request; the partial call is never executed, whether the provider kept it in
// the failed message or dropped it; and the text that was complete is kept.
func checkCutOff(r *caseRun) {
	r.tb.Helper()
	ee, ok := r.terminalError()
	if !ok {
		r.fail("no terminal ErrorEvent")
		return
	}
	if !errors.Is(ee.Err, r.p.truncated) {
		r.fail("the terminal error is %v, want one that matches %v", ee.Err, r.p.truncated)
	}
	if !errors.Is(r.runErr, r.p.truncated) {
		r.fail("the run's error is %v, want one that matches %v", r.runErr, r.p.truncated)
	}
	r.checkFailedRun()
	r.checkFailedMessage(partialAnswer)
}

// errorStatusCase answers HTTP 400 with the vendor's error body.
var errorStatusCase = conformanceCase{
	name:   "error_status",
	prompt: "Say hello.",
	turns: func(providerInfo) []logicalTurn {
		return []logicalTurn{{end: endStatus, status: 400, message: badRequest}}
	},
	check: checkErrorStatus,
}

// checkErrorStatus is TS-30-25 (30-REQ-5.3): the terminal error is the
// provider's *StatusError with the code 400, its text starts with the provider's
// name and "HTTP 400" and holds the vendor's message, and a 400 is retried by no
// provider.
func checkErrorStatus(r *caseRun) {
	r.tb.Helper()
	ee, ok := r.terminalError()
	if !ok {
		r.fail("no terminal ErrorEvent")
		return
	}
	if code, ok := r.p.statusCode(ee.Err); !ok || code != 400 {
		r.fail("the terminal error is %v, want the provider's *StatusError with Code 400", ee.Err)
	}
	prefix := r.p.name + ": HTTP 400"
	for _, text := range []string{ee.Message, errText(ee.Err)} {
		if !strings.HasPrefix(text, prefix) || !strings.Contains(text, badRequest) {
			r.fail("the error text is %q, want it to start %q and contain %q", text, prefix, badRequest)
		}
	}
	r.checkFailedRun()
}

// errText is err's text, or "" for nil.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// errorInStreamCase has the vendor report an error after some text.
var errorInStreamCase = conformanceCase{
	name:   "error_in_stream",
	prompt: "Say hello.",
	turns: func(providerInfo) []logicalTurn {
		return []logicalTurn{{
			blocks:  []block{textBlock(workingOnIt)},
			end:     endInStream,
			message: upstreamFailure,
		}}
	},
	check: checkErrorInStream,
}

// checkErrorInStream is TS-30-26 (30-REQ-5.4): the vendor's message is in the
// terminal error, with no requirement on a prefix (Anthropic's carries none of
// its own); the run stops after one request, since an error after content is
// retried by no provider; and the text streamed before the error is kept.
func checkErrorInStream(r *caseRun) {
	r.tb.Helper()
	ee, ok := r.terminalError()
	if !ok {
		r.fail("no terminal ErrorEvent")
		return
	}
	for _, text := range []string{ee.Message, errText(ee.Err)} {
		if !strings.Contains(text, upstreamFailure) {
			r.fail("the error text is %q, want it to contain %q", text, upstreamFailure)
		}
	}
	r.checkFailedRun()
	r.checkFailedMessage(workingOnIt)
}

// failingCases are the cases whose turn fails.
var failingCases = []conformanceCase{cutOffCase, errorStatusCase, errorInStreamCase}

// TS-30-15 (30-REQ-3.4), at the provider events themselves: in each case that
// fails, the turn's last two provider events are a terminal ErrorEvent and a
// MessageEndEvent whose message has StopReasonError.
func TestFailedTurnsEndWithErrorThenMessageEnd_TS30_15(t *testing.T) {
	for _, p := range providerTable {
		for _, c := range failingCases {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				r := runCase(t, p, c, runOptions{})
				evs := providerEvents(r.events, 0)
				if len(evs) < 2 {
					t.Fatalf("%d provider events, want at least 2", len(evs))
				}
				ee, ok := evs[len(evs)-2].(core.ErrorEvent)
				if !ok || !ee.Terminal {
					t.Errorf("the second to last provider event is %#v, want a terminal ErrorEvent", evs[len(evs)-2])
				}
				me, ok := evs[len(evs)-1].(core.MessageEndEvent)
				if !ok || me.Message.StopReason != core.StopReasonError {
					t.Errorf("the last provider event is %#v, want a MessageEndEvent with StopReasonError", evs[len(evs)-1])
				}
			})
		}
	}
}

// TS-30-13 (30-REQ-3.1), over all seven cases: every request any case makes
// carries the model id and the effort the catalog selects, in the vendor's form.
func TestEveryRequestCarriesTheSelectedModelAndEffort_TS30_13(t *testing.T) {
	if len(conformanceCases) != 7 {
		t.Fatalf("the suite has %d cases, want the seven of the spec", len(conformanceCases))
	}
	for _, p := range providerTable {
		sel, err := catalog.Select(p.name, "STANDARD", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range conformanceCases {
			t.Run(p.name+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				r := runCase(t, p, c, runOptions{})
				if len(r.summaries) == 0 {
					t.Fatal("no request was decoded")
				}
				for i, s := range r.summaries {
					if s.Model != sel.Model.ID {
						t.Errorf("request %d names model %q, want %q", i+1, s.Model, sel.Model.ID)
					}
					if s.Effort != string(sel.Effort) {
						t.Errorf("request %d carries effort %q, want %q", i+1, s.Effort, sel.Effort)
					}
				}
			})
		}
	}
}

// failedRun is the caseRun a provider that conforms leaves after the failing
// case c: the run stopped on the error after one request, the terminal
// ErrorEvent is the one the case expects, no tool ran, and the failed message
// keeps what the case streamed.
func failedRun(c conformanceCase, rec reporter) *caseRun {
	p := providerByName("ollama")
	var err error
	var kept string
	switch c.name {
	case "cut_off":
		err, kept = fmt.Errorf("%w", p.truncated), partialAnswer
	case "error_status":
		err = &ollama.StatusError{Code: 400, Text: "ollama: HTTP 400: " + badRequest}
	case "error_in_stream":
		err, kept = errors.New("ollama: "+upstreamFailure), workingOnIt
	}
	return &caseRun{
		tb: rec, name: p.name + "/" + c.name, p: p, c: c,
		echoCalls: new(atomic.Int32),
		requests:  make([]recordedRequest, 1),
		events:    []core.Event{core.ErrorEvent{Message: err.Error(), Err: err, Terminal: true}},
		runErr:    err,
		res: core.RunResult{StopReason: core.RunStopError, Error: err, Messages: core.Messages{
			core.UserMessage{},
			core.AssistantMessage{StopReason: core.StopReasonError, Content: core.Content{core.TextBlock{Text: kept}}},
		}},
	}
}

// setTerminal replaces the terminal error of a failedRun, in the event and in
// the message, and the error the run returns with it.
func setTerminal(r *caseRun, err error) {
	r.events = []core.Event{core.ErrorEvent{Message: err.Error(), Err: err, Terminal: true}}
	r.runErr, r.res.Error = err, err
}

// TS-30-23 to TS-30-26 (30-REQ-5.1 to 30-REQ-5.4) at the checks themselves: what
// a conforming provider leaves passes, and each way it can fail them is reported.
func TestFailedCaseChecks_TS30_23(t *testing.T) {
	setLast := func(r *caseRun, mutate func(*core.AssistantMessage)) {
		a := r.res.Messages[1].(core.AssistantMessage)
		mutate(&a)
		r.res.Messages[1] = a
	}
	common := []struct {
		name   string
		mutate func(r *caseRun)
		want   string
	}{
		{"the run ended normally", func(r *caseRun) { r.res.StopReason = core.RunStopEndTurn }, "ended"},
		{"the run has no error", func(r *caseRun) { r.runErr, r.res.Error = nil, nil }, "error"},
		{"the run's result has no error", func(r *caseRun) { r.res.Error = nil }, "error"},
		{"two requests", func(r *caseRun) { r.requests = make([]recordedRequest, 2) }, "recorded 2 requests"},
		{"no request", func(r *caseRun) { r.requests = nil }, "recorded 0 requests"},
		{"echo ran", func(r *caseRun) { r.echoCalls.Store(1) }, "echo ran"},
		{"a tool execution started", func(r *caseRun) { r.events = append(r.events, core.ToolExecutionStartEvent{Name: "echo"}) }, "ToolExecutionStartEvent"},
		{"no terminal error event", func(r *caseRun) { r.events = nil }, "no terminal ErrorEvent"},
		{"a non-terminal error event only", func(r *caseRun) { r.events = []core.Event{core.ErrorEvent{Message: "x"}} }, "no terminal ErrorEvent"},
	}
	specific := map[string][]struct {
		name   string
		mutate func(r *caseRun)
		want   string
	}{
		"cut_off": {
			{"the terminal error is not the sentinel", func(r *caseRun) {
				e := r.events[0].(core.ErrorEvent)
				r.events = []core.Event{core.ErrorEvent{Message: e.Message, Err: errors.New("other"), Terminal: true}}
			}, "terminal error"},
			{"the run's error is not the sentinel", func(r *caseRun) { r.runErr = errors.New("other") }, "run's error"},
			{"the failed message ends in a stop", func(r *caseRun) { setLast(r, func(a *core.AssistantMessage) { a.StopReason = core.StopReasonStop }) }, "ends"},
			{"the partial text is gone", func(r *caseRun) { setLast(r, func(a *core.AssistantMessage) { a.Content = nil }) }, "keep"},
			{"no assistant message", func(r *caseRun) { r.res.Messages = core.Messages{core.UserMessage{}} }, "no assistant message"},
		},
		"error_status": {
			{"the error has another code", func(r *caseRun) {
				setTerminal(r, &ollama.StatusError{Code: 500, Text: "ollama: HTTP 400: " + badRequest})
			}, "StatusError"},
			{"the error is not a status error", func(r *caseRun) { setTerminal(r, errors.New("ollama: HTTP 400: "+badRequest)) }, "StatusError"},
			{"the text lacks the prefix", func(r *caseRun) {
				setTerminal(r, &ollama.StatusError{Code: 400, Text: "ollama: status 400: " + badRequest})
			}, "start"},
			{"the text names another provider", func(r *caseRun) {
				setTerminal(r, &ollama.StatusError{Code: 400, Text: "anthropic: HTTP 400: " + badRequest})
			}, "start"},
			{"the error's own text lacks the vendor's message, the event's has it", func(r *caseRun) {
				err := &ollama.StatusError{Code: 400, Text: "ollama: HTTP 400: Bad Request"}
				r.events = []core.Event{core.ErrorEvent{Message: "ollama: HTTP 400: " + badRequest, Err: err, Terminal: true}}
				r.runErr, r.res.Error = err, err
			}, "contain"},
			{"the text lacks the vendor's message", func(r *caseRun) {
				setTerminal(r, &ollama.StatusError{Code: 400, Text: "ollama: HTTP 400: Bad Request"})
			}, "contain"},
		},
		"error_in_stream": {
			{"the vendor's message is gone", func(r *caseRun) { setTerminal(r, errors.New("ollama: stream error")) }, "contain"},
			{"the error's own text lacks the vendor's message, the event's has it", func(r *caseRun) {
				err := errors.New("ollama: stream error")
				r.events = []core.Event{core.ErrorEvent{Message: "ollama: " + upstreamFailure, Err: err, Terminal: true}}
				r.runErr, r.res.Error = err, err
			}, "contain"},
			{"the streamed text is gone", func(r *caseRun) { setLast(r, func(a *core.AssistantMessage) { a.Content = nil }) }, "keep"},
			{"the failed message ends in a stop", func(r *caseRun) { setLast(r, func(a *core.AssistantMessage) { a.StopReason = core.StopReasonStop }) }, "ends"},
		},
	}
	for _, c := range failingCases {
		rec := &recorder{}
		good := failedRun(c, rec)
		c.check(good)
		if rec.failed() {
			t.Errorf("%s: a run that conforms was reported: %v", c.name, rec.all())
		}
		variants := append(slices.Clone(common), specific[c.name]...)
		for _, v := range variants {
			rec := &recorder{}
			r := failedRun(c, rec)
			v.mutate(r)
			c.check(r)
			if !strings.Contains(strings.Join(rec.all(), "\n"), v.want) {
				t.Errorf("%s, %s: reported %q, want a failure mentioning %q", c.name, v.name, rec.all(), v.want)
			}
		}
	}
}

// The terminal error is the last one the run raised, not the first.
func TestTerminalErrorIsTheLast_TS30_23(t *testing.T) {
	r := &caseRun{events: []core.Event{
		core.ErrorEvent{Message: "first", Terminal: true},
		core.ErrorEvent{Message: "not terminal"},
		core.ErrorEvent{Message: "last", Terminal: true},
		core.ErrorEvent{Message: "also not terminal"},
	}}
	if ee, ok := r.terminalError(); !ok || ee.Message != "last" {
		t.Errorf("terminalError = %q, %v, want the last terminal error", ee.Message, ok)
	}
	if _, ok := (&caseRun{events: []core.Event{core.ErrorEvent{Message: "x"}}}).terminalError(); ok {
		t.Error("a run with no terminal error has one")
	}
}
