package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

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
	opts   Options
	tools  []core.Tool
	ctx    context.Context
	thread *starlark.Thread
	out    *tools.Accumulator
	seq    int

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
	r := &runner{opts: opts, tools: bound, ctx: ctx, out: tools.NewAccumulator(opts.MaxOutputBytes, tools.TruncateMiddle)}
	return r.run(a.Script)
}

// run executes the script and renders its result.
func (r *runner) run(script string) core.ToolResult {
	r.thread = &starlark.Thread{
		Name: r.opts.Name,
		Print: func(_ *starlark.Thread, msg string) {
			_, _ = r.out.Write([]byte(msg + "\n"))
		},
		Load: func(*starlark.Thread, string) (starlark.StringDict, error) {
			return nil, errors.New("load is not permitted in code_mode scripts")
		},
	}
	// The script stops when the caller's context ends.
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
		return r.failure("script_failed", err.Error(), false)
	}
	retGo, err := toGo(ret)
	if err != nil {
		return r.failure("script_failed", "the return value: "+err.Error(), false)
	}
	printed := strings.TrimSuffix(r.out.String(), "\n")
	text := printed
	if ret != starlark.None {
		if text != "" {
			text += "\n"
		}
		text += "Return value: " + ret.String()
	}
	if text == "" {
		text = "[Script finished with no output]"
	}
	res := core.OKResult(map[string]any{"output": printed, "return_value": retGo, "calls_completed": []any{}})
	res.Text = text
	return res
}

// predeclared is everything a script can name besides Starlark's own
// builtins: the bound tools and the helpers.
func (r *runner) predeclared() starlark.StringDict {
	env := starlark.StringDict{
		"parallel": placeholder("parallel"),
		"call":     placeholder("call"),
		"is_error": placeholder("is_error"),
	}
	for _, t := range r.tools {
		env[t.Name] = r.toolFunc(t)
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

// ctxHalt is the halt for a context that ended.
func (r *runner) ctxHalt() *halt {
	return &halt{code: "aborted", detail: "Operation aborted"}
}

// failure is an error result for the script.
func (r *runner) failure(code, detail string, terminate bool) core.ToolResult {
	res := core.ErrResult(code, detail)
	res.Terminate = terminate
	printed := strings.TrimSuffix(r.out.String(), "\n")
	res.Data = map[string]any{"error": code, "message": detail, "partial_output": printed, "calls_completed": []any{}}
	return res
}
