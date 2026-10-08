package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
)

// referenceCache maintains in-memory reference analysis, parsed Go packages,
// file outlines, and candidate file sets across queries with invalidation hooks.
type referenceCache struct {
	mu sync.Mutex

	ws *Workspace
	ft *fileTools

	dirtyPaths           map[string]bool
	dirtyPackages        map[string]bool
	revalidateAll        bool
	revalidateCandidates bool
	generation           uint64

	files      map[string]bool
	outlines   map[string]*outline.File
	sources    map[string][]byte
	candidates map[string][]string

	importer *workspaceImporter
}

// newReferenceCache creates an in-memory reference cache for a workspace.
func newReferenceCache(ws *Workspace, ft *fileTools) *referenceCache {
	return &referenceCache{
		ws:            ws,
		ft:            ft,
		dirtyPaths:    make(map[string]bool),
		dirtyPackages: make(map[string]bool),
		files:         make(map[string]bool),
		outlines:      make(map[string]*outline.File),
		sources:       make(map[string][]byte),
		candidates:    make(map[string][]string),
	}
}

// markDirty marks a workspace-relative path dirty in the reference cache.
func (rc *referenceCache) markDirty(rel string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rel = filepath.ToSlash(filepath.Clean(rel))
	if rc.dirtyPaths == nil {
		rc.dirtyPaths = make(map[string]bool)
	}
	if rc.dirtyPackages == nil {
		rc.dirtyPackages = make(map[string]bool)
	}
	rc.dirtyPaths[rel] = true
	rc.generation++

	if strings.HasSuffix(rel, ".go") {
		pkgDir := filepath.ToSlash(filepath.Dir(rel))
		if pkgDir == "." {
			pkgDir = ""
		}
		rc.dirtyPackages[pkgDir] = true
	}

	base := filepath.Base(rel)
	if base == ".gitignore" || base == ".ignore" {
		rc.revalidateCandidates = true
	}
}

// markRevalidateAll flags the entire reference cache for revalidation.
func (rc *referenceCache) markRevalidateAll() {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rc.revalidateAll = true
	rc.revalidateCandidates = true
	rc.generation++
}

// isPathDirty returns true if the workspace-relative path is flagged dirty.
func (rc *referenceCache) isPathDirty(rel string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rel = filepath.ToSlash(filepath.Clean(rel))
	return rc.dirtyPaths[rel]
}

// isPackageDirty returns true if the Go package is flagged dirty.
func (rc *referenceCache) isPackageDirty(pkg string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.revalidateAll {
		return true
	}
	clean := filepath.ToSlash(filepath.Clean(pkg))
	if strings.HasSuffix(clean, ".go") {
		clean = filepath.Dir(clean)
	}
	clean = strings.TrimPrefix(clean, "./")
	if clean == "." {
		clean = ""
	}
	return rc.dirtyPackages[clean]
}

// isPkgDirty is an alias for isPackageDirty.
func (rc *referenceCache) isPkgDirty(pkg string) bool {
	return rc.isPackageDirty(pkg)
}

// needsRevalidateAll returns true if the reference cache is pending whole-table revalidation.
func (rc *referenceCache) needsRevalidateAll() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.revalidateAll
}

// hasFile returns true if rel is currently tracked in the cache.
func (rc *referenceCache) hasFile(rel string) bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rel = filepath.ToSlash(filepath.Clean(rel))
	return rc.files[rel]
}

// populateInitial scans workspace files to populate the initial file set.
func (rc *referenceCache) populateInitial(ctx context.Context) {
	if rc.ws == nil {
		return
	}
	_ = Walk(ctx, rc.ws, rc.ws.Root, WalkOptions{Ignore: IgnoreOptions{}, IncludeHidden: false}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() {
			relSlash := filepath.ToSlash(rel)
			rc.files[relSlash] = true
		}
		return nil
	})
}

