package agentkit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
)

// maxTokensToolText is REQ-LOOP-10's fixed result text, pinned byte-for-byte
// because it is model-visible. The line break inside it is a literal newline
// (ruling P-43): the PRD renders it across two lines inside a fenced block,
// and "inherit whatever the implementer typed" is not a specification for text
// the model reads.
const maxTokensToolText = "Tool call %q was not executed: the response hit the output token limit,\n" +
	"so its arguments may be truncated. Re-issue the tool call with complete arguments."

// Run sends prompt and runs the loop to a stop condition. The error is the
// result's own Error: a run that stopped on a limit, a refusal or a failure
// returns both the result and why.
func (a *Agent) Run(ctx context.Context, prompt string) (core.RunResult, error) {
	s, err := a.Stream(ctx, prompt)
	if err != nil {
		return core.RunResult{}, err
	}
	return s.RunResult()
}

// Stream runs the loop and returns its event stream. The producer never
// blocks on the consumer, so abandoning the stream is safe and the result
// stays available through RunResult.
//
// It returns an error rather than only a pre-closed stream for the one case a
// caller most often gets wrong — a second run while one is in flight —
// because a silent ErrBusy inside a stream nobody reads is indistinguishable
// from a run that produced nothing.
func (a *Agent) Stream(ctx context.Context, prompt string) (*core.EventStream, error) {
	if err := a.claimSlot(); err != nil {
		return nil, err
	}
	m := core.UserMessage{Content: core.Content{core.TextBlock{Text: prompt}}, Timestamp: time.Now()}
	s := core.NewEventStream(core.StreamOptions{})
	go func() {
		defer a.releaseSlot()
		res, err := a.runLoop(ctx, s, m)
		s.End(core.StreamResult{Message: lastAssistant(res.Messages), Result: &res, Err: err})
	}()
	return s, nil
}

func lastAssistant(ms core.Messages) *core.AssistantMessage {
	for i := len(ms) - 1; i >= 0; i-- {
		if am, ok := ms[i].(core.AssistantMessage); ok {
			return &am
		}
	}
	return nil
}

