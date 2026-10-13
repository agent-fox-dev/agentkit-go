package landlock

import (
	"context"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// landlockSandbox is a landlock sandbox: attach's sandbox, plus what the
// provider's own steps need.
type landlockSandbox struct {
	inner     sandbox.Sandbox
	p         *provider
	dir, ws   string
	created   time.Time
	agentPATH string // the PATH the sandbox's agent got
}

func (s *landlockSandbox) ID() string               { return s.inner.ID() }
func (s *landlockSandbox) Spec() sandbox.Spec       { return s.inner.Spec() }
func (s *landlockSandbox) Agent() sandbox.AgentInfo { return s.inner.Agent() }

func (s *landlockSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	return s.inner.Sync(ctx, src)
}

func (s *landlockSandbox) Tools(ctx context.Context, opts sandbox.ToolOptions) ([]core.Tool, error) {
	return s.inner.Tools(ctx, opts)
}

func (s *landlockSandbox) Export(ctx context.Context, path string, dst sandbox.Destination) error {
	return s.inner.Export(ctx, path, dst)
}

func (s *landlockSandbox) Close(ctx context.Context) error { return s.inner.Close(ctx) }
