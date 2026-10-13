package podman

import (
	"context"
	"time"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

// runner is the provider's command runner: the seam offline tests replace
// with a fake that records argv.
type runner interface {
	// run runs a short engine command to completion. argv[0] is the
	// resolved binary.
	run(ctx context.Context, argv []string, timeout time.Duration) (tools.ExecResult, error)
}

// outputTail bounds the output a short engine command keeps: its tail.
const outputTail = 64 << 10

// realRunner runs engine commands with the engine's environment.
type realRunner struct {
	env []string
}

func (r realRunner) run(ctx context.Context, argv []string, timeout time.Duration) (tools.ExecResult, error) {
	return tools.RunArgv(ctx, argv, tools.ExecOptions{Env: r.env, Timeout: timeout, MaxBytes: outputTail})
}
