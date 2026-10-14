package landlock

import (
	"context"
	"sync"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// landlockSandbox is a landlock sandbox: attach's sandbox, the GitRepo slot,
// and what the init agent needs.
type landlockSandbox struct {
	inner     sandbox.Sandbox
	p         *provider
	h         *agentProc // the sandbox's agent
	dir, ws   string
	created   time.Time
	ttl       time.Duration
	agentPATH string // the PATH the sandbox's agent got

	// mu serialises Tools and Sync (read side) against an in-flight
	// GitRepo Sync (write side).
	mu sync.RWMutex
	// slotMu guards taken: a GitRepo is no longer accepted.
	slotMu sync.Mutex
	taken  bool
}

func (s *landlockSandbox) ID() string               { return s.inner.ID() }
func (s *landlockSandbox) Spec() sandbox.Spec       { return s.inner.Spec() }
func (s *landlockSandbox) Agent() sandbox.AgentInfo { return s.inner.Agent() }

// Sync forwards every source but a GitRepo to attach's sandbox; a successful
// one takes the GitRepo slot. A GitRepo is cloned by a confined init agent
// beside the idle sandbox agent, only as the first Sync and before the first
// Tools call; it reports a SyncResult of zeros.
func (s *landlockSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	if repo, ok := src.(sandbox.GitRepo); ok {
		return s.syncGitRepo(ctx, repo)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.inner.Sync(ctx, src)
	if err == nil {
		s.take()
	}
	return r, err
}

// Tools forwards to attach's sandbox; the first call takes the GitRepo slot
// whatever its outcome.
func (s *landlockSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.take()
	return s.inner.Tools(ctx, opts)
}

func (s *landlockSandbox) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	return s.inner.Export(ctx, path, dst)
}

func (s *landlockSandbox) Close(ctx context.Context) error { return s.inner.Close(ctx) }

// agentDone is closed once the sandbox's agent has exited.
func (s *landlockSandbox) agentDone() <-chan struct{} { return s.h.done }

func (s *landlockSandbox) take() {
	s.slotMu.Lock()
	s.taken = true
	s.slotMu.Unlock()
}

func (s *landlockSandbox) slotTaken() bool {
	s.slotMu.Lock()
	defer s.slotMu.Unlock()
	return s.taken
}
