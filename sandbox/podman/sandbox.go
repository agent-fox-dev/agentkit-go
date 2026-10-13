package podman

import (
	"context"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// podSandbox is a podman sandbox: the attached sandbox, plus what the
// provider's own steps need.
type podSandbox struct {
	inner   sandbox.Sandbox
	p       *provider
	id      string
	created time.Time
	eff     sandbox.Spec
}

func (s *podSandbox) ID() string               { return s.inner.ID() }
func (s *podSandbox) Spec() sandbox.Spec       { return s.inner.Spec() }
func (s *podSandbox) Agent() sandbox.AgentInfo { return s.inner.Agent() }

func (s *podSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	return s.inner.Sync(ctx, src)
}

func (s *podSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	return s.inner.Tools(ctx, opts)
}

func (s *podSandbox) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	return s.inner.Export(ctx, path, dst)
}

func (s *podSandbox) Close(ctx context.Context) error { return s.inner.Close(ctx) }
