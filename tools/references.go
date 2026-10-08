package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

	return executeReferenceSearch(ctx, w, target, "", opts, nil)
}

// isGoSymbol checks if a declaration exists in any Go package within the workspace.
func isGoSymbol(target outline.Decl, ws *Workspace) bool {
	if ws == nil || target.Name == "" {
		return false
	}
	imp := newWorkspaceImporter(ws, "")
	dirMap := make(map[string]bool)
	_ = Walk(context.Background(), ws, ws.Root, WalkOptions{}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".go") {
			dirMap[filepath.Dir(filepath.Join(ws.Root, filepath.FromSlash(rel)))] = true
		}
		return nil
	})
	for dir := range dirMap {
		relDir, err := filepath.Rel(ws.Root, dir)
		if err != nil {
			relDir = dir
		}
		if relDir == "." {
			relDir = ""
		}
		_, _ = checkPackage(imp, relDir)
	}
	for _, info := range imp.pkgInfos {
		for _, obj := range info.Defs {
			if obj != nil && obj.Name() == target.Name {
				return true
			}
		}
	}
	return false
}

// executeReferenceSearch runs reference discovery and ranking across workspace files.
func executeReferenceSearch(ctx context.Context, ws *Workspace, target outline.Decl, backend string, opts ReferenceOptions, rc *referenceCache) (ReferenceResult, error) {
	if err := ctx.Err(); err != nil {
		return ReferenceResult{}, err
	}
	if ws == nil {
		return ReferenceResult{}, errors.New("tools: nil workspace")
	}

	var scopePrefix string
	if opts.Path != "" {
		resolved, err := ws.Resolve(opts.Path)
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
		scopePrefix = filepath.ToSlash(ws.Rel(resolved))
		if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
			scopePrefix += "/"
		}
	}

	maxResults := clampMaxResults(opts.MaxResults)

	var sites []ReferenceSite
	if backend == "" {
		goSites := resolveGoReferences(target, ws)
		if len(goSites) > 0 {
			backend = "go/types"
			sites = goSites
		} else if isGoSymbol(target, ws) {
			backend = "go/types"
			sites = goSites
		} else if target.Name != "" {
			backend = "lexical"
		} else {
			backend = "text"
		}
	} else if backend == "go/types" {
		sites = resolveGoReferences(target, ws)
	}

	if backend == "go/types" {
		if scopePrefix != "" {
			var scoped []ReferenceSite
			for _, s := range sites {
				if strings.HasPrefix(filepath.ToSlash(s.Path), scopePrefix) {
					scoped = append(scoped, s)
				}
			}
			sites = scoped
		}
	} else {
		var candFiles []string
		var err error
		if rc != nil {
			candFiles, err = rc.getCandidateFiles(ctx, target.Name)
		} else {
			candFiles, err = findCandidateFilesCtx(ctx, ws, target.Name, nil)
		}
		if err != nil {
			return ReferenceResult{}, err
		}
		for _, rel := range candFiles {
			if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
				continue
			}
			abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
			content, readErr := os.ReadFile(abs)
			if readErr != nil {
				continue
			}
			matches := scanContentForMatches(rel, content, target.Name, target)
			for _, m := range matches {
				site := m.Site()
				if backend == "text" {
					site.Confidence = "text"
				}
				sites = append(sites, site)
			}
		}

		if len(sites) == 0 && backend != "go/types" {
			for _, rel := range candFiles {
				if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
					continue
				}
				abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
				content, readErr := os.ReadFile(abs)
				if readErr != nil {
					continue
				}
				if bytes.Contains(content, []byte(target.Name)) {
					matches := scanContentForMatches(rel, content, target.Name, target)
					for _, m := range matches {
						site := m.Site()
						if backend == "text" {
							site.Confidence = "text"
						}
						sites = append(sites, site)
					}
				}
			}
		}
	}

	sites = filterTestSites(sites, opts.IncludeTests)
	sortReferenceSites(sites)
	limited := applyResultLimits(sites, maxResults)

	return ReferenceResult{
		Target:          target,
		Sites:           limited.Sites,
		Backend:         backend,
		Partial:         false,
		Truncated:       limited.Truncated,
		PackagesChecked: 1,
		Errors:          0,
	}, nil
}
