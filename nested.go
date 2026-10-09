package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// nestedEnv is what a nested call needs from the batch that runs its
// wrapper. It is built once per batch from the same cfg copy executeBatch
// uses, so a nested call never reaches back through a.mu for configuration.
type nestedEnv struct {
	a         *Agent
	cfg       core.AgentConfig
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
// joined by a WaitGroup (07-REQ-7.1), unless ParallelTools is off or one of
// the calls is to a Sequential tool, which runs them in call order
// (07-REQ-7.2). Results are written by index, so they come back in call
// order whatever order the handlers finish in (07-REQ-7.3), and a call that
// fails is a result of its own that stops no sibling (07-REQ-7.4).
func (n *nestedCaller) Call(ctx context.Context, calls ...core.ToolUseBlock) ([]core.ToolResult, error) {
	if n.terminated.Load() {
		return nil, core.ErrTerminated
	}
	results := make([]core.ToolResult, len(calls))
	var run []func()
	sequential := !n.env.cfg.ParallelTools
	for i, c := range calls {
		prepared, tool, call, res, ok := n.prepare(ctx, calls, i, c)
		if n.terminated.Load() {
			return nil, core.ErrTerminated
		}
		if !ok {
			results[i] = res
			n.audit(call, tool, prepared.Raw, res, false, 0)
			continue
		}
		if tool.ExecutionMode == core.Sequential {
			sequential = true
		}
		run = append(run, func() { results[i] = n.execute(ctx, call, tool, prepared) })
	}
	if sequential || len(run) <= 1 {
		for _, f := range run {
			f()
		}
	} else {
		var wg sync.WaitGroup
		for _, f := range run {
			wg.Add(1)
			go func() {
				defer wg.Done()
				f()
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

	// A cancelled context starts nothing more.
	if ctx.Err() != nil {
		return prepared, tool, c, nestedAborted(), false
	}
	tool, known := n.tools[c.Name]
	if !known {
		return prepared, tool, c, core.ErrResult("unknown_tool",
			fmt.Sprintf("%s cannot call %q: ", n.parentName, c.Name)+unknownToolMessage(c.Name, n.tools)), false
	}
	// Every nested call gets an id of its own: the wrapper's are its own
	// business, and need not be unique across wrappers or turns.
	input := c.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	call, err := core.NewToolUse(newID("nested"), c.Name, input)
	if err != nil {
		return prepared, tool, c, core.ErrResult("invalid_arguments", err.Error()), false
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
	if cfg.BeforeToolCall != nil {
		dec := n.env.a.callBefore(ctx, cfg.BeforeToolCall, core.BeforeToolCallContext{
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
	if d, by := pluginVeto(ctx, cfg.Plugins, call.Name, prepared.Raw); d == core.PluginBlock {
		return prepared, tool, call, core.ErrResult("blocked_by_plugin",
			fmt.Sprintf("plugin %q blocked this call", by.PluginName())), false
	}
	return prepared, tool, call, core.ToolResult{}, true
}

// execute runs one prepared nested call's handler and finalizes it.
func (n *nestedCaller) execute(ctx context.Context, call core.ToolUseBlock, tool core.Tool, prepared core.PreparedArguments) core.ToolResult {
	start := time.Now()
	hctx, child := n.env.withCaller(ctx, call, tool)
	out := invokeHandler(hctx, tool, prepared)
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
	ignored := out.Terminate
	if ignored {
		out.Terminate = false
		out.Detail = annotate(out.Detail, "terminate vote ignored: "+call.Name+" was called through "+n.parentName)
		n.voteIgnored.Store(true)
	}
	n.audit(call, tool, prepared.Raw, out, ignored, time.Since(start))

	cfg := n.env.cfg
	if cfg.AfterToolCall != nil {
		n.mu.Lock()
		defer n.mu.Unlock()
		msg := toolResultMessage(call, out)
		dec := callAfter(ctx, n.env.report, cfg.AfterToolCall, core.AfterToolCallContext{
			ToolName:        call.Name,
			ToolUseID:       call.ID,
			ParentToolUseID: n.parentID,
			ParentToolName:  n.parentName,
			Arguments:       prepared.Args,
			Result:          &msg,
			Elapsed:         time.Since(start),
			ToolResult:      out,
		})
		// The after-interceptor is the embedder's authority as much as the
		// before one: its vote to terminate ends the wrapper too.
		if dec.Terminate != nil && *dec.Terminate {
			n.terminated.Store(true)
		}
	}
	return out
}

// annotate appends note to a result's detail.
func annotate(detail, note string) string {
	if detail == "" {
		return note
	}
	return detail + "; " + note
}

// audit records one nested call like a direct one, linked to its wrapper's
// call (07-REQ-8.4).
func (n *nestedCaller) audit(call core.ToolUseBlock, tool core.Tool, args json.RawMessage, out core.ToolResult,
	ignored bool, elapsed time.Duration) {
	if len(args) == 0 {
		args = call.Input
	}
	n.env.a.audit(core.AuditEvent{
		Kind: core.AuditToolCall, SessionID: n.env.cfg.SessionID,
		ToolName: call.Name, ToolUseID: call.ID, ParentToolUseID: n.parentID,
		ServerName:       serverNameOf(tool, call.Name),
		ArgumentsHash:    core.HashArguments(args),
		IsError:          !out.OK,
		ErrorCode:        errorCodeOf(out),
		ElapsedMS:        elapsed.Milliseconds(),
		TerminateIgnored: ignored,
	})
}
