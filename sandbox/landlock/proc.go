package landlock

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	// stopWait is how long the destroy step waits for the agent to exit on
	// EOF before killing its process group.
	stopWait = 5 * time.Second
	// sessionWait bounds the session kill's rescans.
	sessionWait = 5 * time.Second
)

// killWait is how long a killed agent is waited for.
const killWait = 2 * time.Second

// procEntry is one process: its pid, session id (/proc/<pid>/stat field 6),
// state (field 3, 'Z' for a zombie) and start time (field 22).
type procEntry struct {
	PID, Session int
	State        byte
	Start        uint64
}

// procTable is the process table the destroy step and Reap read: the seam
// offline tests replace.
type procTable interface {
	list() ([]procEntry, error)
	// lookup is pid's start time, and whether it exists.
	lookup(pid int) (start uint64, ok bool, err error)
	bootID() (string, error)
	kill(pid int, sig syscall.Signal) error
}

// agentProc is a started agent: its pid, its exit once recorded, its stderr
// tail, and how to kill its process group.
type agentProc struct {
	pid    int
	done   chan struct{}
	tail   *tailBuffer
	killFn func() error

	// killed is set when the provider kills the agent's group.
	killed atomic.Bool

	once     sync.Once
	mu       sync.Mutex
	code     int
	signaled bool
	sig      syscall.Signal
}

func newAgentProc(pid int, kill func() error) *agentProc {
	return &agentProc{pid: pid, done: make(chan struct{}), tail: &tailBuffer{}, killFn: kill}
}

// setExit records the agent's exit, once.
func (h *agentProc) setExit(code int, signaled bool, sig syscall.Signal) {
	h.once.Do(func() {
		h.mu.Lock()
		h.code, h.signaled, h.sig = code, signaled, sig
		h.mu.Unlock()
		close(h.done)
	})
}

// exit is the recorded exit status: the code, or the signal that ended it.
func (h *agentProc) exit() (code int, signaled bool, sig syscall.Signal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.code, h.signaled, h.sig
}

// exitedOnItsOwn reports whether the agent has exited and the provider did
// not cause it: a non-zero status or a signal it did not send. An agent
// exits 0 on EOF, which is how the destroy step stops it.
func (h *agentProc) exitedOnItsOwn() bool {
	select {
	case <-h.done:
	default:
		return false
	}
	code, signaled, _ := h.exit()
	return !h.killed.Load() && (code != 0 || signaled)
}

// stopAgent waits up to wait for h to exit (its connection is closed, and the
// agent exits 0 on EOF), then kills its process group and waits for that.
func stopAgent(h *agentProc, wait time.Duration) error {
	select {
	case <-h.done:
		return nil
	case <-time.After(wait):
	}
	h.killed.Store(true)
	err := h.killFn()
	select {
	case <-h.done:
	case <-time.After(killWait):
		err = fmtJoin(err, fmt.Errorf("landlock: agent %d did not exit after SIGKILL", h.pid))
	}
	if err != nil {
		return fmt.Errorf("landlock: stopping agent %d: %w", h.pid, err)
	}
	return nil
}

// sessionKill SIGKILLs every non-zombie process whose session id is sid,
// rescanning until a scan finds none; after wait it returns an error naming
// the survivors. It reaches what a tool left running in its own process
// group, which stays in the agent's session.
func sessionKill(ctx context.Context, pt procTable, sid int, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		procs, err := pt.list()
		var alive []int
		for _, e := range procs {
			if e.Session == sid && e.State != 'Z' {
				alive = append(alive, e.PID)
			}
		}
		if err == nil && len(alive) == 0 {
			return nil
		}
		for _, pid := range alive {
			_ = pt.kill(pid, syscall.SIGKILL)
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			if err != nil {
				return fmt.Errorf("landlock: killing session %d: %w", sid, err)
			}
			slices.Sort(alive)
			return fmt.Errorf("landlock: processes of session %d survived SIGKILL: %s", sid, strings.Trim(fmt.Sprint(alive), "[]"))
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// fmtJoin joins two errors, either of which may be nil.
func fmtJoin(a, b error) error {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	return fmt.Errorf("%w; %w", a, b)
}
