package landlock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// fakeTable is a fake process table: processes by pid, a boot id, and the
// kills it received. A kill removes its target unless it is immortal.
type fakeTable struct {
	mu       sync.Mutex
	procs    map[int]procEntry
	boot     string
	kills    []string
	immortal map[int]bool
	scans    int
	listErr  error
}

func newFakeTable() *fakeTable {
	return &fakeTable{procs: map[int]procEntry{}, boot: "B1", immortal: map[int]bool{}}
}

func (f *fakeTable) add(e procEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.procs[e.PID] = e
}

func (f *fakeTable) list() ([]procEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scans++
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []procEntry
	for _, e := range f.procs {
		out = append(out, e)
	}
	return out, nil
}

func (f *fakeTable) lookup(pid int) (uint64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.procs[pid]
	return e.Start, ok, nil
}

func (f *fakeTable) bootID() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.boot, nil
}

func (f *fakeTable) kill(pid int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, fmt.Sprintf("%d/%d", pid, sig))
	if !f.immortal[pid] {
		delete(f.procs, pid)
	}
	return nil
}

func (f *fakeTable) killed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.kills)
}

// TS-18-44: the session kill SIGKILLs every non-zombie process in the
// agent's session, rescans until none remain, and names survivors after the
// wait. 18-REQ-7.3.
func TestSessionKill_TS18_44(t *testing.T) {
	ft := newFakeTable()
	ft.add(procEntry{PID: 101, Session: 100, State: 'S'})
	ft.add(procEntry{PID: 102, Session: 100, State: 'Z'})
	ft.add(procEntry{PID: 103, Session: 200, State: 'S'})
	ft.add(procEntry{PID: 104, Session: 100, State: 'R'})
	if err := sessionKill(context.Background(), ft, 100, sessionWait); err != nil {
		t.Fatal(err)
	}
	got := ft.killed()
	slices.Sort(got)
	k := int(syscall.SIGKILL)
	if want := []string{fmt.Sprintf("101/%d", k), fmt.Sprintf("104/%d", k)}; !slices.Equal(got, want) || ft.scans < 2 {
		t.Errorf("kills %v after %d scans, want %v after at least 2", got, ft.scans, want)
	}

	ft2 := newFakeTable()
	ft2.add(procEntry{PID: 105, Session: 100, State: 'S'})
	ft2.immortal[105] = true
	start := time.Now()
	err := sessionKill(context.Background(), ft2, 100, sessionWait)
	if d := time.Since(start); err == nil || !strings.Contains(err.Error(), "105") || d < 5*time.Second || d > 7*time.Second {
		t.Errorf("a survivor: %v after %v, want an error naming 105 after about 5s", err, d)
	}
}

// TS-18-42: the destroy step runs once, performs its three actions in order
// even after a failure, joins their errors and fits the 30-second budget.
// 18-REQ-7.1.
func TestDestroyStep_TS18_42(t *testing.T) {
	ctx := context.Background()
	f := newInProc(t)
	var mu sync.Mutex
	var order []string
	record := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	ot := &orderTable{fakeTable: f.table, record: record}
	var removes atomic.Int32
	p := newTestProvider(t, Options{}, f.with, func(s *seams) {
		s.procs = ot
		s.remove = func(dir string) error { removes.Add(1); record("remove"); return removeTree(dir) }
	})
	sb, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	h := sb.(*landlockSandbox)
	go func() { <-h.agentDone(); record("stop") }()
	if err := sb.Close(ctx); err != nil {
		t.Errorf("Close = %v", err)
	}
	_ = sb.Close(ctx)
	mu.Lock()
	got := slices.Clone(order)
	mu.Unlock()
	if want := []string{"stop", "session", "remove"}; !slices.Equal(compact(got), want) || removes.Load() != 1 {
		t.Errorf("order %v after %d removals, want %v once", got, removes.Load(), want)
	}

	errWait, errScan := errors.New("wait failed"), errors.New("scan failed")
	oldStop, oldSession := stopWait, sessionWait
	stopWait, sessionWait = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { stopWait, sessionWait = oldStop, oldSession })
	f2 := newInProc(t)
	f2.ignoreEOF, f2.killErr = true, errWait
	p2 := newTestProvider(t, Options{}, f2.with)
	sb2, err := p2.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	f2.table.listErr = errScan
	dir := filepath.Join(p2.root, sb2.ID())
	err = sb2.Close(ctx)
	if !errors.Is(err, errWait) || !errors.Is(err, errScan) {
		t.Errorf("failing Close = %v, want both errors joined", err)
	}
	if _, serr := os.Stat(dir); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("the directory remains after a failing destroy: %v", serr)
	}
	stopWait, sessionWait = oldStop, oldSession
	if stopWait != 5*time.Second || sessionWait != 5*time.Second || stopWait+sessionWait >= 30*time.Second {
		t.Errorf("stopWait %v, sessionWait %v", stopWait, sessionWait)
	}
}

// orderTable records "session" on each scan.
type orderTable struct {
	*fakeTable
	record func(string)
}

func (o *orderTable) list() ([]procEntry, error) { o.record("session"); return o.fakeTable.list() }

// compact drops adjacent repeats.
func compact(s []string) []string { return slices.Compact(slices.Clone(s)) }
