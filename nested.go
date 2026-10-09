package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// nestedEnv is what a nested call needs from the batch that runs its
// wrapper. It is built once per batch from the same cfg copy executeBatch
// uses, so a nested call never reaches back through a.mu for configuration.
type nestedEnv struct {
	cfg       Config
	s         *core.EventStream
	assistant *core.AssistantMessage
	turnCount int
	report    func(error)
}

// nestedCaller is the core.NestedCaller executeBatch attaches to a wrapper's
// context (07-REQ-4.3). It is bound to one wrapper call: its parent id and
// name, and the wrapper's reachable tools as the policy resolved them. A
// wrapper can call those tools and no others.
type nestedCaller struct {
	env        *nestedEnv
	parentID   string
	parentName string
	tools      map[string]core.Tool

	// mu serializes finalization (AfterToolCall), as the batch mutex does
	// for direct calls.
	mu sync.Mutex
	// eventMu serializes this caller's stream pushes. It is separate from mu
	// so an AfterToolCall that calls CallNested itself does not deadlock on
	// the start event of its own call.
	eventMu sync.Mutex
	// terminated is set when an interceptor votes to end the run during one
	// of this wrapper's nested calls. Every later Call returns
	// ErrTerminated, and the wrapper's own result carries the vote.
	terminated atomic.Bool
	// voteIgnored is set when a nested handler voted to terminate and the
	// vote was dropped; the wrapper's result says so (07-REQ-6.3).
	voteIgnored atomic.Bool
}

func (env *nestedEnv) caller(parentID, parentName string, reach []core.Tool) *nestedCaller {
	tools := make(map[string]core.Tool, len(reach))
	for _, t := range reach {
		tools[t.Name] = t
	}
	return &nestedCaller{env: env, parentID: parentID, parentName: parentName, tools: tools}
}

// withCaller returns the context a tool's handler runs under: with a
// nestedCaller attached when the tool reaches other tools, unchanged (and a
// nil caller) when it does not.
func (env *nestedEnv) withCaller(ctx context.Context, c core.ToolUseBlock, t core.Tool) (context.Context, *nestedCaller) {
	if len(t.ReachableTools) == 0 {
		return ctx, nil
	}
	nc := env.caller(c.ID, c.Name, t.ReachableTools)
	return core.WithNestedCaller(ctx, nc), nc
}

// Call runs calls for the wrapper. Preparation and authorization run in
// order, as for a direct batch, so the interceptor sees a deterministic
// sequence. Then the handlers run: concurrently, one goroutine per call
// joined by a WaitGroup (07-REQ-7.1), unless one of the calls is to a
// Sequential tool, which runs them in call order (07-REQ-7.2). Results are written by index, so they come back in call
// order whatever order the handlers finish in (07-REQ-7.3), and a call that
// fails is a result of its own that stops no sibling (07-REQ-7.4).
func (n *nestedCaller) Call(ctx context.Context, calls ...core.ToolUseBlock) ([]core.ToolResult, error) {
	if n.terminated.Load() {
		return nil, core.ErrTerminated
	}
	results := make([]core.ToolResult, len(calls))
	// A single Sequential tool among the calls demotes the whole group, as
	// in a direct batch, whether or not that call survives preparation.
	sequential := false
	for _, c := range calls {
		if t, ok := n.tools[c.Name]; ok && t.ExecutionMode == core.Sequential {
			sequential = true
		}
	}
	type queued struct {
		i        int
		call     core.ToolUseBlock
		tool     core.Tool
		prepared core.PreparedArguments
	}
	var run []queued
	for i, c := range calls {
		prepared, tool, call, res, ok := n.prepare(ctx, calls, i, c)
		if !ok {
			results[i] = res
			n.closeInline(call, res)
		}
		if n.terminated.Load() {
			// An interceptor ended the wrapper. The calls already queued
			// opened on the stream and never ran; they close as aborted,
			// so every nested call that opened closes exactly once.
			for _, q := range run {
				n.closeInline(q.call, nestedAborted())
			}
			return nil, core.ErrTerminated
		}
		if ok {
			run = append(run, queued{i, call, tool, prepared})
		}
	}
	if sequential || len(run) <= 1 {
		for _, q := range run {
			results[q.i] = n.execute(ctx, q.call, q.tool, q.prepared)
		}
	} else {
		var wg sync.WaitGroup
		for _, q := range run {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[q.i] = n.execute(ctx, q.call, q.tool, q.prepared)
			}()
		}
		wg.Wait()
	}
	if n.terminated.Load() {
		return nil, core.ErrTerminated
	}
	return results, nil
}

