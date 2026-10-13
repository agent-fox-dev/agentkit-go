package podman

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// podSandbox is a podman sandbox: the attached sandbox, plus the GitRepo
// slot and what the init step needs. Every method but a GitRepo Sync
// forwards to the attached sandbox.
type podSandbox struct {
	inner   sandbox.Sandbox
	p       *provider
	id      string
	created time.Time
	eff     sandbox.Spec // the effective spec, Workdir as configured

	// mu serialises Tools and Sync (read side) against an in-flight GitRepo
	// Sync (write side).
	mu sync.RWMutex
	// slotTaken: a GitRepo is no longer accepted, because something has
	// written to the workspace or the tools have been configured.
	slotTaken atomic.Bool
}

func (s *podSandbox) ID() string { return s.inner.ID() }

// Spec is the attached sandbox's spec with the effective TTL (attach's own
// timer was given what was left of it after Create).
func (s *podSandbox) Spec() sandbox.Spec {
	sp := s.inner.Spec()
	sp.TTL = s.eff.TTL
	return sp
}
func (s *podSandbox) Agent() sandbox.AgentInfo { return s.inner.Agent() }

// Sync forwards every source but a GitRepo to the attached sandbox; a
// successful one takes the GitRepo slot. A GitRepo is cloned by the init
// step beside the idle agent: it is accepted only as the first Sync and
// before the first Tools call, reports a SyncResult of zeros, and the
// clone's files are visible to the tools under Workdir.
func (s *podSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	if repo, ok := src.(sandbox.GitRepo); ok {
		return s.syncGitRepo(ctx, repo)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.inner.Sync(ctx, src)
	if err == nil {
		s.slotTaken.Store(true)
	}
	return r, err
}

// Tools forwards to the attached sandbox; the first call takes the GitRepo
// slot whatever its outcome.
func (s *podSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.slotTaken.Store(true)
	return s.inner.Tools(ctx, opts)
}

func (s *podSandbox) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	return s.inner.Export(ctx, path, dst)
}

func (s *podSandbox) Close(ctx context.Context) error { return s.inner.Close(ctx) }
