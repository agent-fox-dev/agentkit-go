package gateway

import (
	"context"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gitclone"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// gwSandbox is a sandbox on a gateway: attach's sandbox over the shared
// connection, plus the GitRepo Sync, which is a gateway RPC rather than an
// agent one. Everything else is attach's, unchanged.
type gwSandbox struct {
	inner sandbox.Sandbox
	p     *Provider
	id    string
}

func (s *gwSandbox) ID() string               { return s.inner.ID() }
func (s *gwSandbox) Spec() sandbox.Spec       { return s.inner.Spec() }
func (s *gwSandbox) Agent() sandbox.AgentInfo { return s.inner.Agent() }

func (s *gwSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	return s.inner.Tools(ctx, opts)
}

func (s *gwSandbox) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	return s.inner.Export(ctx, path, dst)
}

// Close runs attach's teardown, which destroys the sandbox on the gateway
// with a fresh 30-second context; a failed destroy is returned, and the
// sandbox then lives on the gateway until its expiry.
func (s *gwSandbox) Close(ctx context.Context) error { return s.inner.Close(ctx) }

// Unwrap is attach's sandbox; attach.ClientConn and attach.Live follow it.
func (s *gwSandbox) Unwrap() sandbox.Sandbox { return s.inner }

// Sync forwards a Tar or LocalDir to attach's sandbox. A GitRepo is cloned
// by the gateway's backend: the sandbox must be live and the repository
// valid before any RPC, and success reports a SyncResult of zeros.
func (s *gwSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	repo, ok := src.(sandbox.GitRepo)
	if !ok {
		return s.inner.Sync(ctx, src)
	}
	if err := attach.Live(s.inner); err != nil {
		return sandbox.SyncResult{}, err
	}
	if err := gitclone.Check(repo); err != nil {
		return sandbox.SyncResult{}, err
	}
	resp, err := s.p.gw.SyncGit(ctx, &sandboxpb.SyncGitRequest{Id: s.id, Url: repo.URL, Ref: repo.Ref, Depth: int32(repo.Depth)})
	if err != nil {
		return sandbox.SyncResult{}, s.p.rpcErr("sync git", err)
	}
	if r := resp.GetRefusal(); r != nil {
		return sandbox.SyncResult{}, refusalErr(s.p.caps.Provider, r)
	}
	return sandbox.SyncResult{}, nil
}
