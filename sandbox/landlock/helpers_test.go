package landlock

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// agentBin and dialtcpBin are built for the Linux tests; children are what a
// re-executed test binary runs, by the name in childVar.
var (
	agentBin, dialtcpBin string
	children             = map[string]func() error{}
)

const childVar, childAgentVar = "AGENTKIT_LANDLOCK_CHILD", "AGENTKIT_LANDLOCK_AGENT"

func TestMain(m *testing.M) {
	if name := os.Getenv(childVar); name != "" {
		agentBin = os.Getenv(childAgentVar)
		if err := children[name](); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if runtime.GOOS != "linux" {
		os.Exit(m.Run())
	}
	dir, _ := os.MkdirTemp("", "landlock-test-")
	agentBin, dialtcpBin = filepath.Join(dir, "agentkit-sandboxd"), filepath.Join(dir, "dialtcp", "dialtcp")
	for out, pkg := range map[string]string{agentBin: "./cmd/agentkit-sandboxd", dialtcpBin: "./cmd/agentkit-sandboxd/testdata/dialtcp"} {
		cmd := exec.Command("go", "build", "-o", out, pkg)
		cmd.Dir, cmd.Env = "..", append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "building %s: %v\n%s", pkg, err, b)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// testSeams are Linux seams probing abi, with no agent beside the executable
// or on PATH and a version run that answers protocol 1.2, counting runs.
func testSeams(abi int, versionRuns *int) seams {
	return seams{goos: "linux", probe: func() (int, error) { return abi, nil },
		runVersion: func(string) (string, error) {
			if versionRuns != nil {
				*versionRuns++
			}
			return "agentkit-sandboxd v0.0.0-test (protocol 1.2)\n", nil
		},
		executable: func() (string, error) { return "/nonexistent/test.binary", nil },
		lookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		resolver:   func(string) string { return "" }, remove: removeTree, initCommand: defaultInitCommand,
	}
}

// newTestProvider is newProvider over testSeams(4) with a temporary Root
// and a placeholder agent unless o sets them; mods adjust the seams.
func newTestProvider(t *testing.T, o Options, mods ...func(*seams)) *provider {
	t.Helper()
	if o.Root == "" {
		o.Root = t.TempDir()
	}
	if o.AgentBinary == "" {
		o.AgentBinary = script(t, t.TempDir(), "agentkit-sandboxd", "exit 0")
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

// script writes an executable shell script holding body.
func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeTable is a fake process table on boot B1. A kill removes its target
// unless immortal; listErr fails each scan; anyPID makes lookup find any pid.
type fakeTable struct {
	mu       sync.Mutex
	procs    map[int]procEntry
	kills    []int
	immortal map[int]bool
	scans    int
	listErr  error
	anyPID   bool
}

func newFakeTable() *fakeTable {
	return &fakeTable{procs: map[int]procEntry{}, immortal: map[int]bool{}}
}

func (f *fakeTable) add(es ...procEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range es {
		f.procs[e.PID] = e
	}
}

func (f *fakeTable) forget(pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.procs, pid)
}

func (f *fakeTable) list() ([]procEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans++
	var out []procEntry
	for _, e := range f.procs {
		out = append(out, e)
	}
	return out, f.listErr
}

func (f *fakeTable) lookup(pid int) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.procs[pid]; ok || !f.anyPID {
		return e.Start, ok, nil
	}
	return uint64(pid) * 10, true, nil
}

func (f *fakeTable) bootID() (string, error) { return "B1", nil }

func (f *fakeTable) kill(pid int, _ syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, pid)
	if !f.immortal[pid] {
		delete(f.procs, pid)
	}
	return nil
}

// alive is whether pid is in the table.
func (f *fakeTable) alive(pid int) bool { _, ok, _ := f.lookup(pid); return ok }

// creatorStart is the start time the fake table gives this process.
const creatorStart = 4711

// inProc starts each agent in process: an agent.New served over pipes with
// the Landlock report report(<sandbox dir>), under a fake pid (from 40000,
// start time pid*10, its own session) that leaves the table when it ends.
type inProc struct {
	table  *fakeTable
	mu     sync.Mutex
	recs   []startReq
	pid    int
	report func(dir string) sandbox.LandlockInfo
	// before runs first in each start; fail makes the start fail.
	before func(req startReq)
	fail   error
}

func newInProc() *inProc {
	ft := newFakeTable()
	ft.add(procEntry{PID: os.Getpid(), Session: os.Getpid(), State: 'S', Start: creatorStart})
	return &inProc{table: ft, pid: 40000, report: goodReport}
}

// with installs f as the starter and process table.
func (f *inProc) with(s *seams) { s.start, s.procs = f.start, f.table }

// starts is a copy of the starts so far.
func (f *inProc) starts() []startReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.recs)
}

