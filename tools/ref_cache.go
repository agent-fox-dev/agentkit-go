package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

	files      map[string]bool
	outlines   map[string]*outline.File
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
		candidates:    make(map[string][]string),
	}
}

// markDirty marks a workspace-relative path dirty in the reference cache.
func (rc *referenceCache) markDirty(rel string) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rel = filepath.ToSlash(filepath.Clean(rel))
	rc.dirtyPaths[rel] = true
	rc.markGoPackageDirty(rel)

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
}

// markGoPackageDirty flags the package directory of rel when rel is a Go file.
func (rc *referenceCache) markGoPackageDirty(rel string) {
	if !strings.HasSuffix(rel, ".go") {
		return
	}
	pkgDir := filepath.ToSlash(filepath.Dir(rel))
	if pkgDir == "." {
		pkgDir = ""
	}
	rc.dirtyPackages[pkgDir] = true
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
	_ = Walk(ctx, rc.ws, rc.ws.Root, WalkOptions{}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() {
			rc.files[filepath.ToSlash(rel)] = true
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
			if rc.importer != nil {
				delete(rc.importer.parsedFiles, abs)
				delete(rc.importer.fileSources, abs)
			}
			rc.markGoPackageDirty(f)
		}
	}

	// 4. If revalidateAll is set, mark all known Go packages and non-Go files dirty
	if rc.revalidateAll {
		for f := range rc.files {
			if strings.HasSuffix(f, ".go") {
				rc.markGoPackageDirty(f)
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

// getCandidateFiles returns candidate files matching name, checking
// candidate cache first. A list cut short by budget is not cached.
func (rc *referenceCache) getCandidateFiles(ctx context.Context, name string, budget *refBudget) ([]string, error) {
	rc.mu.Lock()
	if cands, ok := rc.candidates[name]; ok {
		rc.mu.Unlock()
		return cands, nil
	}
	rc.mu.Unlock()

	cands, err := findCandidateFiles(ctx, rc.ws, name, rc.ft.index, budget)
	if err != nil {
		return nil, err
	}

	rc.mu.Lock()
	if !budget.exhausted() {
		rc.candidates[name] = cands
	}
	for _, c := range cands {
		rc.files[c] = true
	}
	rc.mu.Unlock()

	return cands, nil
}
