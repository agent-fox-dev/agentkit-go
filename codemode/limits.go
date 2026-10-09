package codemode

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// The halts for the four limits (08-REQ-7). Each detail names the limit and
// its value, so the model knows what to change.

func (r *runner) stepHalt() *halt {
	return &halt{code: "step_limit_exceeded",
		detail: fmt.Sprintf("the script ran past its limit of %d execution steps (MaxSteps)", r.opts.MaxSteps)}
}

func (r *runner) callHalt() *halt {
	return &halt{code: "call_limit_exceeded",
		detail: fmt.Sprintf("the script tried to make more than %d tool calls (MaxCalls)", r.opts.MaxCalls)}
}

func (r *runner) outputHalt() *halt {
	return &halt{code: "output_limit_exceeded",
		detail: fmt.Sprintf("the script's output passed its limit of %d bytes (MaxOutputBytes)", r.opts.MaxOutputBytes)}
}

// ctxHalt is the halt for a context that ended: the caller cancelled
// (aborted), or MaxTimeout passed (timeout).
func (r *runner) ctxHalt() *halt {
	if r.parent.Err() == nil && errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		return &halt{code: "timeout",
			detail: fmt.Sprintf("the script ran past its %s time limit (MaxTimeout)", r.opts.MaxTimeout)}
	}
	return &halt{code: "aborted", detail: "Operation aborted"}
}

// takeCalls checks that n more nested calls fit in MaxCalls, counting the
// calls already made, and stops the script before any of them is made if
// they do not.
func (r *runner) takeCalls(n int) error {
	if len(r.ledger)+n > r.opts.MaxCalls {
		return r.stop(r.callHalt())
	}
	return nil
}

// failure is the result of a script that did not finish: the error, what it
// printed before it stopped and the calls it completed, whose side effects
// stand (08-REQ-7.5).
func (r *runner) failure(code, detail string, terminate bool) core.ToolResult {
	res := core.ErrResult(code, detail)
	res.Terminate = terminate
	printed := r.printed()
	res.Data = map[string]any{"error": code, "message": detail, "partial_output": printed, "calls_completed": r.ledgerData()}

	var b strings.Builder
	fmt.Fprintf(&b, "Script stopped (%s): %s", code, detail)
	if printed != "" {
		b.WriteString("\n\nOutput before it stopped:\n" + printed)
	}
	b.WriteString("\n\n" + r.ledgerText())
	res.Text = b.String()
	return res
}
