package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider"
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
	// The timeout's cause is how the run tells its own deadline from the
	// caller's cancellation.
	cancel := context.CancelFunc(func() {})
	if a.cfg.Timeout > 0 {
		ctx, cancel = context.WithTimeoutCause(ctx, a.cfg.Timeout, errRunTimeout)
	}
	go func() {
		res, err := a.runLoop(ctx, s, m)
		// The slot is free before the stream ends: a caller whose Run has
		// returned may start the next one at once.
		cancel()
		a.releaseSlot()
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

	// finish is the ONE exit path. It is a closure so the panic recovery
	// below reaches the same tail as a normal exit — a run that panicked
	// still owes its AgentDoneEvent, or a consumer cannot tell it from one
	// still running.
	finish := func() {
		res = a.endRun(s, newMessages, runReason, runErr, turnCount)
		runErr = res.Error
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
		view, elided, reason, err := a.beforeRequest()
		if err != nil {
			runReason, runErr = reason, err
			break
		}

		s.Push(core.TurnStartEvent{TurnIndex: turnCount})
		assistant := a.callModel(ctx, s, view, report)
		record(assistant)
		a.noteElided(elided)
		a.addUsage(a.priced(assistant.Usage))

		// Only Error and Aborted short-circuit, and they do so BEFORE tool
		// extraction. Every other reason is treated identically for control
		// flow.
		if assistant.StopReason.ShortCircuits() {
			turnCount++
			s.Push(core.TurnEndEvent{TurnIndex: turnCount - 1, Message: assistant, ToolResults: []core.ToolResultMessage{}, Usage: assistant.Usage})
			if assistant.StopReason == core.StopReasonAborted {
				runReason, runErr = a.contextStop(ctx)
			} else {
				runReason = core.RunStopError
				runErr = errors.New(assistant.ErrorMessage)
			}
			break
		}

		// The continuation predicate: PRESENCE of tool_use blocks, never
		// stop_reason.
		toolCalls := core.ExtractToolUse(&assistant)

		results, terminate := a.runTools(ctx, s, &assistant, toolCalls, turnCount)
		for _, r := range results {
			record(r)
			if r.Usage != nil {
				a.addUsage(*r.Usage)
			}
		}

		turnCount++
		s.Push(core.TurnEndEvent{TurnIndex: turnCount - 1, Message: assistant, ToolResults: results, Usage: assistant.Usage})

		if stop, reason, err := a.afterTurn(ctx, terminate, len(toolCalls) > 0, turnCount); stop {
			runReason, runErr = reason, err
			break
		}
	}

	finish()
	return res, runErr
}

// runTools answers one turn's tool calls: by running them, or, when the
// response was truncated, with the fixed notice. The results are never nil:
// a no-tool turn reports [].
func (a *Agent) runTools(ctx context.Context, s *core.EventStream, assistant *core.AssistantMessage, calls []core.ToolUseBlock, turn int) ([]core.ToolResultMessage, bool) {
	if len(calls) == 0 {
		return []core.ToolResultMessage{}, false
	}
	if assistant.StopReason == core.StopReasonLength {
		// Reading stop_reason here is not the continuation predicate, which
		// already ran; this is a different decision.
		//
		// Execute NONE of them. Streamed arguments are finalized by a
		// best-effort salvage parser, so a truncated {"path":"/et becomes a
		// syntactically valid object that passes schema validation — and a
		// truncated edit whose new_string was cut off applies cleanly and
		// silently corrupts the file. Only the stop reason can catch this.
		return synthesizeTruncated(s, calls), false
	}
	return a.executeBatch(ctx, s, assistant, calls, turn)
}

// endRun builds the run's result and pushes the terminal events. A run that
// ended because the model refused is not a clean end_turn: only the account
// of how the run ended changes, since a refusal that carries tool calls
// carries on like any other turn.
func (a *Agent) endRun(s *core.EventStream, msgs core.Messages, reason core.RunStopReason, err error, turns int) core.RunResult {
	if reason == core.RunStopEndTurn && err == nil {
		if am := lastAssistant(msgs); am != nil && am.StopReason == core.StopReasonRefusal {
			reason, err = core.RunStopRefusal, core.ErrRefusal
			if am.StopDetail != "" {
				err = fmt.Errorf("%w: %s", core.ErrRefusal, am.StopDetail)
			}
		}
	}
	res := core.RunResult{Messages: msgs, StopReason: reason, Usage: a.runUsageSnapshot(), TurnCount: turns, Error: err}
	if am := lastAssistant(msgs); am != nil {
		res.LastReason = am.StopReason
	}
	// Result.Usage is this run's; the event's own Usage is the lifetime
	// aggregate.
	s.Push(core.AgentDoneEvent{Result: res, Usage: a.Usage()})
	return res
}

