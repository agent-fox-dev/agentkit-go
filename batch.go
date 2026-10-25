package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// batchOutcome is what one batch produced. The three slices hold one entry per
// call, in call order.
type batchOutcome struct {
	// messages are the ToolResultMessages as After left them: what an agent
	// run records in its transcript.
	messages []core.ToolResultMessage
	// results are the calls' core.ToolResults, with Terminate set to each
	// call's final vote. After's in-place edits to a message do not reach
	// them.
	results []core.ToolResult
	// terminate is the OR of the final votes.
	terminate bool
}

// run runs one batch of tool calls. It is three phases with distinct
// concurrency rules, and each phase is where it is because the natural Go
// answer is wrong:
//
//  1. Prepare  — strictly SEQUENTIAL, on the calling goroutine. Events,
//     argument preparation, validation and the authorization
//     interceptor. A blocked call is finalized inline here, so the
//     policy observes a deterministic order even though the
//     handlers race.
//  2. Execute  — parallel. ONLY the handler body runs outside the lock. One
//     goroutine per call, joined by sync.WaitGroup — never
//     errgroup, which returns the first error and cancels the
//     siblings, when every call in a batch needs a result.
//  3. Finalize — serialized under ONE BATCH-SCOPED mutex with a func-scoped
//     deferred unlock, so a panicking interceptor or event
//     listener cannot leak it and deadlock the remaining tool
//     goroutines at the join.
//
// Results are written by SLOT INDEX into pre-sized slices, so their order is
// independent of completion order.
//
// emit receives every execution event of the batch, from the goroutines that
// run the calls, and holds no lock of the runner's while it does. A nil emit
// discards the events, and a panic in emit drops the event being delivered.
// assistant and turnCount are the guard's context for a call a model made; a
// runner's own Call and Batch pass nil and 0. run attaches no usage reporter: a
// context that carries one passes it through to the handlers, and the agent
// loop attaches its own.
//
// The batch's termination vote is true when any call that ran voted Terminate,
// or the Guard blocked a call and voted to terminate. The vote is read only
// after every call in the batch has finished, so a terminating call never cuts
// its siblings short.
func (r *Runner) run(ctx context.Context, emit func(core.Event), assistant *core.AssistantMessage, calls []core.ToolUseBlock, turnCount int) batchOutcome {
	emit = safeSink(emit)
	// report surfaces an error — a panicking interceptor — as an event. It
	// takes no lock of the runner's, so it is safe inside the batch-scoped
	// critical section.
	report := func(err error) { emit(core.ErrorEvent{Message: err.Error(), Err: err}) }
	byName := r.byName

	// started marks calls whose ToolExecutionStartEvent has been emitted, so
	// the abort path can open the calls the prepare loop never reached and
	// every call closes exactly once.
	started := make([]bool, len(calls))

	n := len(calls)
	msgs := make([]core.ToolResultMessage, n)
	results := make([]core.ToolResult, n)
	votes := make([]bool, n)
	// filled marks the slots that hold a result. A call's id cannot: a
	// provider may send an empty one.
	filled := make([]bool, n)
	thunks := make([]func(), 0, n)

	// env is what a wrapper's nested calls run with: the same
	// runner, sink and reporter as this batch.
	env := &nestedEnv{runner: r, emit: emit, assistant: assistant, turnCount: turnCount, report: report}

	// batchMu is BATCH-SCOPED: created here, acquired by nothing else.
	//
	// Finalization runs under one mutex, and no interceptor may run while an
	// agent lock is held; read literally, the two rules contradict each other.
	// The natural reading — reuse the agent mutex — IS the deadlock the second
	// rule exists to prevent. A mutex of the batch's own satisfies both.
	var batchMu sync.Mutex

	// ---- Phase 1: prepare, strictly sequential.
	// finalizeInline closes a call that never reaches a handler — unknown
	// tool, bad arguments, a block. It still emits the execution end event
	// and the result, so every call opens and closes exactly once on the
	// stream whichever way it ended, and a UI keyed on the start event never
	// waits for an end that will not come. res.Terminate is the call's vote.
	finalizeInline := func(i int, c core.ToolUseBlock, res core.ToolResult) {
		m := toolResultMessage(c, res)
		msgs[i], results[i], votes[i], filled[i] = m, res, res.Terminate, true
		emit(core.ToolExecutionEndEvent{ToolUseID: m.ToolUseID, Name: m.ToolName, IsError: m.IsError})
		emit(core.ToolResultEvent{Message: m})
	}

	for i, c := range calls {
		// Once the context is cancelled the prepare loop stops enqueuing
		// further calls. The calls already prepared are still covered by the
		// single decision below, and the ones never reached are closed there
		// as aborted.
		if ctx.Err() != nil {
			break
		}

		// No ToolCallStart/ToolCallEnd here. Those describe the MODEL emitting
		// a tool call and are the provider's to push as it streams; the batch
		// owns the EXECUTION triple (ToolExecutionStart/Update/End) and the
		// finalized ToolResultEvent. Emitting both pairs would duplicate every
		// call in any UI driven by the stream.
		//
		// The execution START is emitted HERE, in the sequential prepare
		// phase, not inside the thunk: the start events go out in call order,
		// and a call finalized inline (blocked, invalid) or aborted before its
		// handler ran must still have opened.
		emit(core.ToolExecutionStartEvent{ToolUseID: c.ID, Name: c.Name})
		started[i] = true

		tool, known := byName[c.Name]
		if !known {
			finalizeInline(i, c, core.ErrResult("unknown_tool", unknownToolMessage(c.Name, byName)))
			continue
		}

		// Argument preparation — the per-tool shim included — runs inside a
		// panic-recover boundary. Tool.PrepareArguments is user code; a panic
		// in it is an invalid-arguments result, not a crashed process.
		var (
			prepared core.PreparedArguments
			perr     error
		)
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					perr = fmt.Errorf("argument preparation for %q panicked: %v", c.Name, rec)
					report(fmt.Errorf("agentkit: panic in PrepareArguments for %q: %v", c.Name, rec))
				}
			}()
			prepared, perr = core.PrepareArguments(tool, c)
		}()
		if perr != nil {
			// The error text re-serializes the model's OWN key order, so the
			// message is self-correcting.
			finalizeInline(i, c, core.ErrResult("invalid_arguments", perr.Error()))
			continue
		}

		if r.guard != nil {
			dec := callBefore(ctx, report, r.guard, core.BeforeToolCallContext{
				ToolName:  c.Name,
				ToolUseID: c.ID,
				Tool:      tool,
				Arguments: prepared.Args,
				RawInput:  c.Input,
				Assistant: assistant,
				Batch:     calls,
				Index:     i,
				TurnCount: turnCount,
			})
			if dec.Block {
				reason := dec.Reason
				if reason == "" {
					reason = "blocked by policy"
				}
				// A blocked call casts no vote of its own: its tool never ran.
				// The Guard may vote instead, with Terminate beside Block —
				// what lets a permission denial end the run rather than loop
				// the model into retrying, as it does for a nested call.
				blocked := core.ErrResult(core.BlockErrorCode, reason)
				blocked.Terminate = dec.Terminate
				finalizeInline(i, c, blocked)
				continue
			}
			if dec.Arguments != nil {
				// The interceptor may widen as well as narrow.
				// Arguments it returns that JSON cannot carry fail this call
				// alone, as any other invalid arguments do.
				next, err := prepared.TryWithArgs(dec.Arguments)
				if err != nil {
					finalizeInline(i, c, core.ErrResult("invalid_arguments",
						"BeforeToolCall returned arguments that are not JSON: "+err.Error()))
					continue
				}
				prepared = next
			}
		}

		thunks = append(thunks, func() {
			start := time.Now()

			// A tool that reaches other tools runs with a NestedCaller bound
			// to this call on its context.
			hctx, nested := env.withCaller(ctx, c, tool)
			out := invokeHandler(hctx, report, tool, prepared)
			// An interceptor that voted to terminate during one of this
			// wrapper's nested calls ends the run, whatever the wrapper
			// returned. AfterToolCall below may still override.
			if nested != nil && nested.terminated.Load() {
				out.Terminate = true
			}
			// A nested call's own terminate vote was dropped; the wrapper's
			// result records that it happened.
			if nested != nil && nested.voteIgnored.Load() {
				out.Detail = annotate(out.Detail, "nested terminate vote ignored")
			}
			// ---- Phase 3, per call: finalize under the batch mutex.
			// A func-scoped critical section with a DEFERRED unlock: a
			// panicking AfterToolCall or event listener must not leak the
			// mutex and hang every peer at the join.
			func() {
				batchMu.Lock()
				defer batchMu.Unlock()

				msg := toolResultMessage(c, out)
				if r.after != nil {
					dec := callAfter(ctx, report, r.after, core.AfterToolCallContext{
						ToolName:   c.Name,
						ToolUseID:  c.ID,
						Arguments:  prepared.Args,
						Result:     &msg,
						Elapsed:    time.Since(start),
						ToolResult: out,
					})
					// Terminate is *bool: nil means "no opinion", so an
					// interceptor that does not care cannot accidentally vote
					// against a tool that does.
					if dec.Terminate != nil {
						out.Terminate = *dec.Terminate
					}
				}
				msgs[i], results[i], votes[i], filled[i] = msg, out, out.Terminate, true
				emit(core.ToolExecutionEndEvent{
					ToolUseID: c.ID, Name: c.Name, IsError: msg.IsError,
					ElapsedMS: time.Since(start).Milliseconds(),
				})
				emit(core.ToolResultEvent{Message: msg})
			}()
		})
	}

	// ---- Phase 2: the abort decision, made ONCE, here, on the calling
	// goroutine, after prepare and BEFORE any handler starts.
	//
	// It must not be re-checked inside each tool goroutine. Per-goroutine
	// ctx.Err() checks — the obvious Go idiom — let the scheduler SPLIT the
	// batch: an abort landing just after the batch starts skips whichever
	// calls had not yet been scheduled and runs the rest. That is
	// nondeterministic, unreproducible in tests, and shows up in production
	// as phantom side effects after the user pressed Ctrl-C.
	//
	// The handlers still RECEIVE ctx, so a running subprocess is still
	// killed. What must not be re-decided is *whether a handler runs*.
	if ctx.Err() != nil {
		for i, c := range calls {
			if filled[i] {
				continue // already finalized in prepare (blocked/invalid)
			}
			// A call the prepare loop never reached still opens, so it can
			// close.
			if !started[i] {
				emit(core.ToolExecutionStartEvent{ToolUseID: c.ID, Name: c.Name})
				started[i] = true
			}
			finalizeInline(i, c, abortedResult())
		}
		// No handler ran, so no tool voted: an aborted batch does not end
		// the run on a tool's say-so. A Guard's own vote, cast in prepare,
		// stands.
		return batchOutcome{messages: msgs, results: results, terminate: slices.Contains(votes, true)}
	}

	sequential := len(thunks) <= 1
	if !sequential {
		// A single Sequential tool anywhere in the batch demotes the WHOLE
		// batch. Tools with process-wide or workspace-wide
		// side effects ship as Sequential.
		for _, c := range calls {
			if t, ok := byName[c.Name]; ok && t.ExecutionMode == core.Sequential {
				sequential = true
				break
			}
		}
	}

	if sequential {
		for _, th := range thunks {
			th()
		}
	} else {
		var wg sync.WaitGroup
		for _, th := range thunks {
			wg.Add(1)
			go func(f func()) {
				defer wg.Done()
				f()
			}(th)
		}
		wg.Wait()
	}

	// Every call in the batch produced a result — a handler error, a panic, an
	// interceptor block, a validation failure and an abort all become a result
	// with IsError set. A batch returning fewer results than calls leaves
	// dangling tool_use blocks and makes the next request invalid, so this is
	// asserted rather than assumed.
	for i, c := range calls {
		if !filled[i] {
			res := core.ErrResult("no_result", "internal: the tool batch produced no result for this call")
			msgs[i], results[i] = toolResultMessage(c, res), res
		}
	}
	return batchOutcome{messages: msgs, results: results, terminate: slices.Contains(votes, true)}
}

