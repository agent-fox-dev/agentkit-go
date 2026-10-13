package podman

import (
	"context"
	"io"
	"os"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/tools"
)

// runner is the provider's command runner: the seam offline tests replace
// with a fake that records argv and serves an in-process agent.
type runner interface {
	// run runs a short engine command to completion. argv[0] is the
	// resolved binary.
	run(ctx context.Context, argv []string, timeout time.Duration) (tools.ExecResult, error)
	// start starts the long-lived engine process with env.
	start(argv, env []string) (process, error)
}

// process is the long-lived engine process whose stdin and stdout carry the
// connection to the agent.
type process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	// Done is closed once the process has exited.
	Done() <-chan struct{}
	// ExitCode is the exit status once Done is closed; -1 for a signal.
	ExitCode() int
	// StderrTail is the last stderrTail bytes the process wrote to stderr.
	StderrTail() string
	// KillGroup kills the process's group (the process alone on Windows).
	KillGroup() error
}

const (
	// outputTail bounds the output a short engine command keeps: its tail.
	outputTail = 64 << 10
	// shortTimeout bounds every short engine command.
	shortTimeout = 2 * time.Minute
	// stderrTail is how much of the engine process's stderr is kept.
	stderrTail = 8 << 10
)

// shortOptions are the options of every short engine command: the engine's
// environment (os.Environ() for nil), a 2-minute timeout and a bounded tail.
func shortOptions(env []string) tools.ExecOptions {
	if env == nil {
		env = os.Environ()
	}
	return tools.ExecOptions{Env: env, Timeout: shortTimeout, MaxBytes: outputTail}
}

// realRunner runs engine commands with the engine's environment.
type realRunner struct {
	env []string
}

func (r realRunner) run(ctx context.Context, argv []string, timeout time.Duration) (tools.ExecResult, error) {
	opts := shortOptions(r.env)
	opts.Timeout = timeout
	return tools.RunArgv(ctx, argv, opts)
}

func (r realRunner) start(argv, env []string) (process, error) { return startProc(argv, env) }

// tailBuffer is an io.Writer that keeps the last stderrTail bytes written.
type tailBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if over := len(t.b) - stderrTail; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