// beforeRequest decides whether the next request is sent, and builds its
// view. The cost bound is checked BEFORE the request, so a run never spends
// past it on a call it already knew it could not afford. The view is the
// transcript, with old tool results elided when it is large, and refused
// outright when even that cannot fit: sending a request the model cannot
// hold only buys an HTTP 400.
func (a *Agent) beforeRequest() (core.Messages, int64, core.RunStopReason, error) {
	if limit := a.cfg.MaxCostUSD; limit > 0 {
		if spent := a.runUsageSnapshot().CostUSD; spent >= limit {
			return nil, 0, core.RunStopBudgetExceeded,
				fmt.Errorf("%w: the run has cost $%.4f of its $%.4f budget", core.ErrBudgetExceeded, spent, limit)
		}
	}
	view, elided, err := a.outboundView(a.Messages())
	if err != nil {
		return nil, 0, core.RunStopError, err
	}
	return view, elided, "", nil
}

// afterTurn decides whether the run ends at this turn boundary, and why.
func (a *Agent) afterTurn(ctx context.Context, terminate, calledTools bool, turns int) (bool, core.RunStopReason, error) {
	if terminate {
		return true, core.RunStopToolTerminate, nil
	}
	// A cancellation that landed during the tool batch ends the run HERE,
	// with the results in the transcript. Going around again would issue a
	// request on a dead context.
	if ctx.Err() != nil {
		reason, err := a.contextStop(ctx)
		return true, reason, err
	}
	if !calledTools {
		return true, core.RunStopEndTurn, nil
	}
	// The turn bound is checked only when the run would go on, and after the
	// turn's results are in the transcript, so it never leaves a tool_use
	// unanswered.
	if a.cfg.MaxTurns > 0 && turns >= a.cfg.MaxTurns {
		return true, core.RunStopMaxTurns, fmt.Errorf("%w: %d turns", core.ErrMaxTurns, turns)
	}
	return false, "", nil
}

// record appends msgs to the transcript and to the run's own list. It is the
// only way a message enters a run, so the two cannot drift apart.
func (a *Agent) record(run *core.Messages, msgs ...core.Message) {
	a.mu.Lock()
	a.transcript = append(a.transcript, msgs...)
	a.mu.Unlock()
	*run = append(*run, msgs...)
}

// charsPerToken is the estimate's exchange rate where no usage report
// anchors it.
const charsPerToken = 4

// elisionNotice replaces a pruned tool result's content in the request.
const elisionNotice = "[result of %s (%d bytes) elided; call again if needed]"

// outboundView is the message list a request sends: msgs, pruned when
// Config.Prune says the estimate is past its threshold, and refused with an
// error naming the model and its window when the estimate is still larger
// than the window. msgs is never modified; the transcript keeps every result
// whole. elided is the estimate the pruning took off, which the response's
// usage will not count.
func (a *Agent) outboundView(msgs core.Messages) (view core.Messages, elided int64, err error) {
	window := int64(a.model.ContextWindow)
	tokens := a.estimateTokens(msgs)
	if p := a.cfg.Prune; p.Threshold > 0 && window > 0 && float64(tokens)/float64(window) >= p.Threshold {
		msgs, elided = pruneToolResults(msgs, p.KeepTurns)
		tokens -= elided
	}
	if window > 0 && tokens > window {
		return nil, 0, fmt.Errorf("agentkit: the request to model %q is about %d tokens, more than its %d-token context window",
			a.model.ID, tokens, window)
	}
	return msgs, elided, nil
}