// nestedAborted is the result of a nested call that was cancelled
// (07-REQ-7.5).
func nestedAborted() core.ToolResult { return core.ErrResult("aborted", "Operation aborted") }

// prepare takes one nested call through the steps before its handler. ok is
// false when the call ends here, and res is its result.
func (n *nestedCaller) prepare(ctx context.Context, batch []core.ToolUseBlock, i int, c core.ToolUseBlock) (
	prepared core.PreparedArguments, tool core.Tool, call core.ToolUseBlock, res core.ToolResult, ok bool) {

	// Every nested call gets an id of its own (07-REQ-8.1): the wrapper's
	// ids are its own business, and need not be unique across wrappers or
	// turns. With the id it opens on the stream, so that, however it ends,
	// it closes there too.
	id := newID("nested")
	call = core.ToolUseBlock{ID: id, Name: c.Name, Input: c.Input}
	n.push(core.ToolExecutionStartEvent{ToolUseID: id, Name: c.Name, ParentToolUseID: n.parentID})

	// A cancelled context starts nothing more.
	if ctx.Err() != nil {
		return prepared, tool, call, nestedAborted(), false
	}
	tool, known := n.tools[c.Name]
	if !known {
		return prepared, tool, call, core.ErrResult("unknown_tool",
			fmt.Sprintf("%s cannot call %q: ", n.parentName, c.Name)+unknownToolMessage(c.Name, n.tools)), false
	}
	input := c.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	call, err := core.NewToolUse(id, c.Name, input)
	if err != nil {
		return prepared, tool, core.ToolUseBlock{ID: id, Name: c.Name, Input: c.Input},
			core.ErrResult("invalid_arguments", err.Error()), false
	}

	var perr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				perr = fmt.Errorf("argument preparation for %q panicked: %v", c.Name, r)
				n.env.report(fmt.Errorf("agentkit: panic in PrepareArguments for %q: %v", c.Name, r))
			}
		}()
		prepared, perr = core.PrepareArguments(tool, call)
	}()
	if perr != nil {
		return prepared, tool, call, core.ErrResult("invalid_arguments", perr.Error()), false
	}

	cfg := n.env.cfg
	if cfg.Guard != nil {
		dec := callBefore(ctx, n.env.report, cfg.Guard, core.BeforeToolCallContext{
			ToolName:        call.Name,
			ToolUseID:       call.ID,
			ParentToolUseID: n.parentID,
			ParentToolName:  n.parentName,
			Tool:            tool,
			Arguments:       prepared.Args,
			RawInput:        call.Input,
			Assistant:       n.env.assistant,
			Batch:           batch,
			Index:           i,
			TurnCount:       n.env.turnCount,
		})
		if dec.Block {
			// A block is a result the wrapper can read and react to, not an
			// error that aborts it (07-REQ-5.2). A block that also votes to
			// terminate ends the wrapper (07-REQ-5.3): the interceptor is the
			// embedder's authority, and the wrapper must not carry on.
			if dec.Terminate {
				n.terminated.Store(true)
			}
			reason := dec.Reason
			if reason == "" {
				reason = "blocked by policy"
			}
			return prepared, tool, call, core.ErrResult(core.BlockErrorCode, reason), false
		}
		if dec.Arguments != nil {
			next, err := prepared.TryWithArgs(dec.Arguments)
			if err != nil {
				return prepared, tool, call, core.ErrResult("invalid_arguments",
					"BeforeToolCall returned arguments that are not JSON: "+err.Error()), false
			}
			prepared = next
		}
	}
	return prepared, tool, call, core.ToolResult{}, true
}

