//go:build windows

package codesearch

import (
	"context"
	"time"

	"github.com/agentfox/agentkit-go/core"
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

// Index is the codesearch index stub for Windows. It implements tools.Index
// with inert methods, so one cross-platform code path compiles everywhere.
// New never returns one.
type Index struct{}

var _ tools.Index = (*Index)(nil)

// Symbols never answers, so find_symbol uses its own table.
func (*Index) Symbols(context.Context, tools.SymbolQuery) (tools.SymbolAnswer, bool, error) {
	return tools.SymbolAnswer{}, false, nil
}

// Tools provides no tools.
func (*Index) Tools() []core.Tool { return nil }

// Invalidate does nothing.
func (*Index) Invalidate(string) {}

// Close does nothing and is idempotent.
func (*Index) Close() error { return nil }

// New returns ErrUnsupported on Windows because zoekt does not build on this
// platform. The result is a nil interface; an embedder falls back to
// Options.Index == nil.
func New(_ *tools.Workspace, _ Options) (tools.Index, error) {
	return nil, ErrUnsupported
}