// refresh re-parses dirty Go packages, re-outlines dirty non-Go files,
// purges deleted files, and clears dirty flags.
func (rc *referenceCache) refresh(ctx context.Context) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if rc.ws == nil {
		return nil
	}

	// 1. Initial population if cache is empty
	if len(rc.files) == 0 {
		rc.populateInitial(ctx)
	}

	// 2. Candidate cache revalidation if .gitignore changed or whole table revalidated
	if rc.revalidateCandidates || rc.revalidateAll {
		rc.candidates = make(map[string][]string)
	}

	// 3. Purge deleted files from cache
	for f := range rc.files {
		abs := filepath.Join(rc.ws.Root, filepath.FromSlash(f))
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() {
			delete(rc.files, f)
			delete(rc.outlines, f)
			delete(rc.sources, f)
			if rc.importer != nil {
				delete(rc.importer.parsedFiles, abs)
				delete(rc.importer.fileSources, abs)
			}
			if strings.HasSuffix(f, ".go") {
				pkgDir := filepath.ToSlash(filepath.Dir(f))
				if pkgDir == "." {
					pkgDir = ""
				}
				rc.dirtyPackages[pkgDir] = true
			}
		}
	}

	// 4. If revalidateAll is set, mark all known Go packages and non-Go files dirty
	if rc.revalidateAll {
		for f := range rc.files {
			if strings.HasSuffix(f, ".go") {
				pkgDir := filepath.ToSlash(filepath.Dir(f))
				if pkgDir == "." {
					pkgDir = ""
				}
				rc.dirtyPackages[pkgDir] = true
			} else {
				rc.dirtyPaths[f] = true
			}
		}
	}

	// 5. Re-parse dirty Go packages
	if rc.importer == nil {
		rc.importer = newWorkspaceImporter(rc.ws, "")
	}
	for pkgDir := range rc.dirtyPackages {
		if err := ctx.Err(); err != nil {
			return err
		}
		delete(rc.importer.imported, pkgDir)
		delete(rc.importer.pkgInfos, pkgDir)
		if rc.importer.modulePath != "" {
			importPath := rc.importer.modulePath
			if pkgDir != "" {
				importPath += "/" + pkgDir
			}
			delete(rc.importer.imported, importPath)
			delete(rc.importer.pkgInfos, importPath)
		}

		absDir := filepath.Join(rc.ws.Root, filepath.FromSlash(pkgDir))
		for path := range rc.importer.parsedFiles {
			if filepath.Dir(path) == absDir {
				delete(rc.importer.parsedFiles, path)
				delete(rc.importer.fileSources, path)
			}
		}

		if fi, err := os.Stat(absDir); err == nil && fi.IsDir() {
			_, _ = checkPackage(rc.importer, pkgDir)
			if entries, err := os.ReadDir(absDir); err == nil {
				for _, entry := range entries {
					if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
						rel := filepath.ToSlash(filepath.Join(pkgDir, entry.Name()))
						rc.files[rel] = true
					}
				}
			}
		}
	}

	// 6. Re-outline dirty non-Go files
	for rel := range rc.dirtyPaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".go") {
			continue
		}
		abs := filepath.Join(rc.ws.Root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil || fi.IsDir() {
			delete(rc.files, rel)
			delete(rc.outlines, rel)
			delete(rc.sources, rel)
			continue
		}
		rc.files[rel] = true
		ofile, err := outline.Outline(ctx, abs, nil, outline.Options{Root: rc.ws.Root})
		if err == nil {
			rc.outlines[rel] = &ofile
		}
	}

	// 7. Clear dirty flags
	rc.dirtyPaths = make(map[string]bool)
	rc.dirtyPackages = make(map[string]bool)
	rc.revalidateAll = false
	rc.revalidateCandidates = false

	return nil
}

// getCandidateFiles returns candidate files matching name, checking candidate cache first.
func (rc *referenceCache) getCandidateFiles(ctx context.Context, name string) ([]string, error) {
	rc.mu.Lock()
	if cands, ok := rc.candidates[name]; ok {
		rc.mu.Unlock()
		return cands, nil
	}
	rc.mu.Unlock()

	var idx Index
	if rc.ft != nil {
		idx = rc.ft.index
	}
	cands, err := findCandidateFilesCtx(ctx, rc.ws, name, idx)
	if err != nil {
		return nil, err
	}

	rc.mu.Lock()
	rc.candidates[name] = cands
	for _, c := range cands {
		rc.files[c] = true
	}
	rc.mu.Unlock()

	return cands, nil
}