// invokeHandler calls the tool, converting every failure mode into a result.
// No tool outcome is ever propagated to the caller as a Go error.
func invokeHandler(ctx context.Context, report func(error), t core.Tool, p core.PreparedArguments) (res core.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			// A handler panic becomes a tool result and the loop continues. It
			// must never crash the agent process, and it is reported, so it is
			// not mistaken for an ordinary failure.
			res = core.ErrResult("panic", fmt.Sprintf("tool %q panicked: %v", t.Name, r))
			report(fmt.Errorf("agentkit: panic in tool %q: %v", t.Name, r))
		}
	}()

	if t.Execute != nil {
		return t.Execute(ctx, p.Raw)
	}
	out, err := t.Handler(ctx, p.Raw)
	if err != nil {
		return core.ErrResult("handler_error", err.Error())
	}
	var data map[string]any
	if len(out) > 0 {
		if err := json.Unmarshal(out, &data); err != nil {
			// A handler may legitimately return a non-object; carry it
			// through rather than failing the call.
			return core.ToolResult{OK: true, Data: map[string]any{"result": json.RawMessage(out)}}
		}
	}
	return core.OKResult(data)
}

// callBefore invokes the authorization interceptor through a recovering
// wrapper, with NO agent lock held. A panicking interceptor fails CLOSED — it
// blocks the call — because a security boundary that opens on panic is not a
// boundary.
func callBefore(ctx context.Context, report func(error), f core.BeforeToolCall, in core.BeforeToolCallContext) (dec core.BeforeToolCallDecision) {
	defer func() {
		if r := recover(); r != nil {
			dec = core.BeforeToolCallDecision{
				Block:  true,
				Reason: fmt.Sprintf("interceptor panicked: %v", r),
			}
			report(fmt.Errorf("agentkit: panic in BeforeToolCall for %q: %v", in.ToolName, r))
		}
	}()
	return f(ctx, in)
}