// runLoop is the driver. Read it top to bottom; the order of the steps is
// the specification.
func (a *Agent) runLoop(ctx context.Context, s *core.EventStream, initial core.UserMessage) (res core.RunResult, runErr error) {
	var (
		newMessages core.Messages
		turnCount   int
		runReason   = core.RunStopEndTurn
	)
	report := streamReporter(s)

	// finish is the ONE exit path: it builds the RunResult and pushes the
	// terminal event. It is a closure so the panic recovery below reaches the
	// same tail as a normal exit — a run that panicked still owes its
	// AgentDoneEvent, or a consumer cannot tell it from one still running.
	finish := func() {
		// A run that ended because the model refused is not a clean
		// end_turn. Only the account of how the run ended changes: a refusal
		// that carries tool calls carries on like any other turn.
		if runReason == core.RunStopEndTurn && runErr == nil {
			if am := lastAssistant(newMessages); am != nil && am.StopReason == core.StopReasonRefusal {
				runReason = core.RunStopRefusal
				runErr = core.ErrRefusal
				if am.StopDetail != "" {
					runErr = fmt.Errorf("%w: %s", core.ErrRefusal, am.StopDetail)
				}
			}
		}
		res = core.RunResult{
			Messages:   newMessages,
			StopReason: runReason,
			Usage:      a.runUsageSnapshot(),
			TurnCount:  turnCount,
			Error:      runErr,
		}
		if am := lastAssistant(newMessages); am != nil {
			res.LastReason = am.StopReason
		}
		// Result.Usage is this run's; the event's own Usage is the lifetime
		// aggregate.
		s.Push(core.AgentDoneEvent{Result: res, Usage: a.Usage()})
		if runErr != nil {
			s.Push(core.ErrorEvent{Message: runErr.Error(), Err: runErr, Terminal: true})
		}
	}

	// A panic in code the loop calls must never crash the process. Tool
	// handlers and interceptors are each wrapped at their own call site; this
	// is the backstop for the loop's own bugs. It leaves a terminal marker,
	// so a turn that started always has a terminal message and the
	// transcript stays sendable.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		runErr = fmt.Errorf("agentkit: panic in run: %v", r)
		runReason = core.RunStopError
		needMarker := true
		if n := len(newMessages); n > 0 {
			if am, ok := newMessages[n-1].(core.AssistantMessage); ok && am.StopReason.ShortCircuits() {
				needMarker = false
			}
		}
		if needMarker {
			marker := a.errorMessage(runErr)
			a.record(&newMessages, marker)
		}
		finish()
	}()

	record := func(msgs ...core.Message) { a.record(&newMessages, msgs...) }
	record(initial)

	s.Push(core.AgentStartEvent{Provider: a.model.Provider, API: a.model.API, Model: a.model.ID})

	for {
		view := a.Messages()

		s.Push(core.TurnStartEvent{TurnIndex: turnCount})
		assistant := a.callModel(ctx, s, view, report)
		record(assistant)
		a.addUsage(assistant.Usage)

		// Only Error and Aborted short-circuit, and they do so BEFORE tool
		// extraction. Every other reason is treated identically for control
		// flow.
		if assistant.StopReason.ShortCircuits() {
			turnCount++
			s.Push(core.TurnEndEvent{TurnIndex: turnCount - 1, Message: assistant, ToolResults: []core.ToolResultMessage{}, Usage: assistant.Usage})
			if assistant.StopReason == core.StopReasonAborted {
				runReason, runErr = core.RunStopAborted, abortError(ctx)
			} else {
				runReason = core.RunStopError
				runErr = errors.New(assistant.ErrorMessage)
			}
			break
		}

		// The continuation predicate: PRESENCE of tool_use blocks, never
		// stop_reason.
		toolCalls := core.ExtractToolUse(&assistant)

		var (
			results   = []core.ToolResultMessage{}
			terminate bool
		)
		if len(toolCalls) > 0 {
			if assistant.StopReason == core.StopReasonLength {
				// Reading stop_reason here is not the continuation
				// predicate, which already ran above; this is a different
				// decision.
				//
				// Execute NONE of them. Streamed arguments are finalized by a
				// best-effort salvage parser, so a truncated {"path":"/et
				// becomes a syntactically valid object that passes schema
				// validation — and a truncated edit whose new_string was cut
				// off applies cleanly and silently corrupts the file. Only
				// the stop reason can catch this.
				results = synthesizeTruncated(s, toolCalls)
			} else {
				results, terminate = a.executeBatch(ctx, s, &assistant, toolCalls, turnCount)
			}
			for _, r := range results {
				record(r)
				if r.Usage != nil {
					a.addUsage(*r.Usage)
				}
			}
		}

		turnCount++
		s.Push(core.TurnEndEvent{TurnIndex: turnCount - 1, Message: assistant, ToolResults: results, Usage: assistant.Usage})

		if terminate {
			runReason = core.RunStopToolTerminate
			break
		}

		// A cancellation that landed during the tool batch ends the run
		// HERE, at the turn boundary, with the results in the transcript.
		// Going around again would issue a request on a dead context.
		if ctx.Err() != nil {
			runReason, runErr = core.RunStopAborted, abortError(ctx)
			break
		}

		if len(toolCalls) == 0 {
			break
		}
	}

	finish()
	return res, runErr
}

// record appends msgs to the transcript and to the run's own list. It is the
// only way a message enters a run, so the two cannot drift apart.
func (a *Agent) record(run *core.Messages, msgs ...core.Message) {
	a.mu.Lock()
	a.transcript = append(a.transcript, msgs...)
	a.mu.Unlock()
	*run = append(*run, msgs...)
}

// abortError says why a run stopped on its context.
func abortError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return core.ErrAborted
}

