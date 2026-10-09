package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/agentfox/agentkit-go/core"
)

// reservedNames are globals the runtime defines or a script uses by
// convention; a bound tool may not take one.
var reservedNames = map[string]bool{
	"parallel": true, "call": true, "is_error": true, "main": true, "result": true,
}

// checkBindable refuses a tool name a script could not call: not a Starlark
// identifier, a keyword, a builtin, or a name the runtime reserves.
func checkBindable(name string) error {
	if !isIdentifier(name) {
		return fmt.Errorf("codemode: tool name %q is not a Starlark identifier", name)
	}
	if _, builtin := starlark.Universe[name]; builtin || reservedNames[name] {
		return fmt.Errorf("codemode: tool name %q collides with a name the script runtime defines", name)
	}
	return nil
}

func isIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	// A keyword parses as itself, not as an identifier.
	_, err := syntax.ParseExpr("", name, 0)
	return err == nil
}

// halt is why a builtin stopped the script: a limit, a terminate vote, an
// abort. The runner reports it in place of the Starlark error it caused.
type halt struct {
	code      string
	detail    string
	terminate bool
}

func (h *halt) Error() string { return h.detail }

// toolFunc is the Starlark function a script calls for tool t.
func (r *runner) toolFunc(t core.Tool) *starlark.Builtin {
	return starlark.NewBuiltin(t.Name, func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if len(args) > 0 {
			return nil, argErrorf("%s only accepts keyword arguments, e.g. %s(name=value)", b.Name(), b.Name())
		}
		block, err := r.block(t.Name, kwargs)
		if err != nil {
			return nil, err
		}
		res, err := r.dispatch(block)
		if err != nil {
			return nil, err
		}
		return r.value(res[0])
	})
}

// block builds the ToolUseBlock for a call from its keyword arguments.
func (r *runner) block(name string, kwargs []starlark.Tuple) (core.ToolUseBlock, error) {
	args := make(map[string]any, len(kwargs))
	for _, kv := range kwargs {
		k := string(kv[0].(starlark.String))
		gv, err := toGo(kv[1])
		if err != nil {
			return core.ToolUseBlock{}, argErrorf("%s: argument %s: %v", name, k, err)
		}
		args[k] = gv
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return core.ToolUseBlock{}, argErrorf("%s: %v", name, err)
	}
	r.seq++
	return core.ToolUseBlock{ID: fmt.Sprintf("codemode_%d", r.seq), Name: name, Input: raw}, nil
}

// dispatch runs blocks through the agent's nested pipeline and returns their
// results in order. A terminate vote or a cancelled context stops the script:
// the error returned is a *halt the runner reports.
func (r *runner) dispatch(blocks ...core.ToolUseBlock) ([]core.ToolResult, error) {
	if err := r.takeCalls(len(blocks)); err != nil {
		return nil, err
	}
	res, err := core.CallNested(r.ctx, blocks...)
	switch {
	case errors.Is(err, core.ErrTerminated):
		return nil, r.stop(&halt{code: "terminated", terminate: true,
			detail: "an interceptor ended the run during a tool call; the script was stopped"})
	case r.ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return nil, r.stop(r.ctxHalt())
	case err != nil:
		return nil, err
	}
	r.record(blocks, res)
	return res, nil
}

// value is what a call returns to the script: the result's Data, or
// {"text": Text} when it has none.
func (r *runner) value(res core.ToolResult) (starlark.Value, error) {
	if !res.OK {
		// A failure is a value the script inspects, never an exception
		// it cannot catch (08-REQ-5.1). Starlark has no try.
		return NewToolError(res), nil
	}
	if res.Data == nil {
		d := starlark.NewDict(1)
		_ = d.SetKey(starlark.String("text"), starlark.String(res.Text))
		return d, nil
	}
	return toStarlark(res.Data)
}

// stop records why the script is stopping and cancels the thread, so the
// interpreter unwinds at once even if the script catches nothing.
func (r *runner) stop(h *halt) error {
	r.mu.Lock()
	if r.halted == nil {
		r.halted = h
	}
	r.mu.Unlock()
	r.thread.Cancel(h.code)
	return h
}