// execute runs one prepared nested call's handler and finalizes it.
func (n *nestedCaller) execute(ctx context.Context, call core.ToolUseBlock, tool core.Tool, prepared core.PreparedArguments) core.ToolResult {
	start := time.Now()
	hctx, child := n.env.withCaller(ctx, call, tool)
	out := invokeHandler(hctx, n.env.report, tool, prepared)
	if child != nil {
		if child.terminated.Load() {
			// A wrapper nested in this one was terminated; so is this one.
			n.terminated.Store(true)
		}
		if child.voteIgnored.Load() {
			out.Detail = annotate(out.Detail, "nested terminate vote ignored")
		}
	}
	// A call still running when the context was cancelled is aborted,
	// whatever its handler made of the cancellation (07-REQ-7.5).
	if ctx.Err() != nil {
		out = nestedAborted()
	}
	// A nested handler cannot end the run (07-REQ-6.1). No tool a wrapper
	// reaches is Terminating — registration refuses that — so the vote is
	// always dropped, and recorded where it was cast and on the wrapper.
	if out.Terminate {
		out.Terminate = false
		out.Detail = annotate(out.Detail, "terminate vote ignored: "+call.Name+" was called through "+n.parentName)
		n.voteIgnored.Store(true)
	}
	elapsed := time.Since(start)

	// Finalize under the caller's mutex, as a direct call finalizes under the
	// batch mutex: AfterToolCall and the end event are serialized, with a
	// deferred unlock so a panicking listener cannot leave it held.
	n.mu.Lock()
	defer n.mu.Unlock()
	if after := n.env.cfg.After; after != nil {
		msg := toolResultMessage(call, out)
		dec := callAfter(ctx, n.env.report, after, core.AfterToolCallContext{
			ToolName:        call.Name,
			ToolUseID:       call.ID,
			ParentToolUseID: n.parentID,
			ParentToolName:  n.parentName,
			Arguments:       prepared.Args,
			Result:          &msg,
			Elapsed:         elapsed,
			ToolResult:      out,
		})
		// The after-interceptor is the embedder's authority as much as the
		// before one: its vote to terminate ends the wrapper too.
		if dec.Terminate != nil && *dec.Terminate {
			n.terminated.Store(true)
		}
	}
	n.push(n.endEvent(call, out, elapsed))
	return out
}

// closeInline ends a nested call that never reached its handler: it closes
// on the stream, as a direct call finalized in prepare is.
func (n *nestedCaller) closeInline(call core.ToolUseBlock, res core.ToolResult) {
	n.push(n.endEvent(call, res, 0))
}

// annotate appends note to a result's detail.
func annotate(detail, note string) string {
	if detail == "" {
		return note
	}
	return detail + "; " + note
}

// push emits a nested call's execution event. Pushes are serialized so a
// nested call's events are ordered as a direct call's are.
func (n *nestedCaller) push(e core.Event) {
	n.eventMu.Lock()
	defer n.eventMu.Unlock()
	n.env.s.Push(e)
}

// endEvent closes a nested call on the stream. There is no ToolResultEvent:
// that event is the transcript's, and a nested result is the wrapper's
// (07-REQ-8.3).
func (n *nestedCaller) endEvent(call core.ToolUseBlock, out core.ToolResult, elapsed time.Duration) core.Event {
	return core.ToolExecutionEndEvent{ToolUseID: call.ID, Name: call.Name, IsError: !out.OK,
		ElapsedMS: elapsed.Milliseconds(), ParentToolUseID: n.parentID}
}