// errorMessage is the terminal assistant message of a turn that failed
// without the provider producing one.
func (a *Agent) errorMessage(err error) core.AssistantMessage {
	return core.AssistantMessage{
		StopReason:   core.StopReasonError,
		ErrorMessage: err.Error(),
		Timestamp:    time.Now(),
		Provider:     a.model.Provider,
		API:          a.model.API,
		Model:        a.model.ID,
	}
}

// streamReporter reports an error the run survives — a panic in a handler or
// an interceptor — as an event on the run's stream.
func streamReporter(s *core.EventStream) func(error) {
	return func(err error) { s.Push(core.ErrorEvent{Message: err.Error(), Err: err}) }
}

// callModel issues one provider request and returns the assistant message.
// It never returns an error: failures are encoded in the message, which is
// what lets a provider emit half a message and then fail without the partial
// content being lost.
func (a *Agent) callModel(ctx context.Context, out *core.EventStream, view core.Messages, report func(error)) core.AssistantMessage {
	maxTokens := a.cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = core.DefaultMaxTokens
	}
	req := core.Request{
		Messages:  view,
		Tools:     core.ToolWires(a.tools),
		MaxTokens: &maxTokens,
		Effort:    a.cfg.Effort,
	}
	// The assembled prompt, not the raw field: per-tool guidelines are only
	// visible to the model if something assembles them.
	if sys := prompt.Build(prompt.Input{Custom: a.cfg.System, Tools: a.tools}); sys != "" {
		req.System = []core.ContentBlock{core.TextBlock{Text: sys}}
	}

	// A provider is third-party code too. A panic in it becomes the error
	// message this function returns for any other provider failure.
	var ps *core.EventStream
	func() {
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("agentkit: panic in provider: %v", r)
				report(err)
				msg := a.errorMessage(err)
				ps = core.ErrorStream(&msg, err)
			}
		}()
		m := a.model
		ps = a.client.Stream(ctx, &m, req, core.ProviderStreamOptions{})
	}()
	// Forward provider events onto the agent stream. The provider stream is
	// unbounded and non-blocking, so this cannot stall the model call.
	for e := range ps.Events() {
		out.Push(e)
	}
	msg := ps.Result()
	if msg == nil {
		// A provider that ended without a message is a provider bug; produce
		// the terminal marker rather than a nil deref.
		err := ps.Err()
		if err == nil {
			err = errors.New("provider ended the stream with no assistant message")
		}
		return a.errorMessage(err)
	}
	return *msg
}

// synthesizeTruncated answers each call of a truncated response without
// running it: every call still emits the normal event sequence and produces
// a result, so the transcript and the UI stay well-formed and the model can
// re-issue.
func synthesizeTruncated(s *core.EventStream, calls []core.ToolUseBlock) []core.ToolResultMessage {
	out := make([]core.ToolResultMessage, 0, len(calls))
	for _, c := range calls {
		s.Push(core.ToolExecutionStartEvent{ToolUseID: c.ID, Name: c.Name})
		m := core.ToolResultMessage{
			ToolUseID: c.ID,
			ToolName:  c.Name,
			Content:   core.Content{core.TextBlock{Text: fmt.Sprintf(maxTokensToolText, c.Name)}},
			IsError:   true,
			Timestamp: time.Now(),
		}
		s.Push(core.ToolExecutionEndEvent{ToolUseID: c.ID, Name: c.Name, IsError: true})
		s.Push(core.ToolResultEvent{Message: m})
		out = append(out, m)
	}
	return out
}

func (a *Agent) addUsage(u core.Usage) {
	a.mu.Lock()
	a.usage = a.usage.Add(u)
	a.runUsage = a.runUsage.Add(u)
	a.mu.Unlock()
}

// runUsageSnapshot is the current run's usage, for RunResult. Agent.Usage is
// the lifetime total.
func (a *Agent) runUsageSnapshot() core.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.runUsage
}
