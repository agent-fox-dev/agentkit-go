package codemode

import (
	"errors"
	"fmt"

	"go.starlark.net/resolve"
	"go.starlark.net/starlark"
	"go.starlark.net/syntax"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// argError is a malformed call the script made: positional arguments, an
// argument JSON cannot carry, a bad parallel entry. It is classified as
// invalid_arguments rather than a runtime error.
type argError struct{ msg string }

func (e *argError) Error() string { return e.msg }

func argErrorf(format string, a ...any) error { return &argError{msg: fmt.Sprintf(format, a...)} }

// diagnosis is a script error classified for the model (08-REQ-9).
type diagnosis struct {
	code   string
	line   int
	detail string
	// trace is the call stack, for a runtime error inside a function.
	trace string
}

// diagnose classifies a script's error:
//   - syntax_error: the script does not parse;
//   - runtime_error: it parses but fails while running, or names something
//     undefined (Starlark resolves names before running anything);
//   - invalid_arguments: a malformed tool call;
//   - script_failed: anything else, such as a return value JSON cannot carry.
//
// Each carries the line, and a detail with the file and line.
func diagnose(err error) diagnosis {
	var (
		synErr  syntax.Error
		resErr  resolve.ErrorList
		evalErr *starlark.EvalError
		argErr  *argError
	)
	switch {
	case errors.As(err, &synErr):
		return diagnosis{code: "syntax_error", line: int(synErr.Pos.Line),
			detail: fmt.Sprintf("syntax error at %s: %s", synErr.Pos, synErr.Msg)}
	case errors.As(err, &resErr):
		e := resErr[0]
		return diagnosis{code: "runtime_error", line: int(e.Pos.Line), detail: fmt.Sprintf("%s: %s", e.Pos, e.Msg)}
	case errors.As(err, &evalErr):
		pos := lastPosition(evalErr.CallStack)
		d := diagnosis{code: "runtime_error", line: int(pos.Line), detail: fmt.Sprintf("%s: %s", pos, evalErr.Msg)}
		if errors.As(err, &argErr) {
			d.code = "invalid_arguments"
		}
		if len(evalErr.CallStack) > 2 {
			d.trace = evalErr.Backtrace()
		}
		return d
	}
	return diagnosis{code: "script_failed", detail: err.Error()}
}

// lastPosition is the innermost position in the script: a builtin's frame
// has no line of its own.
func lastPosition(stack starlark.CallStack) syntax.Position {
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].Pos.Line > 0 {
			return stack[i].Pos
		}
	}
	return syntax.Position{}
}

// scriptError is the result for a script that failed with err.
func (r *runner) scriptError(err error) core.ToolResult {
	d := diagnose(err)
	res := r.failure(d.code, d.detail, false)
	res.Data["line"] = d.line
	if d.line > 0 {
		res.Text = fmt.Sprintf("Script failed at line %d.\n", d.line) + res.Text
	}
	if d.trace != "" {
		res.Text += "\n\n" + d.trace
	}
	return res
}
