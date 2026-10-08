package tools

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/agentfox/agentkit-go/outline"
)

// ReferenceOptions configures reference lookup on a Workspace.
type ReferenceOptions struct {
	Path         string // Subdirectory filter ("" for entire workspace)
	IncludeTests bool   // Whether to include test files (default false in struct, tool defaults to true)
	MaxResults   int    // 0 or negative defaults to 30; capped at 100
}

// ReferenceSite is one source location where a declaration is referenced.
type ReferenceSite struct {
	Path       string       // Slash-separated workspace-relative path
	Line       int          // 1-based line number
	Column     int          // 1-based byte column
	Confidence string       // "resolved", "lexical", or "text"
	Enclosing  outline.Decl // Enclosing declaration, or Kind "file" at top-level
	Source     string       // Trimmed source line, max 200 bytes, no control chars
}

// ReferenceResult is the outcome of a reference search.
type ReferenceResult struct {
	Target          outline.Decl
	Sites           []ReferenceSite
	Backend         string
	Partial         bool
	PackagesChecked int
	Errors          int
	Truncated       bool
}

// clampMaxResults normalizes maxResults: <= 0 defaults to 30; > 100 clamps to 100.
func clampMaxResults(maxResults int) int {
	if maxResults <= 0 {
		return 30
	}
	if maxResults > 100 {
		return 100
	}
	return maxResults
}

// normalizeWorkspaceMaxResults normalizes maxResults for Workspace.References.
func normalizeWorkspaceMaxResults(maxResults int) int {
	return clampMaxResults(maxResults)
}

// References finds usages and callers of target across the workspace.
func (w *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error) {
	if err := ctx.Err(); err != nil {
		return ReferenceResult{}, err
	}
	if w == nil {
		return ReferenceResult{}, errors.New("tools: nil workspace")
	}

	if opts.Path != "" {
		resolved, err := w.Resolve(opts.Path)
		if err != nil {
			return ReferenceResult{}, err
		}
		fi, err := os.Stat(resolved)
		if err != nil {
			return ReferenceResult{}, fmt.Errorf("references: path %q does not exist: %w", opts.Path, err)
		}
		if !fi.IsDir() {
			return ReferenceResult{}, fmt.Errorf("references: path %q is not a directory", opts.Path)
		}
	}

	opts.MaxResults = clampMaxResults(opts.MaxResults)

	// Reference resolution engine will be invoked here in subsequent tasks.
	return ReferenceResult{
		Target: target,
		Sites:  []ReferenceSite{},
	}, nil
}
