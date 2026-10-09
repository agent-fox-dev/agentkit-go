package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// executeBatch runs one turn's tool calls (REQ-LOOP-05). It is three phases
// with distinct concurrency rules, and each phase is where it is because the
// natural Go answer is wrong:
//
//  1. Prepare  — strictly SEQUENTIAL, on the loop goroutine. Events,
//     argument preparation, validation and the authorization
//     interceptor. A blocked call is finalized inline here, so the
//     policy observes a deterministic order even though the
//     handlers race (REQ-SEC-03.4).
//  2. Execute  — parallel. ONLY the handler body runs outside the lock. One
//     goroutine per call, joined by sync.WaitGroup — never
//     errgroup, which returns the first error and cancels the
//     siblings, when every call in a batch needs a result
//     (REQ-GO-04).
//  3. Finalize — serialized under ONE BATCH-SCOPED mutex with a func-scoped
//     deferred unlock, so a panicking interceptor or event
//     listener cannot leak it and deadlock the remaining tool
//     goroutines at the join.
//
// Results are written by SLOT INDEX into a pre-sized slice and appended in
// slot order after the join, so transcript order is independent of completion
// order (REQ-LOOP-05).
//
// Returns the results and the REQ-TOOL-13 termination vote.
func (a *Agent) executeBatch(ctx context.Context, s *core.EventStream, assistant *core.AssistantMessage, calls []core.ToolUseBlock, turnCount int) ([]core.ToolResultMessage, bool) {
	// A tool that spends on model calls of its own — a delegated subagent —
	// reports it through core.ReportUsage the moment it is spent, so the
	// agent's usage (and a budget computed from it, by the next delegation
	// in the same parallel batch) sees it at once rather than when the batch
	// finalizes.
	ctx = core.WithUsageReporter(ctx, a.addUsage)
	a.mu.Lock()
	cfg := a.cfg
	tools := cfg.ToolPolicy.Resolve(a.tools)
	a.mu.Unlock()

	// report surfaces an error from INSIDE the batch-scoped critical section
	// without touching a.mu. The hooks are taken from the cfg copy above, so
	// the documented lock order (a.mu is never acquired under batchMu) is a
	// fact rather than a comment: fireError re-reads the hooks under a.mu,
	// and calling it from the finalize block took the agent lock inside the
	// batch lock on every tool result.
	hooks := cfg.Hooks
	report := func(err error) {
		safely(nil, "OnError", func() {
			if hooks.OnError != nil {
				hooks.OnError(err)
			}
		})
	}

	// started marks calls whose ToolExecutionStartEvent has been pushed, so
	// the abort path can open the calls the prepare loop never reached and
	// every call closes exactly once (REQ-LOOP-11.3).
	started := make([]bool, len(calls))

	byName := make(map[string]core.Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}

	n := len(calls)
	results := make([]core.ToolResultMessage, n)
	votes := make([]bool, n)
	thunks := make([]func(), 0, n)

	// env is what a wrapper's nested calls run with (07-REQ-4.3): the same
	// config copy, stream and reporter as this batch.
	env := &nestedEnv{a: a, cfg: cfg, s: s, assistant: assistant, turnCount: turnCount, report: report}

	// batchMu is BATCH-SCOPED: created here, acquired by nothing else, and
	// a.mu is never acquired while it is held.
	//
	// This is the reconciliation of REQ-LOOP-05 phase 3 ("under one mutex")
	// with NFR-REL-02.1 ("no interceptor may run while an agent lock is
	// held"), which contradict each other on a literal reading (ruling P-6).
	// The natural reading — reuse the agent mutex — IS the deadlock
	// NFR-REL-02.1 exists to prevent.
	var batchMu sync.Mutex

	// ---- Phase 1: prepare, strictly sequential.
	// finalizeInline closes a call that never reaches a handler — unknown
	// tool, bad arguments, a block. It still emits the execution end event
	// and the result, so every call opens and closes exactly once on the
	// stream whichever way it ended (REQ-LOOP-11.3), and a UI keyed on the
	// start event never waits for an end that will not come.
	finalizeInline := func(i int, m core.ToolResultMessage) {
		results[i] = m
		s.Push(core.ToolExecutionEndEvent{ToolUseID: m.ToolUseID, Name: m.ToolName, IsError: m.IsError})
		s.Push(core.ToolResultEvent{Message: m})
	}

	for i, c := range calls {
		i, c := i, c
		// REQ-LOOP-11.2: once the context is cancelled the prepare loop stops
		// enqueuing further calls. The calls already prepared are still
		// covered by the single decision below, and the ones never reached
		// are closed there as aborted.
		if ctx.Err() != nil {
			break
		}

		// No ToolCallStart/ToolCallEnd here. Those describe the MODEL emitting
		// a tool call and are the provider's to push as it streams; the loop
		// owns the EXECUTION triple (ToolExecutionStart/Update/End) and the
		// finalized ToolResultEvent. REQ-LOOP-05 names the call events and
		// REQ-OBS-06 names the execution ones — emitting both duplicates every
		// call in any UI driven by the stream (ruling C19).
		//
		// The execution START is emitted HERE, in the sequential prepare
		// phase, not inside the thunk: REQ-LOOP-05 phase 1 emits the start
		// event for each call in order, and a call finalized inline (blocked,
		// invalid) or aborted before its handler ran must still have opened.
		s.Push(core.ToolExecutionStartEvent{ToolUseID: c.ID, Name: c.Name})
		started[i] = true

		tool, known := byName[c.Name]
		if !known {
			finalizeInline(i, errorResult(c, "unknown_tool", unknownToolMessage(c.Name, byName)))
			continue
		}

		// REQ-TOOL-11: the whole pipeline — the per-tool shim included — runs
		// inside the panic-recover boundary of REQ-LOOP-03. Tool.PrepareArguments
		// is user code; a panic in it is an invalid-arguments result, not a
		// crashed process.
		var (
			prepared core.PreparedArguments
			perr     error
		)
		func() {
			defer func() {
				if r := recover(); r != nil {
					perr = fmt.Errorf("argument preparation for %q panicked: %v", c.Name, r)
					report(fmt.Errorf("agentkit: panic in PrepareArguments for %q: %v", c.Name, r))
				}
			}()
			prepared, perr = core.PrepareArguments(tool, c)
		}()
		if perr != nil {
			// The error text re-serializes the model's OWN key order, so the
			// message is self-correcting (REQ-TOOL-11.4, REQ-TOOL-12.3).
			finalizeInline(i, errorResult(c, "invalid_arguments", perr.Error()))
			continue
		}

		if cfg.BeforeToolCall != nil {
			dec := a.callBefore(ctx, cfg.BeforeToolCall, core.BeforeToolCallContext{
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
				// A blocked call casts the same termination vote, which is
				// what lets a permission denial end the run instead of looping
				// the model into retrying (REQ-TOOL-13.2). Honoured only when
				// Block is set.
				votes[i] = dec.Terminate
				finalizeInline(i, errorResult(c, core.BlockErrorCode, reason))
				continue
			}
			if dec.Arguments != nil {
				// The interceptor may widen as well as narrow (REQ-SEC-03.5).
				// Arguments it returns that JSON cannot carry fail this call
				// alone, as any other invalid arguments do.
				next, err := prepared.TryWithArgs(dec.Arguments)
				if err != nil {
					finalizeInline(i, errorResult(c, "invalid_arguments",
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
			out := invokeHandler(hctx, tool, prepared)
			// An interceptor that voted to terminate during one of this
			// wrapper's nested calls ends the run, whatever the wrapper
			// returned (07-REQ-5.3). AfterToolCall below may still override.
			if nested != nil && nested.terminated.Load() {
				out.Terminate = true
			}
			// A nested call's own terminate vote was dropped; the wrapper's
			// result records that it happened (07-REQ-6.3).
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
				if cfg.AfterToolCall != nil {
					dec := callAfter(ctx, report, cfg.AfterToolCall, core.AfterToolCallContext{
						ToolName:   c.Name,
						ToolUseID:  c.ID,
						Arguments:  prepared.Args,
						Result:     &msg,
						Elapsed:    time.Since(start),
						ToolResult: out,
					})
					// Terminate is *bool: nil means "no opinion", so an
					// interceptor that does not care cannot accidentally vote
					// against a tool that does (REQ-TOOL-13.3).
					if dec.Terminate != nil {
						out.Terminate = *dec.Terminate
					}
				}
				results[i] = msg
				votes[i] = out.Terminate
				s.Push(core.ToolExecutionEndEvent{
					ToolUseID: c.ID, Name: c.Name, IsError: msg.IsError,
					ElapsedMS: time.Since(start).Milliseconds(),
				})
				s.Push(core.ToolResultEvent{Message: msg})
			}()
		})
	}

	// ---- Phase 2: the abort decision, made ONCE, here, on the loop
	// goroutine, after prepare and BEFORE any handler starts (REQ-LOOP-11).
	//
	// It must not be re-checked inside each tool goroutine. Per-goroutine
	// ctx.Err() checks — the obvious Go idiom, and what REQ-GO-05 alone
	// implies — let the scheduler SPLIT the batch: an abort landing just after
	// the batch starts skips whichever calls had not yet been scheduled and
	// runs the rest. That is nondeterministic, unreproducible in tests, and
	// shows up in production as phantom side effects after the user pressed
	// Ctrl-C.
	//
	// The handlers still RECEIVE ctx, so a running subprocess is still killed
	// (REQ-TOOL-17.2). What must not be re-decided is *whether a handler runs*
	// (ruling P-20).
	if ctx.Err() != nil {
		for i, c := range calls {
			if results[i].ToolUseID != "" {
				continue // already finalized in prepare (blocked/invalid)
			}
			// A call the prepare loop never reached still opens, so it can
			// close (REQ-LOOP-11.3).
			if !started[i] {
				s.Push(core.ToolExecutionStartEvent{ToolUseID: c.ID, Name: c.Name})
				started[i] = true
			}
			finalizeInline(i, abortedResult(c))
		}
		// The calls that were blocked in prepare keep their termination vote;
		// an aborted call abstains. The AND over the batch is therefore false
		// whenever any call was aborted, which is the correct reading of
		// REQ-TOOL-13.1: an aborted batch did not finish, so it does not
		// finish the run on a tool's say-so.
		return results, false
	}

	sequential := !cfg.ParallelTools || len(thunks) <= 1
	if !sequential {
		// A single Sequential tool anywhere in the batch demotes the WHOLE
		// batch (REQ-LOOP-05a). Tools with process-wide or workspace-wide
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
	// with IsError set (REQ-LOOP-05b). A batch returning fewer results than
	// calls leaves dangling tool_use blocks and makes the next request
	// invalid, so this is asserted rather than assumed.
	for i, c := range calls {
		if results[i].ToolUseID == "" {
			results[i] = errorResult(c, "no_result",
				"internal: the tool batch produced no result for this call")
		}
	}
	return results, core.BatchTerminates(votes)
}

// invokeHandler calls the tool, converting every failure mode into a result.
// No tool outcome is ever propagated to the caller as a Go error (REQ-GO-04).
func invokeHandler(ctx context.Context, t core.Tool, p core.PreparedArguments) (res core.ToolResult) {
	defer func() {
		if r := recover(); r != nil {
			// A handler panic becomes a tool result and the loop continues
			// (NFR-REL-02.2). It must never crash the agent process.
			res = core.ErrResult("panic", fmt.Sprintf("tool %q panicked: %v", t.Name, r))
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
// wrapper, with NO agent lock held (NFR-REL-02). A panicking interceptor fails
// CLOSED — it blocks the call — because a security boundary that opens on
// panic is not a boundary.
func (a *Agent) callBefore(ctx context.Context, f core.BeforeToolCall, in core.BeforeToolCallContext) (dec core.BeforeToolCallDecision) {
	defer func() {
		if r := recover(); r != nil {
			dec = core.BeforeToolCallDecision{
				Block:  true,
				Reason: fmt.Sprintf("interceptor panicked: %v", r),
			}
			a.fireError(fmt.Errorf("agentkit: panic in BeforeToolCall for %q: %v", in.ToolName, r))
		}
	}()
	return f(ctx, in)
}

// callAfter invokes the after-interceptor through a recovering wrapper. A
// panic here yields "no opinion" rather than a vote: unlike BeforeToolCall
// this is not a security boundary, and inventing a termination vote from a
// crash would end the run for the wrong reason.
//
// It reports through the batch's lock-free reporter, never a.fireError: it
// runs inside the batch-scoped critical section, and the agent lock is never
// taken there.
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
// the REQ-TOOL-08 JSON envelope; either way the tool's extra blocks (images)
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

func errorResult(c core.ToolUseBlock, code, detail string) core.ToolResultMessage {
	return toolResultMessage(c, core.ErrResult(code, detail))
}

// abortedResult is the fixed shape of a call that did not run because the
// batch was aborted. It still emits events and a result so the transcript
// stays well-formed and resumable (REQ-LOOP-11.3).
func abortedResult(c core.ToolUseBlock) core.ToolResultMessage {
	m := toolResultMessage(c, core.ErrResult("aborted", "Operation aborted"))
	return m
}

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
