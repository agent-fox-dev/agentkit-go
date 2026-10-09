package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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

	partialReason string // SymbolPartialMarker reason when Partial
}

// clampMaxResults normalizes maxResults: <= 0 defaults to 30; > 100 clamps to 100.
func clampMaxResults(maxResults int) int {
	if maxResults <= 0 {
		return 30
	}
	return min(maxResults, 100)
}

// References finds usages and callers of target across the workspace,
// bounded by the default SymbolOptions.
func (w *Workspace) References(ctx context.Context, target outline.Decl, opts ReferenceOptions) (ReferenceResult, error) {
	return executeReferenceSearch(ctx, w, target, "", "", opts, SymbolOptions{}, nil)
}

// executeReferenceSearch runs reference discovery and ranking across
// workspace files. targetPath, when known, is the workspace-relative file
// that declares target. An empty backend is chosen from the target: "go/types"
// when a Go package declares it, "lexical" otherwise, "text" for an empty
// name. The pass is bounded by bounds (05-REQ-6.4). rc, when non-nil,
// supplies the checked Go packages, outlines and candidate files it has
// cached (05-REQ-8.1).
func executeReferenceSearch(ctx context.Context, ws *Workspace, target outline.Decl, targetPath, backend string, opts ReferenceOptions, bounds SymbolOptions, rc *referenceCache) (ReferenceResult, error) {
	if err := ctx.Err(); err != nil {
		return ReferenceResult{}, err
	}
	if ws == nil {
		return ReferenceResult{}, errors.New("tools: nil workspace")
	}
	scopePrefix, err := referenceScope(ws, opts.Path)
	if err != nil {
		return ReferenceResult{}, err
	}

	budget := newRefBudget(bounds)
	outlineOf := func(rel string) []outline.Decl { return outlineDecls(ctx, ws, rel) }
	if rc != nil {
		outlineOf = func(rel string) []outline.Decl { return rc.outlineDecls(ctx, rel) }
	}

	var sites []ReferenceSite
	var packages, typeErrors int
	if backend == "" || backend == "go/types" {
		var imp *workspaceImporter
		if rc != nil {
			imp = rc.goImporter(ctx, budget)
		} else {
			imp = loadGoWorkspace(ctx, ws, budget)
		}
		sites = resolveGoReferences(imp, target, targetPath)
		packages, typeErrors = imp.stats()
		switch {
		case backend != "":
		case len(sites) > 0 || (target.Name != "" && imp.definesName(target.Name)):
			backend = "go/types"
		case target.Name != "":
			backend = "lexical"
		default:
			backend = "text"
		}
	}

	if backend == "go/types" {
		sites = slices.DeleteFunc(sites, func(s ReferenceSite) bool { return !strings.HasPrefix(s.Path, scopePrefix) })
	} else {
		sites, err = scanCandidates(ctx, ws, rc, target, scopePrefix, backend == "text", budget)
		if err != nil {
			return ReferenceResult{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ReferenceResult{}, err
	}

	sites = filterTestSites(sites, opts.IncludeTests)
	sortReferenceSites(sites)
	sites, truncated := applyResultLimits(sites, clampMaxResults(opts.MaxResults))
	attributeSites(sites, outlineOf)

	return ReferenceResult{
		Target:          target,
		Sites:           sites,
		Backend:         backend,
		Partial:         budget.exhausted(),
		PackagesChecked: packages,
		Errors:          typeErrors,
		Truncated:       truncated,
		partialReason:   budget.partialReason(),
	}, nil
}

// referenceScope validates path, a directory inside ws, and returns it as
// a slash-separated workspace-relative prefix ending in "/", or "" for the
// whole workspace.
func referenceScope(ws *Workspace, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	resolved, err := ws.Resolve(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("references: path %q does not exist: %w", path, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("references: path %q is not a directory", path)
	}
	prefix := filepath.ToSlash(ws.Rel(resolved))
	if prefix == "." {
		return "", nil
	}
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix, nil
}

// scanCandidates scans the files under scopePrefix that mention target's
// name for whole-identifier matches, all labelled text when allText, until
// budget runs out or ctx ends.
func scanCandidates(ctx context.Context, ws *Workspace, rc *referenceCache, target outline.Decl, scopePrefix string, allText bool, budget *refBudget) ([]ReferenceSite, error) {
	var candFiles []string
	var err error
	if rc != nil {
		candFiles, err = rc.getCandidateFiles(ctx, target.Name, budget)
	} else {
		candFiles, err = findCandidateFiles(ctx, ws, target.Name, nil, budget)
	}
	if err != nil {
		return nil, err
	}
	var sites []ReferenceSite
	for _, rel := range candFiles {
		if !strings.HasPrefix(rel, scopePrefix) {
			continue
		}
		if ctx.Err() != nil || budget.expired() {
			break
		}
		content, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		for _, site := range scanContentForMatches(rel, content, target) {
			if allText {
				site.Confidence = "text"
			}
			sites = append(sites, site)
		}
	}
	return sites, nil
}
