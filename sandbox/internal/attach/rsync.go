package attach

import (
	"context"
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// syncLocalDir fills the workspace from a host directory over the Rsync
// stream.
func (s *sb) syncLocalDir(ctx context.Context, ld sandbox.LocalDir) (sandbox.SyncResult, error) {
	if err := s.gate(); err != nil {
		return sandbox.SyncResult{}, err
	}
	return sandbox.SyncResult{}, fmt.Errorf("%w: LocalDir", sandbox.ErrUnsupported)
}

// exportLocalDir writes a workspace path into a host directory over the
// Rsync stream.
func (s *sb) exportLocalDir(ctx context.Context, path string, ld sandbox.LocalDir) error {
	if err := s.gate(); err != nil {
		return err
	}
	return fmt.Errorf("%w: LocalDir", sandbox.ErrUnsupported)
}
