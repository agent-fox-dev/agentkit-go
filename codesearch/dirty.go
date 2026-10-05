//go:build !windows

package codesearch

import (
	"bytes"
	"context"
	"hash/maphash"
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

	// validated tracks dirty paths whose current mark has been seen by a
	// revalidation walk. These don't trigger another revalidation even if
	// they're not in the original index. A new mark of a .gitignore clears
	// the entry, since each edit of it can change what the walk sees.
	validated map[string]bool

	// verified maps a racy indexed path to the indexedAt of the record whose
	// content a walk has compared with the indexed content after the file's
	// timestamp granule closed. Such a file cannot hide a same-mtime edit any
	// more, so later walks trust its (size, mtime). A rebuild writes new
	// records with a new indexedAt, which makes the entry stale.
	verified map[string]time.Time

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
		verified:  make(map[string]time.Time),
	}
}

// markDirty records a single path as dirty.
func (dt *dirtyTracker) markDirty(rel string) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.generation++
	dt.paths[rel] = dt.generation
	if isGitignore(rel) {
		delete(dt.validated, rel)
	}
}

// isGitignore reports whether rel names a .gitignore at any depth: the files
// whose edits can change which other files the walk yields.
func isGitignore(rel string) bool {
	return filepath.Base(rel) == ".gitignore"
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

// dirtyCount returns the number of dirty paths.
func (dt *dirtyTracker) dirtyCount() int {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	return len(dt.paths)
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

// racyWindow is the filesystem timestamp granularity: a file edited within it
// of the mtime recorded for it can end up with the same mtime again. It is the
// window 02_symbol_navigation_tools uses.
const racyWindow = 2 * time.Second

// contentSeed seeds contentHash. The hashes never leave the process.
var contentSeed = maphash.MakeSeed()

// contentHash is the hash recorded for an indexed file's content, to compare
// with the content a racy file has now.
func contentHash(b []byte) uint64 { return maphash.Bytes(contentSeed, b) }

// indexedFileInfo holds the (size, mtime) of a file at index time, when it
// was indexed, and a hash of the content that was indexed.
type indexedFileInfo struct {
	size      int64
	mtime     time.Time
	indexedAt time.Time
	hash      uint64
}

// racy reports whether the file's mtime lies within racyWindow of the moment
// it was indexed (or after it). A same-size edit made after the content was
// read can then leave (size, mtime) as they were, so they cannot vouch for it.
func (i indexedFileInfo) racy() bool {
	return i.indexedAt.Sub(i.mtime) <= racyWindow
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
		if isGitignore(rel) {
			return true
		}
		if !indexedFiles[rel] {
			return true
		}
	}
	return false
}

// racyEdit reports whether a file whose (size, mtime) match its record has
// changed anyway. Only a racy file can have: its content is compared with the
// hash recorded at index time, which is cheap next to marking the file dirty
// and, for a tree indexed just after it was written, next to marking all of
// it dirty. Once the comparison has been made after the file's timestamp
// granule closed, the result is remembered and the file is not read again.
func (dt *dirtyTracker) racyEdit(rel, abs string, info indexedFileInfo) bool {
	if !info.racy() {
		return false
	}
	dt.mu.Lock()
	verified := dt.verified[rel].Equal(info.indexedAt)
	dt.mu.Unlock()
	if verified {
		return false
	}

	content, err := os.ReadFile(abs)
	if err != nil || contentHash(content) != info.hash {
		return true
	}
	if time.Since(info.mtime) >= racyWindow {
		dt.mu.Lock()
		dt.verified[rel] = info.indexedAt
		dt.mu.Unlock()
	}
	return false
}

// revalidate walks the workspace and compares (size, mtime) with the indexed
// values; a file that matches is neither opened nor read, unless it is racy
// (see racyEdit). Changed, new and removed files are marked dirty. It clears
// the revalidateAll flag using generation-based clearing.
//
// It stops when ctx ends and returns ctx.Err(). A walk that stopped early
// cannot say which files are gone, so it applies nothing beyond the marks of
// files it saw: the whole-index mark stays set and the next call walks again.
func (dt *dirtyTracker) revalidate(
	ctx context.Context,
	ws *tools.Workspace,
	ig tools.IgnoreOptions,
	indexedFiles map[string]bool,
	fileInfos map[string]indexedFileInfo,
) error {
	// Snapshot the generation before the walk so we can clear
	// revalidateAll only if no new marks arrived during the walk.
	dt.mu.Lock()
	genAtStart := dt.generation
	dt.mu.Unlock()
	seen := make(map[string]bool)

	_ = tools.Walk(
		ctx,
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

			// An indexed file whose size and mtime are as recorded passed the
			// build's filters then and has not changed: it is seen, and is
			// not opened.
			info, indexed := fileInfos[rel]
			if indexed && fi.Size() == info.size && fi.ModTime().Equal(info.mtime) &&
				!dt.racyEdit(rel, abs, info) {
				seen[rel] = true
				return nil
			}

			// New or changed: skip oversized and binary files (same filters
			// as build).
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

			if !indexed {
				// New file: mark dirty and validated.
				dt.markDirty(rel)
				dt.mu.Lock()
				dt.validated[rel] = true
				dt.mu.Unlock()
				return nil
			}

			dt.markDirty(rel)
			return nil
		},
	)
	if err := ctx.Err(); err != nil {
		return err
	}

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
	// For every dirty path marked before the walk started, the walk is the
	// authority on visibility: a path it did not yield is gone (deleted,
	// ignored, hidden, binary or oversized) and one it did yield is not, so
	// a file a later .gitignore edit hides or un-hides is settled here. The
	// path is then validated, so it does not trigger another walk. A mark
	// made during the walk is left alone: the walk may have missed it.
	for rel, gen := range dt.paths {
		if gen > genAtStart {
			continue
		}
		dt.validated[rel] = true
		if seen[rel] {
			delete(dt.gone, rel)
		} else {
			dt.gone[rel] = true
		}
	}
	dt.mu.Unlock()

	return nil
}
