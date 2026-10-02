//go:build windows

package codesearch

import (
	"context"
	"time"

	"github.com/agentfox/agentkit-go/tools"
)

// Options configures the codesearch index.
type Options struct {
	Ignore       tools.IgnoreOptions
	Env          []string
	DisableCtags bool
	Runner       func(ctx context.Context, args []string) ([]byte, error)
	MaxFiles     int
	MaxBytes     int64
	MaxBuildTime time.Duration
	TempDir      string
}

// Index is the codesearch index stub for Windows.
type Index struct{}

// New returns ErrUnsupported on Windows because zoekt does not build on this
// platform. An embedder should fall back to Options.Index == nil.
func New(_ *tools.Workspace, _ Options) (*Index, error) {
	return nil, ErrUnsupported
}
