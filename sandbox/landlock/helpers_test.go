package landlock

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// newTestProvider is a provider built through newProvider with Linux seams,
// probe ABI 4, a temporary Root and a faked version run, so it works on
// every platform; mods adjust the seams.
func newTestProvider(t *testing.T, o Options, mods ...func(*seams)) *provider {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	if o.AgentBinary == "" {
		o.AgentBinary = placeholderAgent(t)
	}
	s := testSeams(4, nil)
	for _, m := range mods {
		m(&s)
	}
	p, err := newProvider(o, s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// creatorStart is the start time the fake table reports for this process.
const creatorStart = 4711

// startRec is what one start was given.
type startRec struct {
	args, env []string
	dir       string
}

// inProc is a starter that serves an in-process agent.New over two pipe
// pairs, with a test-chosen Landlock report, and registers a fake pid in a
// fake process table that leaves it when the agent ends.
type inProc struct {
	t       *testing.T
	table   *fakeTable
	mu      sync.Mutex
	recs    []startRec
	nextPID int
	// report is the Landlock report for the sandbox directory dir.
	report func(dir string) sandbox.LandlockInfo
	// before runs before each start; pid, when set, chooses the agent's pid.
	before func(req startReq)
	pid    func() int
	// fail makes every start fail.
	fail error
}

func newInProc(t *testing.T) *inProc {
	ft := newFakeTable()
	ft.add(procEntry{PID: os.Getpid(), Session: os.Getpid(), State: 'S', Start: creatorStart})
	return &inProc{t: t, table: ft, nextPID: 40000, report: goodReport}
}

// goodReport is a report that passes every check for dir.
func goodReport(dir string) sandbox.LandlockInfo {
	var w []string
	for _, sub := range []string{"workspace", "home", "tmp"} {
		r, err := filepath.EvalSymlinks(filepath.Join(dir, sub))
		if err != nil {
			r = filepath.Join(dir, sub)
		}
		w = append(w, r)
	}
	return sandbox.LandlockInfo{ABI: 4, KernelABI: 6, TCP: true, Note: "fake", Writable: append(w, "/dev/null"), ReadOnly: []string{"/usr", "/proc"}}
}

// with installs f as p's starter and process table.
func (f *inProc) with(s *seams) { s.start, s.procs = f.start, f.table }

// starts is a copy of the recorded starts.
func (f *inProc) starts() []startRec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.recs)
}

func (f *inProc) start(req startReq) (*agentProc, net.Conn, error) {
	f.mu.Lock()
	f.recs = append(f.recs, startRec{args: slices.Clone(req.args), env: slices.Clone(req.env), dir: req.dir})
	pid := f.nextPID
	f.nextPID++
	f.mu.Unlock()
	if f.before != nil {
		f.before(req)
	}
	if f.fail != nil {
		return nil, nil, f.fail
	}
	if f.pid != nil {
		pid = f.pid()
	}
	ws := argAfter(req.args, "--workdir")
	a, err := agent.New(agent.Config{Workdir: ws, Env: req.env, TTL: time.Hour, Landlock: f.report(filepath.Dir(ws))})
	if err != nil {
		return nil, nil, err
	}
	r1, w1, _ := os.Pipe()
	r2, w2, _ := os.Pipe()
	host, agentConn := stdioconn.New(r2, w1), stdioconn.New(r1, w2)
	f.table.add(procEntry{PID: pid, Session: pid, State: 'S', Start: uint64(pid) * 10})
	h := newAgentProc(pid, func() error { return agentConn.Close() })
	go func() {
		_ = a.Serve(agentConn)
		_ = agentConn.Close()
		f.table.mu.Lock()
		delete(f.table.procs, pid)
		f.table.mu.Unlock()
		h.setExit(0, false, 0)
	}()
	return h, host, nil
}

// argAfter is the value after flag in args.
func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// entries is the names in dir.
func entries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}