// tool returns the core.Tool for find_references backed by referenceCache.
func (rc *referenceCache) tool() core.Tool {
	return core.Tool{
		Name:        "find_references",
		Description: "Find references and callers of a declaration across the workspace.",
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			if err := ctx.Err(); err != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}

			var args struct {
				Name         string `json:"name"`
				Path         string `json:"path"`
				Kind         string `json:"kind"`
				IncludeTests *bool  `json:"include_tests"`
				MaxResults   *int   `json:"max_results"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return core.ErrResult("invalid_arguments", "Malformed input")
			}
			if args.Name == "" {
				return core.ErrResult("invalid_arguments", "name is required")
			}

			var scopePrefix string
			if args.Path != "" && rc.ws != nil {
				resolved, err := rc.ws.Resolve(args.Path)
				if err != nil {
					return core.ErrResult("path_not_allowed", err.Error())
				}
				fi, err := os.Stat(resolved)
				if err != nil || !fi.IsDir() {
					return core.ErrResult("invalid_arguments", "Path must be an existing directory")
				}
				scopePrefix = filepath.ToSlash(rc.ws.Rel(resolved))
				if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
					scopePrefix += "/"
				}
			}

			includeTests := true
			if args.IncludeTests != nil {
				includeTests = *args.IncludeTests
			}

			maxResults := 30
			if args.MaxResults != nil {
				maxResults = clampMaxResults(*args.MaxResults)
			}

			// Ensure freshness before querying
			if err := rc.refresh(ctx); err != nil {
				if ctx.Err() != nil {
					return core.ErrResult("aborted", "Operation aborted")
				}
				return core.ErrResult("internal_error", err.Error())
			}

			var st *symbolTable
			if rc.ft != nil {
				st = rc.ft.getTable()
			}
			candidates := resolveSymbolCandidates(st, args.Name, args.Kind, args.Path)

			var target outline.Decl
			backend := "text"
			if len(candidates) > 0 {
				top := disambiguateSymbol(candidates, args.Name)
				target = top.Decl()
				if strings.HasSuffix(top.Path, ".go") {
					backend = "go/types"
				} else {
					backend = "lexical"
				}
			} else {
				target = outline.Decl{Name: args.Name}
				backend = "text"
			}

			candFiles, err := rc.getCandidateFiles(ctx, args.Name)
			if err != nil {
				return core.ErrResult("internal_error", err.Error())
			}

			var sites []ReferenceSite
			if backend == "go/types" {
				sites = resolveGoReferences(target, rc.ws)
			} else {
				for _, rel := range candFiles {
					if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
						continue
					}
					abs := filepath.Join(rc.ws.Root, filepath.FromSlash(rel))
					content, readErr := os.ReadFile(abs)
					if readErr != nil {
						continue
					}
					matches := scanContentForMatches(rel, content, args.Name, target)
					for _, m := range matches {
						sites = append(sites, m.Site())
					}
				}
			}

			// If Go resolution returned no sites or for text matches
			if len(sites) == 0 && backend != "go/types" {
				for _, rel := range candFiles {
					if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
						continue
					}
					abs := filepath.Join(rc.ws.Root, filepath.FromSlash(rel))
					content, readErr := os.ReadFile(abs)
					if readErr != nil {
						continue
					}
					if bytes.Contains(content, []byte(args.Name)) {
						matches := scanContentForMatches(rel, content, args.Name, target)
						for _, m := range matches {
							sites = append(sites, m.Site())
						}
					}
				}
			}

			sites = filterTestSites(sites, includeTests)
			sortReferenceSites(sites)
			limited := applyResultLimits(sites, maxResults)

			refRes := ReferenceResult{
				Target:          target,
				Sites:           limited.Sites,
				Backend:         backend,
				Partial:         false,
				Truncated:       limited.Truncated,
				PackagesChecked: 1,
				Errors:          0,
			}

			return renderReferencesResult(args.Name, refRes)
		},
	}
}
