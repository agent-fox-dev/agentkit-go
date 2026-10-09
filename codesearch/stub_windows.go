//go:build windows

package codesearch

import (
	"context"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// Options configures the codesearch index.
type Options struct {
	Ignore       tools.IgnoreOptions
	MaxFiles     int
	MaxBytes     int64
	MaxBuildTime time.Duration
	TempDir      string
}

// Index is the codesearch index stub for Windows. It implements tools.Index
// and the counters, statistics and Build of the real one with inert methods,
// so one cross-platform code path compiles everywhere.
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

// Build reports ErrUnsupported: there is nothing to build on this platform.
func (*Index) Build(context.Context) error { return ErrUnsupported }

// BuildCount is always 0.
func (*Index) BuildCount() int { return 0 }

// OverlayBuildCount is always 0.
func (*Index) OverlayBuildCount() int { return 0 }

// RevalCount is always 0.
func (*Index) RevalCount() int { return 0 }

// BuildStats is the zero value.
func (*Index) BuildStats() BuildStatsResult { return BuildStatsResult{} }

// IndexedFiles is empty.
func (*Index) IndexedFiles() map[string]bool { return map[string]bool{} }

// RunDir is empty: no shard directory is ever created.
func (*Index) RunDir() string { return "" }

// New returns ErrUnsupported on Windows because zoekt does not build on this
// platform. The result is a nil interface; an embedder falls back to
// Options.Index == nil.
func New(_ *tools.Workspace, _ Options) (tools.Index, error) {
	return nil, ErrUnsupported
}
