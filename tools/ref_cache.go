package tools

import (
	"context"
	"go/ast"
	"go/token"
	"io/fs"
	"maps"
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

	// fset and parsed (Go ASTs by absolute path) outlive any one
	// importer, so a re-check parses only the files that changed.
	fset   *token.FileSet
	parsed map[string]*ast.File
	// importer is the last complete check of the workspace's Go
	// packages; nil when it must be rebuilt. It is never mutated once
	// stored, so queries read it without the lock.
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
		fset:          token.NewFileSet(),
		parsed:        make(map[string]*ast.File),
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

// refresh applies the pending marks before a query: files deleted from
// disk are purged, dirty Go packages lose their parsed files and the
// checked workspace (so goImporter re-parses and re-checks them before
// references are resolved), dirty files already outlined are outlined
// again, and candidate lists are dropped once anything changed.
func (rc *referenceCache) refresh(ctx context.Context) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if rc.ws == nil {
		return nil
	}

	if len(rc.files) == 0 {
		rc.populateInitial(ctx)
	}

	// Any change can add or remove a mention of any name.
	if rc.revalidateCandidates || rc.revalidateAll || len(rc.dirtyPaths) > 0 {
		rc.candidates = make(map[string][]string)
	}
	// A changed ignore file changes which Go directories are walked.
	if rc.revalidateCandidates {
		rc.importer = nil
	}

	for f := range rc.files {
		abs := filepath.Join(rc.ws.Root, filepath.FromSlash(f))
		if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
			delete(rc.files, f)
			delete(rc.outlines, f)
			delete(rc.parsed, abs)
			rc.markGoPackageDirty(f)
		}
	}

	// After a shell command any file may have changed.
	if rc.revalidateAll {
		rc.importer = nil
		rc.parsed = make(map[string]*ast.File)
		for rel := range rc.outlines {
			rc.dirtyPaths[rel] = true
		}
	}

	for pkgDir := range rc.dirtyPackages {
		rc.importer = nil
		absDir := filepath.Join(rc.ws.Root, filepath.FromSlash(pkgDir))
		for path := range rc.parsed {
			if filepath.Dir(path) == absDir {
				delete(rc.parsed, path)
			}
		}
	}

	for rel := range rc.dirtyPaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		abs := filepath.Join(rc.ws.Root, filepath.FromSlash(rel))
		if fi, err := os.Stat(abs); err != nil || fi.IsDir() {
			delete(rc.files, rel)
			delete(rc.outlines, rel)
			continue
		}
		rc.files[rel] = true
		if _, ok := rc.outlines[rel]; !ok {
			continue // outlined on first use
		}
		delete(rc.outlines, rel)
		if ofile, err := outline.Outline(ctx, abs, nil, outline.Options{Root: rc.ws.Root}); err == nil {
			rc.outlines[rel] = &ofile
		}
	}

	rc.dirtyPaths = make(map[string]bool)
	rc.dirtyPackages = make(map[string]bool)
	rc.revalidateAll = false
	rc.revalidateCandidates = false

	return nil
}

// goImporter returns the cached check of the workspace's Go packages,
// building it under budget when there is none. Files parsed by an earlier
// build and not dirtied since are reused. A build cut short by budget is
// returned but not cached.
func (rc *referenceCache) goImporter(budget *refBudget) *workspaceImporter {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.importer != nil {
		return rc.importer
	}
	imp := newWorkspaceImporter(rc.ws, "")
	imp.fset = rc.fset
	maps.Copy(imp.parsedFiles, rc.parsed)
	imp.load(budget)
	rc.parsed = maps.Clone(imp.parsedFiles)
	if !budget.exhausted() {
		rc.importer = imp
	}
	return imp
}

// outlineDecls returns the declarations of the workspace file rel from the
// cache, outlining and caching it on first use.
func (rc *referenceCache) outlineDecls(ctx context.Context, rel string) []outline.Decl {
	rc.mu.Lock()
	cached, ok := rc.outlines[rel]
	rc.mu.Unlock()
	if ok {
		return cached.Decls
	}
	ofile, err := outline.Outline(ctx, filepath.Join(rc.ws.Root, filepath.FromSlash(rel)), nil, outline.Options{Root: rc.ws.Root})
	if err != nil {
		return nil
	}
	rc.mu.Lock()
	rc.outlines[rel] = &ofile
	rc.mu.Unlock()
	return ofile.Decls
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
