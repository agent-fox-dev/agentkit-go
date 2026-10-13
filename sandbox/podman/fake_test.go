package podman

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

// cmd is one short engine command the fake runner saw.
type cmd struct {
	argv     []string
	timeout  time.Duration
	deadline time.Time // the ctx deadline, zero when none
}

// fakeRunner is the runner seam for offline tests: it records every short
// command and answers it with answer, or with exit 0 and no output.
type fakeRunner struct {
	mu     sync.Mutex
	cmds   []cmd
	answer func(ctx context.Context, argv []string) (tools.ExecResult, error)
}

func (f *fakeRunner) run(ctx context.Context, argv []string, timeout time.Duration) (tools.ExecResult, error) {
	d, _ := ctx.Deadline()
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd{argv: slices.Clone(argv), timeout: timeout, deadline: d})
	answer := f.answer
	f.mu.Unlock()
	if answer != nil {
		return answer(ctx, argv)
	}
	return tools.ExecResult{Outcome: tools.OutcomeOK}, nil
}

// recorded is a copy of the commands seen so far.
func (f *fakeRunner) recorded() []cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

// exitResult is a short command's result with code and output.
func exitResult(code int, out string) tools.ExecResult {
	if code == 0 {
		return tools.ExecResult{Outcome: tools.OutcomeOK, Output: out}
	}
	return tools.ExecResult{Outcome: tools.OutcomeExit, ExitCode: code, Output: out}
}
