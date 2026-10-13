package podman

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// errSlot is the refusal of a GitRepo once the slot is taken.
var errSlot = errors.New("podman: GitRepo must be the first Sync and come before Tools")

// syncGitRepo clones repo into the workspace volume with the init step. It
// holds the write lock for its whole duration, so it waits for in-flight
// Tools and Sync calls and holds new ones. Before any engine command it
// checks, in order, that the sandbox is live, that gitclone.Check accepts
// repo, that the slot is free, and that the agent reported git on its PATH;
// none of these takes the slot. Only a successful clone does.
func (s *podSandbox) syncGitRepo(ctx context.Context, repo sandbox.GitRepo) (sandbox.SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := attach.Live(s.inner); err != nil {
		return sandbox.SyncResult{}, err
	}
	if err := gitclone.Check(repo); err != nil {
		return sandbox.SyncResult{}, err
	}
	if s.slotTaken.Load() {
		return sandbox.SyncResult{}, errSlot
	}
	if !s.inner.Agent().Git {
		return sandbox.SyncResult{}, errors.New("podman: the image needs git on its PATH for a GitRepo")
	}
	// The init step is bounded by the Sync context and by the sandbox's
	// expiry.
	expiry := s.created.Add(s.eff.TTL)
	ictx, cancel := context.WithDeadline(ctx, expiry)
	defer cancel()
	timeout := max(time.Until(expiry), time.Millisecond)
	res, err := s.p.r.run(ictx, append([]string{s.p.bin}, InitArgs(s.p.opts, s.id, s.created, s.eff, repo)...), timeout)
	if err != nil || ictx.Err() != nil || res.Outcome == tools.OutcomeTimeout || res.Outcome == tools.OutcomeAbort {
		// The engine client is gone, but its container may not be.
		rmErr := s.p.remove(context.WithoutCancel(ctx), "rm", "-f", initName(s.id))
		switch {
		case err != nil:
			return sandbox.SyncResult{}, errors.Join(fmt.Errorf("podman: GitRepo init step for %s: %w", repo.URL, err), rmErr)
		case ctx.Err() != nil:
			return sandbox.SyncResult{}, errors.Join(fmt.Errorf("podman: GitRepo init step for %s: %w", repo.URL, ctx.Err()), rmErr)
		}
		return sandbox.SyncResult{}, errors.Join(fmt.Errorf("podman: GitRepo init step for %s did not finish before the sandbox expired at %s", repo.URL, expiry.UTC().Format(time.RFC3339)), rmErr)
	}
	if err := attach.Live(s.inner); err != nil {
		// Closed or expired while the clone ran: the destroy step may have
		// removed the volume before the init container's --mount re-created
		// it, without labels, so remove it again.
		return sandbox.SyncResult{}, errors.Join(err, s.p.removeVolume(context.WithoutCancel(ctx), s.id))
	}
	switch {
	case res.Outcome == tools.OutcomeOK:
		s.slotTaken.Store(true)
		return sandbox.SyncResult{}, nil
	case res.ExitCode == 126 || res.ExitCode == 127:
		return sandbox.SyncResult{}, fmt.Errorf("podman: the image cannot run git for a GitRepo (exit status %d): %s", res.ExitCode, strings.TrimSpace(res.Output))
	}
	return sandbox.SyncResult{}, fmt.Errorf("podman: GitRepo clone of %s failed (%s, exit status %d): %s", repo.URL, res.Outcome, res.ExitCode, strings.TrimSpace(res.Output))
}