// register records req and adds the next fake pid to the table.
func (f *inProc) register(req startReq) int {
	f.mu.Lock()
	f.recs = append(f.recs, req)
	pid := f.pid
	f.pid++
	f.mu.Unlock()
	f.table.add(procEntry{PID: pid, Session: pid, State: 'S', Start: uint64(pid) * 10})
	return pid
}

// exited ends h's fake process.
func (f *inProc) exited(h *agentProc) { f.table.forget(h.pid); h.setExit(0, false, 0) }

func (f *inProc) start(req startReq) (*agentProc, net.Conn, error) {
	if f.before != nil {
		f.before(req)
	}
	pid := f.register(req)
	if f.fail != nil {
		f.table.forget(pid)
		return nil, nil, f.fail
	}
	ws := argAfter(req.args, "--workdir")
	a, err := agent.New(agent.Config{Workdir: ws, Env: req.env, TTL: time.Hour, Landlock: f.report(filepath.Dir(ws))})
	if err != nil {
		return nil, nil, err
	}
	r1, w1, _ := os.Pipe()
	r2, w2, _ := os.Pipe()
	agentConn := stdioconn.New(r1, w2)
	h := newAgentProc(pid, agentConn.Close)
	go func() { _ = a.Serve(agentConn); _ = agentConn.Close(); f.exited(h) }()
	return h, stdioconn.New(r2, w1), nil
}

// fakeStarter starts fa, over net.Pipe, as the agent.
func (f *inProc) fakeStarter(fa *fakeagent.Agent) func(startReq) (*agentProc, net.Conn, error) {
	return func(req startReq) (*agentProc, net.Conn, error) {
		h := newAgentProc(f.register(req), func() error { fa.Stop(); return nil })
		conn := fa.Serve()
		go func() { <-fa.Closed(); fa.Stop(); f.exited(h) }()
		return h, conn, nil
	}
}

// silentStarter starts an agent that never answers Hello, closing entered
// once started.
func (f *inProc) silentStarter(entered chan<- struct{}) func(startReq) (*agentProc, net.Conn, error) {
	return func(req startReq) (*agentProc, net.Conn, error) {
		r1, w1, _ := os.Pipe()
		r2, w2, _ := os.Pipe()
		h := newAgentProc(f.register(req), r1.Close)
		go func() { close(entered); _, _ = io.Copy(io.Discard, r1); _ = w2.Close(); f.exited(h) }()
		return h, stdioconn.New(r2, w1), nil
	}
}

// goodReport is a Landlock report that passes every check for dir, which
// is beneath the resolved Root.
func goodReport(dir string) sandbox.LandlockInfo {
	return sandbox.LandlockInfo{ABI: 4, KernelABI: 6, TCP: true, Note: "fake",
		Writable: []string{dir + "/workspace", dir + "/home", dir + "/tmp", "/dev/null"}, ReadOnly: []string{"/usr", "/proc"}}
}

// reportWith is goodReport with change applied.
func reportWith(change func(*sandbox.LandlockInfo)) func(string) sandbox.LandlockInfo {
	return func(dir string) sandbox.LandlockInfo { r := goodReport(dir); change(&r); return r }
}

// argAfter is the value after flag in args.
func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// entries is the sorted names in dir; none when it is missing.
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
	return names
}

func evalSymlinks(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
