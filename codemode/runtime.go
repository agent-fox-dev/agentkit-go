package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

// fileOptions is the Starlark dialect scripts are written in: while loops,
// top-level control flow, reassignable globals and sets are allowed, since a
// model writes scripts the way it writes Python. Recursion stays off.
var fileOptions = &syntax.FileOptions{While: true, TopLevelControl: true, GlobalReassign: true, Set: true}

// runner is one script run: a fresh thread and the state its builtins share.
type runner struct {
	opts  Options
	tools []core.Tool
	// parent is the caller's context; ctx is it with MaxTimeout applied.
	parent context.Context
	ctx    context.Context
	thread *starlark.Thread
	out    *tools.Accumulator
	seq    int
	// funcs are the bound tools' functions by name, for parallel and call.
	funcs map[string]*starlark.Builtin
	// ledger is every nested call made, in order, with its outcome. Only
	// the script's thread touches it.
	ledger []ledgerEntry

	mu     sync.Mutex
	halted *halt
}

// execute is the code-mode tool's handler.
func execute(ctx context.Context, opts Options, bound []core.Tool, in json.RawMessage) core.ToolResult {
	var a struct {
		Script string `json:"script"`
	}
	if err := json.Unmarshal(in, &a); err != nil {
		return core.ErrResult("invalid_arguments", err.Error())
	}
	if strings.TrimSpace(a.Script) == "" {
		return core.ErrResult("invalid_arguments", "script is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, opts.MaxTimeout)
	defer cancel()
	r := &runner{opts: opts, tools: bound, parent: ctx, ctx: runCtx, out: newOutput(opts)}
	start := time.Now()
	res := r.run(a.Script)
	r.finishOutput(&res, time.Since(start))
	return res
}

// run executes the script and renders its result.
func (r *runner) run(script string) core.ToolResult {
	// A run whose caller already gave up does not start (08-REQ-9.5).
	if r.ctx.Err() != nil {
		h := r.ctxHalt()
		return r.failure(h.code, h.detail, false)
	}
	r.thread = &starlark.Thread{
		Name:  r.opts.Name,
		Print: func(_ *starlark.Thread, msg string) { r.print(msg) },
		Load: func(*starlark.Thread, string) (starlark.StringDict, error) {
			return nil, errors.New("load is not permitted in code_mode scripts")
		},
		OnMaxSteps: func(*starlark.Thread) { _ = r.stop(r.stepHalt()) },
	}
	r.thread.SetMaxExecutionSteps(r.opts.MaxSteps)

	// The script stops when the caller's context ends or MaxTimeout passes.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-r.ctx.Done():
			_ = r.stop(r.ctxHalt())
		case <-done:
		}
	}()

	globals, err := starlark.ExecFileOptions(fileOptions, r.thread, "script.star", script, r.predeclared())
	var ret starlark.Value = starlark.None
	if err == nil {
		ret, err = r.returnValue(globals)
	}
	if h := r.halt(); h != nil {
		return r.failure(h.code, h.detail, h.terminate)
	}
	if err != nil {
		return r.scriptError(err)
	}
	retGo, err := toGo(ret)
	if err != nil {
		return r.failure("script_failed", "the return value: "+err.Error(), false)
	}
	// The return value counts towards MaxOutputBytes like printed output.
	printed := r.printed()
	if line := returnLine(ret); line != "" {
		r.write(line + "\n")
		if h := r.halt(); h != nil {
			return r.failure(h.code, h.detail, h.terminate)
		}
	}
	res := core.OKResult(map[string]any{"output": printed, "return_value": retGo, "calls_completed": r.ledgerData()})
	res.Text = FormatResultText(printed, ret)
	return res
}

// print is the script's print: one line into the output.
func (r *runner) print(msg string) { r.write(msg + "\n") }

// write adds to the output, and stops the script once it is past
// MaxOutputBytes (08-REQ-7.4).
func (r *runner) write(s string) {
	_, _ = r.out.Write([]byte(s))
	if r.out.Total() > int64(r.opts.MaxOutputBytes) {
		_ = r.stop(r.outputHalt())
	}
}

// printed is the output so far, without its final newline.
func (r *runner) printed() string { return strings.TrimSuffix(r.out.String(), "\n") }

// predeclared is everything a script can name besides Starlark's own
// builtins: the bound tools and the helpers.
func (r *runner) predeclared() starlark.StringDict {
	env := starlark.StringDict{
		"parallel": starlark.NewBuiltin("parallel", r.parallel),
		"call":     starlark.NewBuiltin("call", r.call),
		"is_error": starlark.NewBuiltin("is_error", isError),
	}
	r.funcs = make(map[string]*starlark.Builtin, len(r.tools))
	for _, t := range r.tools {
		fn := r.toolFunc(t)
		r.funcs[t.Name] = fn
		env[t.Name] = fn
	}
	return env
}

// returnValue is main()'s result when the script defines main, else the
// global result, else None.
func (r *runner) returnValue(globals starlark.StringDict) (starlark.Value, error) {
	if fn, ok := globals["main"].(starlark.Callable); ok {
		return starlark.Call(r.thread, fn, nil, nil)
	}
	if v, ok := globals["result"]; ok {
		return v, nil
	}
	return starlark.None, nil
}

func (r *runner) halt() *halt {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.halted
}
