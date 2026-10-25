package podman

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// cmd is a recorded engine command: a short one, or the engine process.
type cmd struct {
	argv     []string
	timeout  time.Duration
	deadline time.Time
	start    bool
}

type answerFunc func(ctx context.Context, argv []string) (tools.ExecResult, error)

// fakeRunner answers with answer (exit 0) and starts startFn (an agent in workdir).
type fakeRunner struct {
	mu       sync.Mutex
	cmds     []cmd
	procs    []*fakeProc
	answer   answerFunc
	startFn  func() (process, error)
	workdir  string
	agentEnv []string
}

func newFake(t *testing.T) *fakeRunner {
	return &fakeRunner{workdir: t.TempDir(), agentEnv: []string{"PATH=/nonexistent"}}
}

func (f *fakeRunner) record(c cmd) (answerFunc, func() (process, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, c)
	return f.answer, f.startFn
}

func (f *fakeRunner) run(ctx context.Context, argv []string, timeout time.Duration) (tools.ExecResult, error) {
	d, _ := ctx.Deadline()
	if answer, _ := f.record(cmd{argv: slices.Clone(argv), timeout: timeout, deadline: d}); answer != nil {
		return answer(ctx, argv)
	}
	return exitResult(0, ""), nil
}

func (f *fakeRunner) start(argv, _ []string) (process, error) {
	if _, fn := f.record(cmd{argv: slices.Clone(argv), start: true}); fn != nil {
		return fn()
	}
	return f.serveAgent(false, sandbox.LandlockInfo{ABI: 4, KernelABI: 4, TCP: true})
}

func (f *fakeRunner) recorded() []cmd { f.mu.Lock(); defer f.mu.Unlock(); return slices.Clone(f.cmds) }

// index is the first command at or after from that starts with args, or -1.
func (f *fakeRunner) index(from int, args ...string) int {
	for i, c := range f.recorded() {
		if i >= from && hasArgs(c.argv, args...) {
			return i
		}
	}
	return -1
}

func (f *fakeRunner) ran(args ...string) bool { return f.index(0, args...) >= 0 }

func (f *fakeRunner) count(args ...string) int {
	n := 0
	for i := f.index(0, args...); i >= 0; i = f.index(i+1, args...) {
		n++
	}
	return n
}

func (f *fakeRunner) lastProc() *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[len(f.procs)-1]
}

// spawn is a new fake process, with body running beside it.
func (f *fakeRunner) spawn(body func(p *fakeProc)) (process, error) {
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	p := &fakeProc{hostIn: inW, hostOut: outR, agentIn: inR, agentOut: outW, done: make(chan struct{})}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs = append(f.procs, p)
	go body(p)
	return p, nil
}

// serveAgent is an in-process agent; with ignoreEOF only a group kill ends it.
func (f *fakeRunner) serveAgent(ignoreEOF bool, landlock sandbox.LandlockInfo) (process, error) {
	a, err := agent.New(agent.Config{Workdir: f.workdir, Env: f.agentEnv, TTL: time.Hour, Landlock: landlock})
	if err != nil {
		return nil, err
	}
	return f.spawn(func(p *fakeProc) {
		_ = a.Serve(stdioconn.New(p.agentIn, p.agentOut))
		if !ignoreEOF {
			p.exit(0)
		}
	})
}

// exitWith is a process that writes stderr and exits with code.
func (f *fakeRunner) exitWith(code int, stderr string) (process, error) {
	return f.spawn(func(p *fakeProc) { _, _ = p.stderr.Write([]byte(stderr)); p.exit(code) })
}

// serveFakeAgent is a process relaying its stdio to fa.
func (f *fakeRunner) serveFakeAgent(fa *fakeagent.Agent) (process, error) {
	return f.spawn(func(p *fakeProc) {
		conn := fa.Serve()
		go func() { _, _ = io.Copy(p.agentOut, conn); p.exit(0) }()
		_, _ = io.Copy(conn, p.agentIn)
		_ = conn.Close()
		p.exit(0)
	})
}

// fakeProc is a fake engine process over two pipes.
type fakeProc struct {
	hostIn, agentOut io.WriteCloser
	hostOut, agentIn io.ReadCloser
	stderr           tailBuffer
	done             chan struct{}
	once             sync.Once
	code             atomic.Int64
	killAt           atomic.Int64 // UnixNano of the group kill, 0 before
}

func (p *fakeProc) Stdin() io.WriteCloser { return p.hostIn }
func (p *fakeProc) Stdout() io.ReadCloser { return p.hostOut }
func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) ExitCode() int         { return int(p.code.Load()) }
func (p *fakeProc) StderrTail() string    { return p.stderr.String() }
func (p *fakeProc) KillGroup() error {
	p.killAt.Store(time.Now().UnixNano())
	p.exit(137)
	return nil
}

func (p *fakeProc) exit(code int) {
	p.once.Do(func() {
		p.code.Store(int64(code))
		_, _ = p.agentIn.Close(), p.agentOut.Close()
		close(p.done)
	})
}

func exitResult(code int, out string) tools.ExecResult {
	if code == 0 {
		return tools.ExecResult{Outcome: tools.OutcomeOK, Output: out}
	}
	return tools.ExecResult{Outcome: tools.OutcomeExit, ExitCode: code, Output: out}
}

// hasArgs reports whether argv after the binary starts with prefix.
func hasArgs(argv []string, prefix ...string) bool {
	return len(argv) > len(prefix) && slices.Equal(argv[1:1+len(prefix)], prefix)
}

// testProvider is a provider over f that looks nothing up; podman by default.
func testProvider(f *fakeRunner, o Options) *provider {
	if o.Engine == "" {
		o.Engine = EnginePodman
	}
	return newResolved(o, "/usr/bin/"+string(o.Engine), o.Engine, f)
}

// create is a sandbox of img:1 over f, closed when the test ends.
func create(t *testing.T, f *fakeRunner, spec sandbox.Spec) sandbox.Sandbox {
	t.Helper()
	spec.Image = "img:1"
	s, err := testProvider(f, Options{}).Create(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// withGit gives f's agent a stub git and a shell on its PATH.
func withGit(t *testing.T, f *fakeRunner) *fakeRunner {
	f.agentEnv = []string{"PATH=" + stubBinary(t, "git") + ":/usr/bin:/bin"}
	return f
}

// stubBinary is a fresh directory holding an executable name that exits 0.
func stubBinary(t *testing.T, name string) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// volumeID is the sandbox id of the volume f saw created.
func volumeID(f *fakeRunner) string {
	c := f.recorded()[f.index(0, "volume", "create")]
	return strings.TrimPrefix(c.argv[len(c.argv)-1], "agentkit-")
}

// createdAt is the creation time behind argv's expiry label for ttl.
func createdAt(t *testing.T, argv []string, ttl time.Duration) time.Time {
	t.Helper()
	i := slices.IndexFunc(argv, func(e string) bool { return strings.HasPrefix(e, labelExpires+"=") })
	exp, err := time.Parse(time.RFC3339, strings.TrimPrefix(argv[max(i, 0)], labelExpires+"="))
	if err != nil {
		t.Fatalf("no expiry label in %q: %v", argv, err)
	}
	return exp.Add(-ttl)
}

func callTool(t *testing.T, ts []core.Tool, name string, input map[string]any) core.ToolResult {
	t.Helper()
	b, _ := json.Marshal(input)
	if i := slices.IndexFunc(ts, func(tl core.Tool) bool { return tl.Name == name }); i >= 0 {
		return ts[i].Execute(context.Background(), b)
	}
	t.Fatalf("no tool %s", name)
	return core.ToolResult{}
}