// callAfter invokes the after-interceptor through a recovering wrapper. A
// panic here yields "no opinion" rather than a vote: unlike BeforeToolCall
// this is not a security boundary, and inventing a termination vote from a
// crash would end the run for the wrong reason.
//
// It reports through the batch's lock-free reporter: it runs inside the
// batch-scoped critical section, and no runner or agent lock is taken there.
func callAfter(ctx context.Context, report func(error), f core.AfterToolCall, in core.AfterToolCallContext) (dec core.AfterToolCallDecision) {
	defer func() {
		if r := recover(); r != nil {
			dec = core.AfterToolCallDecision{}
			report(fmt.Errorf("agentkit: panic in AfterToolCall for %q: %v", in.ToolName, r))
		}
	}()
	return f(ctx, in)
}

// toolResultMessage renders a result for the transcript. The text block is
// the tool's own rendering when it supplied one (core.ToolResult.Text), else
// the JSON envelope; either way the tool's extra blocks (images)
// follow it.
func toolResultMessage(c core.ToolUseBlock, r core.ToolResult) core.ToolResultMessage {
	content := core.Content{core.TextBlock{Text: r.LLMText()}}
	content = append(content, r.Blocks...)
	msg := core.ToolResultMessage{
		ToolUseID: c.ID,
		ToolName:  c.Name,
		Content:   content,
		IsError:   !r.OK,
		Timestamp: time.Now(),
	}
	if !r.Metadata.IsEmpty() {
		msg.Metadata = r.Metadata.Clone()
	}
	return msg
}

// abortedResult is the fixed shape of a call that did not run because its
// batch, or the nested group it belonged to, was aborted. Such a call still
// gets its events and a result, so the transcript stays well-formed and
// resumable.
func abortedResult() core.ToolResult { return core.ErrResult("aborted", "Operation aborted") }

// maxListedTools bounds the tool names an unknown-tool result lists, so a run
// with hundreds of tools does not answer one wrong call with a page of names.
const maxListedTools = 50

// unknownToolMessage tells the model that the tool it called is not there and
// which ones are, sorted so the text does not depend on registration order.
func unknownToolMessage(name string, available map[string]core.Tool) string {
	msg := fmt.Sprintf("no tool named %q is available in this run", name)
	if len(available) == 0 {
		return msg + "; no tools are available"
	}
	names := make([]string, 0, len(available))
	for n := range available {
		names = append(names, n)
	}
	sort.Strings(names)
	more := ""
	if len(names) > maxListedTools {
		more = fmt.Sprintf(" (and %d more)", len(names)-maxListedTools)
		names = names[:maxListedTools]
	}
	return msg + "; available tools: " + strings.Join(names, ", ") + more
}
