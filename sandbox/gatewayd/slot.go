package gatewayd

import (
	"context"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// slotTaken is the refusal of a GitRepo that is no longer the first Sync.
const slotTaken = "GitRepo must be the first Sync and come before Tools"

// slot is a sandbox's GitRepo slot, kept by the gateway with the backends'
// rules because relayed RPCs bypass the backend wrapper's own bookkeeping:
// a successful SyncGit, the first relayed Tools, a relayed SyncTar that
// succeeds and a relayed SYNC Rsync that ends OK each take it. While it is
// free, SyncGit holds the write lock and the slot-taking relays the read
// lock, so the two never interleave.
type slot struct {
	mu    sync.RWMutex
	taken atomic.Bool
}

// hold takes the read lock while the slot is free and returns its release;
// once the slot is taken there is nothing to serialise against.
func (s *slot) hold() (release func()) {
	if s.taken.Load() {
		return func() {}
	}
	s.mu.RLock()
	return s.mu.RUnlock
}

// take marks the slot taken.
func (s *slot) take() { s.taken.Store(true) }

// SyncGit clones a repository into the caller's sandbox with the backend's
// init step, as the sandbox's first Sync and before Tools. An unknown or
// foreign id is answered as the relay answers it.
func (g *gatewayService) SyncGit(ctx context.Context, req *sandboxpb.SyncGitRequest) (*sandboxpb.SyncGitResponse, error) {
	e, err := g.s.reg.access(ownerOf(ctx), req.GetId())
	if err != nil {
		return nil, err
	}
	e.slot.mu.Lock()
	defer e.slot.mu.Unlock()
	if e.slot.taken.Load() {
		return &sandboxpb.SyncGitResponse{Refusal: &sandboxpb.Refusal{Kind: sandboxpb.Refusal_REFUSED, Message: slotTaken}}, nil
	}
	repo := sandbox.GitRepo{URL: req.GetUrl(), Ref: req.GetRef(), Depth: int(req.GetDepth())}
	if _, err := e.sb.Sync(ctx, repo); err != nil {
		if ctx.Err() != nil {
			return nil, status.FromContextError(ctx.Err()).Err()
		}
		// ErrUnsupported is UNSUPPORTED, as from Create; anything else is
		// the clone's own failure.
		r := backendRefusal(err)
		if r == nil || r.Kind != sandboxpb.Refusal_UNSUPPORTED {
			r = &sandboxpb.Refusal{Kind: sandboxpb.Refusal_REFUSED, Message: err.Error()}
		}
		return &sandboxpb.SyncGitResponse{Refusal: r}, nil
	}
	e.slot.take()
	return &sandboxpb.SyncGitResponse{}, nil
}

// unavailable is the status a gateway-side condition is answered with.
func unavailable(format string, args ...any) error {
	return status.Errorf(codes.Unavailable, format, args...)
}
