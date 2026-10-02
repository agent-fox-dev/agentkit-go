//go:build !windows

package codesearch

import (
	"context"
	"errors"
	"time"

	"github.com/agentfox/agentkit-go/tools"
)

// Options configures the codesearch index.
type Options struct {
	// Ignore is the ignore configuration, typically the same value the
	// embedder passes to tools.Options.
	Ignore tools.IgnoreOptions

	// Env replaces the subprocess environment for ctags. Nil means
	// tools.ReducedEnv(nil).
	Env []string

	// DisableCtags forces the runner to nil, so every file falls back to
	// the heuristic or go/ast backend.
	DisableCtags bool

	// Runner, when non-nil, replaces the default CtagsRunner. It is the
	// seam tests use to inject a deterministic backend.
	Runner func(ctx context.Context, args []string) ([]byte, error)

	// MaxFiles is the file-count bound for a build. Zero or negative means
	// 100 000.
	MaxFiles int

	// MaxBytes is the byte-count bound for indexed content. Zero or negative
	// means 1 GiB.
	MaxBytes int64

	// MaxBuildTime is the wall-time bound for a build. Zero or negative means
	// 60 s.
	MaxBuildTime time.Duration

	// TempDir is the directory for shard files. Empty means os.TempDir().
	TempDir string
}

// New creates a codesearch index for the given workspace. It returns a
// tools.Index without starting a goroutine, running a process, walking the
// workspace or touching the disk. It returns an error if ws is nil.
func New(ws *tools.Workspace, opts Options) (*Index, error) {
	if ws == nil {
		return nil, errors.New("codesearch: workspace must not be nil")
	}

	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 100_000
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 1 << 30 // 1 GiB
	}
	if opts.MaxBuildTime <= 0 {
		opts.MaxBuildTime = 60 * time.Second
	}
	if opts.TempDir == "" {
		opts.TempDir = defaultTempDir()
	}

	return &Index{
		ws:   ws,
		opts: opts,
	}, nil
}

// Index is the codesearch index. It implements tools.Index.
type Index struct {
	ws   *tools.Workspace
	opts Options
}
