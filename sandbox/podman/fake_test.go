package podman

import (
	"context"
	"io"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// cmd is one engine command the fake runner saw: a short command, or the
// start of the engine process.
type cmd struct {
	argv     []string
	timeout  time.Duration
	deadline time.Time // the ctx deadline, zero when none
	start    bool
}

// fakeRunner is the runner seam for offline tests. It records every short
// command and answers it with answer, or with exit 0 and no output; it
// starts the engine process with startFn, by default an in-process agent
// served over pipes, as the local provider does.
type fakeRunner struct {
	t       *testing.T
	mu      sync.Mutex
	cmds    []cmd
	answer  func(ctx context.Context, argv []string) (tools.ExecResult, error)
	startFn func(argv []string) (process, error)
	procs   []*fakeProc
	// workdir and agentEnv configure the default in-process agent.
	workdir  string
	agentEnv []string
}

// newFake is a fake runner whose agent works in a fresh directory.
func newFake(t *testing.T) *fakeRunner {
	return &fakeRunner{t: t, workdir: t.TempDir(), agentEnv: []string{"PATH=/nonexistent"}}
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

func (f *fakeRunner) start(argv, env []string) (process, error) {
	f.mu.Lock()
	f.cmds = append(f.cmds, cmd{argv: slices.Clone(argv), start: true})
	fn := f.startFn
	f.mu.Unlock()
	if fn != nil {
		return fn(argv)
	}
	return f.serveAgent(false)
}

// recorded is a copy of the commands seen so far.
func (f *fakeRunner) recorded() []cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

// started is the engine-process command, or fails t.
func (f *fakeRunner) started() cmd {
	f.t.Helper()
	for _, c := range f.recorded() {
		if c.start {
			return c
		}
	}
	f.t.Fatal("no engine process was started")
	return cmd{}
}

// ran reports whether a recorded command's argv after the binary starts
// with args.
func (f *fakeRunner) ran(args ...string) bool {
	return f.index(0, args...) >= 0
}

// index is the position of the first recorded command at or after from
// whose argv after the binary starts with args, or -1.
func (f *fakeRunner) index(from int, args ...string) int {
	for i, c := range f.recorded() {
		if i >= from && hasArgs(c.argv, args...) {
			return i
		}
	}
	return -1
}

// count is how many recorded commands start with args.
func (f *fakeRunner) count(args ...string) int {
	n := 0
	for _, c := range f.recorded() {
		if hasArgs(c.argv, args...) {
			n++
		}
	}
	return n
}

// lastProc is the most recently started fake process.
func (f *fakeRunner) lastProc() *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.procs) == 0 {
		return nil
	}
	return f.procs[len(f.procs)-1]
}

// fullLandlock is the Landlock report of the default fake agent: ABI 4,
// which satisfies every network mode.
var fullLandlock = sandbox.LandlockInfo{ABI: 4, KernelABI: 4, TCP: true, Note: "fake"}

// serveAgent starts an in-process agent over two pipe pairs. It exits 0
// when the host closes its stdin, unless ignoreEOF, in which case only a
// group kill ends it.
func (f *fakeRunner) serveAgent(ignoreEOF bool) (process, error) {
	return f.serveAgentWith(ignoreEOF, fullLandlock)
}

// serveAgentWith is serveAgent with the agent's Landlock report.
func (f *fakeRunner) serveAgentWith(ignoreEOF bool, landlock sandbox.LandlockInfo) (process, error) {
	a, err := agent.New(agent.Config{Workdir: f.workdir, Env: f.agentEnv, TTL: time.Hour, Landlock: landlock})
	if err != nil {
		return nil, err
	}
	p := newFakeProc()
	go func() {
		_ = a.Serve(stdioconn.New(p.agentIn, p.agentOut))
		if !ignoreEOF {
			p.exit(0)
		}
	}()
	f.track(p)
	return p, nil
}

// exitWith starts a process that writes stderr and exits with code without
// serving anything.
func (f *fakeRunner) exitWith(code int, stderr string) (process, error) {
	p := newFakeProc()
	_, _ = p.stderr.Write([]byte(stderr))
	f.track(p)
	p.exit(code)
	return p, nil
}

// serveFakeAgent starts a process that relays its stdio to fa, and exits 0
// when the host closes its stdin.
func (f *fakeRunner) serveFakeAgent(fa *fakeagent.Agent) (process, error) {
	p := newFakeProc()
	conn := fa.Serve()
	go func() { _, _ = io.Copy(p.agentOut, conn); p.exit(0) }()
	go func() { _, _ = io.Copy(conn, p.agentIn); _ = conn.Close(); p.exit(0) }()
	f.track(p)
	return p, nil
}

// silent starts a process that never answers: it reads its stdin until the
// host closes it, then exits 0.
func (f *fakeRunner) silent() (process, error) {
	p := newFakeProc()
	go func() { _, _ = io.Copy(io.Discard, p.agentIn); p.exit(0) }()
	f.track(p)
	return p, nil
}

func (f *fakeRunner) track(p *fakeProc) {
	f.mu.Lock()
	f.procs = append(f.procs, p)
	f.mu.Unlock()
}

// fakeProc is a fake engine process: two pipe pairs standing for its stdin
// and stdout, an exit code, and a stderr tail.
type fakeProc struct {
	hostIn   io.WriteCloser // the host writes the process's stdin
	hostOut  io.ReadCloser  // the host reads the process's stdout
	agentIn  io.ReadCloser
	agentOut io.WriteCloser
	stderr   tailBuffer
	done     chan struct{}
	once     sync.Once
	code     atomic.Int64
	killed   atomic.Bool
	killAt   atomic.Int64 // UnixNano of the group kill
}

func newFakeProc() *fakeProc {
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	return &fakeProc{hostIn: inW, hostOut: outR, agentIn: inR, agentOut: outW, done: make(chan struct{})}
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.hostIn }
func (p *fakeProc) Stdout() io.ReadCloser { return p.hostOut }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) ExitCode() int         { return int(p.code.Load()) }
func (p *fakeProc) StderrTail() string    { return p.stderr.String() }
func (p *fakeProc) KillGroup() error {
	p.killed.Store(true)
	p.killAt.Store(time.Now().UnixNano())
	p.exit(137)
	return nil
}

// exit ends the process with code: its end of both pipes closes.
func (p *fakeProc) exit(code int) {
	p.once.Do(func() {
		p.code.Store(int64(code))
		_ = p.agentIn.Close()
		_ = p.agentOut.Close()
		close(p.done)
	})
}

// exitResult is a short command's result with code and output.
func exitResult(code int, out string) tools.ExecResult {
	if code == 0 {
		return tools.ExecResult{Outcome: tools.OutcomeOK, Output: out}
	}
	return tools.ExecResult{Outcome: tools.OutcomeExit, ExitCode: code, Output: out}
}

// hasArgs reports whether argv (after the binary) starts with prefix.
func hasArgs(argv []string, prefix ...string) bool {
	return len(argv) > len(prefix) && slices.Equal(argv[1:1+len(prefix)], prefix) || len(argv) == len(prefix)+1 && slices.Equal(argv[1:], prefix)
}

// testProvider is a podman provider over f that looks nothing up.
func testProvider(f *fakeRunner, o Options) *provider {
	if o.Engine == "" {
		o.Engine = EnginePodman
	}
	return newResolved(o, "/usr/bin/"+string(o.Engine), o.Engine, f)
}
