//go:build !windows

package codesearch

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/agentfox/agentkit-go/tools"
)

// dirtyTracker tracks files that have been modified since the index was built.
// It uses a separate mutex from the index's main lock so that Invalidate
// never blocks on a build or query in progress.
type dirtyTracker struct {
	mu sync.Mutex

	// paths maps slash-separated workspace-relative paths to their dirty
	// generation. A path present here is dirty.
	paths map[string]uint64

	// gone tracks dirty paths that should not appear in results (deleted
	// or now ignored by .gitignore). These are excluded from dirty-file
	// searching.
	gone map[string]bool

	// validated tracks dirty paths that have been seen by a revalidation
	// walk. These don't trigger another revalidation even if they're not
	// in the original index.
	validated map[string]bool

	// revalidateAll is set when the whole index needs revalidation
	// (Invalidate("") was called).
	revalidateAll bool

	// revalidateAllGen is the generation at which revalidateAll was last set.
	revalidateAllGen uint64

	// generation is advanced by every mark; a pass clears only marks that
	// predate its start.
	generation uint64
}

func newDirtyTracker() *dirtyTracker {
	return &dirtyTracker{
		paths:     make(map[string]uint64),
		gone:      make(map[string]bool),
		validated: make(map[string]bool),
	}
}

// markDirty records a single path as dirty.
func (dt *dirtyTracker) markDirty(rel string) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.generation++
	dt.paths[rel] = dt.generation
}

// markRevalidateAll sets the whole-index revalidation flag.
func (dt *dirtyTracker) markRevalidateAll() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.generation++
	dt.revalidateAll = true
	dt.revalidateAllGen = dt.generation
}

// snapshot reads the current dirty state without clearing it.
// Returns the dirty paths (path -> generation), whether revalidateAll is set,
// and the current generation.
func (dt *dirtyTracker) snapshot() (paths map[string]uint64, revalidateAll bool, gen uint64) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	paths = make(map[string]uint64, len(dt.paths))
	for k, v := range dt.paths {
		paths[k] = v
	}
	return paths, dt.revalidateAll, dt.generation
}

// clearOlderThan removes dirty marks with generation <= genAtStart and
// clears revalidateAll if no new marks have arrived since genAtStart.
func (dt *dirtyTracker) clearOlderThan(genAtStart uint64) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	for k, g := range dt.paths {
		if g <= genAtStart {
			delete(dt.paths, k)
			delete(dt.gone, k)
			delete(dt.validated, k)
		}
	}
	if dt.generation <= genAtStart {
		dt.revalidateAll = false
	}
}

// hasDirty returns true if there are any dirty paths or revalidateAll is set.
func (dt *dirtyTracker) hasDirty() bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	return len(dt.paths) > 0 || dt.revalidateAll
}

// dirtyPathSet returns the set of dirty path names (including gone files).
func (dt *dirtyTracker) dirtyPathSet() map[string]bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	out := make(map[string]bool, len(dt.paths))
	for k := range dt.paths {
		out[k] = true
	}
	return out
}

// isGone returns true if the path has been marked as gone (deleted or ignored).
func (dt *dirtyTracker) isGone(rel string) bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	return dt.gone[rel]
}

// indexedFileInfo holds the (size, mtime) of a file at index time.
type indexedFileInfo struct {
	size      int64
	mtime     time.Time
	indexedAt time.Time
}

// needsRevalidation returns true if a revalidation walk is needed before
// answering a query. This is the case when:
// - revalidateAll is set, OR
// - any dirty path is not in the index AND not already validated, OR
// - any dirty path has base name .gitignore AND not already validated
func (dt *dirtyTracker) needsRevalidation(indexedFiles map[string]bool) bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if dt.revalidateAll {
		return true
	}
	for rel := range dt.paths {
		// Skip paths already validated by a previous revalidation walk.
		if dt.validated[rel] {
			continue
		}
		if filepath.Base(rel) == ".gitignore" {
			return true
		}
		if !indexedFiles[rel] {
			return true
		}
	}
	return false
}

// revalidate walks the workspace and compares (size, mtime) with the indexed
// values. Files indexed within the previous 2 seconds are treated as changed
// (the filesystem timestamp-granularity window). Changed, new and removed
// files are marked dirty. It clears the revalidateAll flag using
// generation-based clearing.
func (dt *dirtyTracker) revalidate(
	ws *tools.Workspace,
	ig tools.IgnoreOptions,
	indexedFiles map[string]bool,
	fileInfos map[string]indexedFileInfo,
) {
	// Snapshot the generation before the walk so we can clear
	// revalidateAll only if no new marks arrived during the walk.
	dt.mu.Lock()
	genAtStart := dt.generation
	dt.mu.Unlock()
	seen := make(map[string]bool)

	_ = tools.Walk(
		context.Background(),
		ws, ws.Root,
		tools.WalkOptions{
			Ignore:        ig,
			IncludeHidden: false,
		},
		func(rel string, d fs.DirEntry) error {
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}

			abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
			fi, err := os.Stat(abs)
			if err != nil {
				return nil
			}

			// Skip oversized and binary files (same filters as build).
			if fi.Size() > maxFileSize {
				return nil
			}
			f, err := os.Open(abs)
			if err != nil {
				return nil
			}
			buf := make([]byte, binarySniffSize)
			n, _ := f.Read(buf)
			f.Close()
			if bytes.IndexByte(buf[:n], 0) >= 0 {
				return nil
			}

			seen[rel] = true

			// Check if the file has changed.
			info, indexed := fileInfos[rel]
			if !indexed {
				// New file: mark dirty and validated.
				dt.markDirty(rel)
				dt.mu.Lock()
				dt.validated[rel] = true
				dt.mu.Unlock()
				return nil
			}

			// Check (size, mtime) and the 2-second racy window.
			changed := false
			if fi.Size() != info.size || !fi.ModTime().Equal(info.mtime) {
				changed = true
			}
			if !changed && time.Since(info.indexedAt) <= 2*time.Second {
				changed = true
			}
			if changed {
				dt.markDirty(rel)
			}

			return nil
		},
	)

	// Files no longer yielded by the walk are dirty and gone.
	for rel := range indexedFiles {
		if !seen[rel] {
			dt.markDirty(rel)
			dt.mu.Lock()
			dt.gone[rel] = true
			dt.mu.Unlock()
		}
	}

	// Clear revalidateAll only if it was set before the walk started
	// (no new Invalidate("") during the walk).
	dt.mu.Lock()
	if dt.revalidateAllGen <= genAtStart {
		dt.revalidateAll = false
	}
	// Mark all currently dirty paths as validated so they don't trigger
	// another revalidation walk.
	for rel := range dt.paths {
		dt.validated[rel] = true
	}
	dt.mu.Unlock()
}