// estimateTokens is the anchored estimate of the request msgs would make
// UNPRUNED: the context the latest assistant message's usage reports, plus
// what pruning took off the request that produced it (its usage counted the
// notices, not the results), plus charsPerToken for every message after it.
// With no usage to anchor on, the system prompt, the tools, the prefix and
// the messages are estimated whole. Deciding on the unpruned size is what
// keeps pruning on once it has started: a pruned request's own usage is
// below the threshold.
func (a *Agent) estimateTokens(msgs core.Messages) int64 {
	for i := len(msgs) - 1; i >= 0; i-- {
		if am, ok := msgs[i].(core.AssistantMessage); ok && am.Usage.ContextTokens() > 0 {
			return am.Usage.ContextTokens() + a.elidedAt(i) + charTokens(msgs[i+1:])
		}
	}
	return a.fixedTokens + charTokens(a.cfg.Prefix) + charTokens(msgs)
}

// charTokens estimates msgs at charsPerToken of their JSON form.
func charTokens(msgs []core.Message) int64 {
	var n int64
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			continue
		}
		n += int64(len(b))
	}
	return n / charsPerToken
}

// pruneToolResults returns msgs with the content of every tool result older
// than the last keep turns replaced by the elision notice. A turn is an
// assistant message and the results that answer it. The results keep their
// ToolUseID and name, so they still pair with their calls, and only the copy
// changes. saved is what the elisions take off the estimate.
func pruneToolResults(msgs core.Messages, keep int) (out core.Messages, saved int64) {
	turns := 0
	for _, m := range msgs {
		if _, ok := m.(core.AssistantMessage); ok {
			turns++
		}
	}
	cutoff := turns - keep // results of turns before this index are elided
	out = make(core.Messages, len(msgs))
	copy(out, msgs)
	turn := -1
	var savedChars int64
	for i, m := range msgs {
		switch v := m.(type) {
		case core.AssistantMessage:
			turn++
		case core.ToolResultMessage:
			if turn < 0 || turn >= cutoff {
				continue
			}
			size := len(v.Content.Text())
			notice := fmt.Sprintf(elisionNotice, v.ToolName, size)
			v.Content = core.Content{core.TextBlock{Text: notice}}
			out[i] = v
			savedChars += int64(size - len(notice))
		}
	}
	return out, savedChars / charsPerToken
}

// noteElided records, against the assistant message just recorded, what
// pruning took off the request it answered.
func (a *Agent) noteElided(tokens int64) {
	if tokens <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.elided == nil {
		a.elided = map[int]int64{}
	}
	a.elided[len(a.transcript)-1] = tokens
}

func (a *Agent) elidedAt(i int) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.elided[i]
}

// errRunTimeout is the cause of a run context that outlived Config.Timeout.
var errRunTimeout = errors.New("agentkit: run timeout")

// contextStop says why a run stopped on its context: its own Timeout, or a
// cancellation from outside — the caller's cancel or deadline, which the
// error wraps beside ErrAborted. With the context still live (a provider
// that ended a stream as aborted on its own), it is ErrAborted alone.
func (a *Agent) contextStop(ctx context.Context) (core.RunStopReason, error) {
	if context.Cause(ctx) == errRunTimeout {
		return core.RunStopTimeout, fmt.Errorf("agentkit: the run exceeded its %s timeout: %w", a.cfg.Timeout, context.DeadlineExceeded)
	}
	if err := ctx.Err(); err != nil {
		return core.RunStopAborted, fmt.Errorf("%w: %w", core.ErrAborted, err)
	}
	return core.RunStopAborted, core.ErrAborted
}

// priced returns u with its cost, priced at the model's catalog row when the
// provider did not price it (a test double, or a custom provider).
func (a *Agent) priced(u core.Usage) core.Usage {
	if !u.Has(core.UsageCostUSD) {
		u.SetCost(provider.ComputeCost(&a.model, u))
	}
	return u
}

// errorMessage is the terminal assistant message of a turn that failed
// without the provider producing one.
func (a *Agent) errorMessage(err error) core.AssistantMessage {
	return core.AssistantMessage{
		StopReason:   core.StopReasonError,
		ErrorMessage: err.Error(),
		Timestamp:    time.Now(),
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
		Prefix:    a.cfg.Prefix,
		Messages:  view,
		Tools:     core.ToolWires(a.tools),
		MaxTokens: &maxTokens,
		Effort:    a.cfg.Effort,
	}
	// The assembled prompt, not the raw field: per-tool guidelines are only
	// visible to the model if something assembles them.
	if sys := prompt.Build(a.cfg.System, a.tools); sys != "" {
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
