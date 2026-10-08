package tools

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/agentfox/agentkit-go/outline"
)

// symbolEntry is the per-file record in the symbol table.
type symbolEntry struct {
	file      outline.File
	size      int64
	mtime     time.Time
	indexedAt time.Time
}

// racyWindow is the filesystem timestamp granularity (1 to 2 s on some
// filesystems), the window of 02-REQ-6.6 and Design Decision 12.
const racyWindow = 2 * time.Second

// racy reports whether the file's mtime lies within racyWindow of the moment
// it was indexed. A same-size edit made in the same timestamp granule as the
// write the entry records leaves (size, mtime) as they were, so for such a
// file they cannot show that it is unchanged and a revalidation re-outlines it,
// whenever that revalidation happens. A re-outline records indexedAt as now,
// so once a revalidation is more than racyWindow past the file's mtime the
// file stops being racy.
func (e *symbolEntry) racy() bool {
	d := e.indexedAt.Sub(e.mtime)
	return d >= -racyWindow && d <= racyWindow
}

// symbolTable is the in-memory per-file store of outline.File, size,
// modification time and index time behind find_symbol. Invalidation
// hooks from fileTools (markTableDirty, markTableRevalidateAll) coordinate
// dirty state across both symbolTable and referenceCache. It is created empty
// with the tool set, built on the first find_symbol call and discarded with
// the tool set.
type symbolTable struct {
	// mu guards entries, built, complete and partialReason. It is a
	// buffered channel of size 1 used as a context-abandonable lock:
	// a waiter whose context ends returns aborted without waiting for
	// the build. Builds, refreshes and reading the table to answer a
	// query happen under it. 02-REQ-7.1, 02-REQ-7.2.
	mu chan struct{}

	// entries maps workspace-relative slash paths to their symbol entries.
	entries map[string]*symbolEntry

	// built is true once at least one build pass has run.
	built bool
	// complete is true when the last build pass finished without hitting a bound.
	complete bool

	// partialReason records why the last pass was partial ("files" or "time").
	partialReason string

	// markMu guards the dirty state: dirtyPaths, revalidateAll and
	// generation. It is separate from mu so that markDirty and
	// markRevalidateAll never block on a build in progress — they only
	// set a flag. 02-REQ-6.6.
	markMu sync.Mutex

	// dirtyPaths tracks workspace-relative paths that need re-indexing.
	dirtyPaths map[string]bool
	// revalidateAll is set when the whole table needs revalidation.
	revalidateAll bool

	// generation is advanced by every mark; a pass clears only marks that
	// predate its start. 02-REQ-6.8.
	generation uint64

	// totalWalked counts the total regular files seen across all passes.
	totalWalked int
}

// newSymbolTable creates an empty symbol table. No walk, subprocess or file
// read happens at construction time.
func newSymbolTable() *symbolTable {
	mu := make(chan struct{}, 1)
	mu <- struct{}{} // start unlocked
	return &symbolTable{
		mu:      mu,
		entries: make(map[string]*symbolEntry),
	}
}

// lock acquires the table lock, or returns false if ctx is cancelled first.
// The caller must call unlock() when done if lock returned true.
func (st *symbolTable) lock(ctx context.Context) bool {
	select {
	case <-st.mu:
		return true
	case <-ctx.Done():
		return false
	}
}

// lockBlocking acquires the table lock unconditionally. It is used by code
// paths that must not be abandoned (e.g. test helpers inspecting state).
func (st *symbolTable) lockBlocking() {
	<-st.mu
}

// unlock releases the table lock.
func (st *symbolTable) unlock() {
	st.mu <- struct{}{}
}

// markDirty marks a single workspace-relative path as needing re-indexing.
// It only sets a flag and never waits on a build in progress. 02-REQ-6.2.
func (st *symbolTable) markDirty(rel string) {
	st.markMu.Lock()
	defer st.markMu.Unlock()
	if st.dirtyPaths == nil {
		st.dirtyPaths = make(map[string]bool)
	}
	st.dirtyPaths[rel] = true
	st.generation++
}

// markRevalidateAll marks the whole table for revalidation. It only sets a
// flag and never waits on a build in progress. 02-REQ-6.3.
func (st *symbolTable) markRevalidateAll() {
	st.markMu.Lock()
	defer st.markMu.Unlock()
	st.revalidateAll = true
	st.generation++
}

// snapshotMarks reads and clears the dirty state under markMu, returning the
// dirty paths, whether revalidateAll was set, and the generation at the time
// of the snapshot. The caller must hold mu (the build lock).
func (st *symbolTable) snapshotMarks() (dirtyPaths map[string]bool, revalidateAll bool, generation uint64) {
	st.markMu.Lock()
	defer st.markMu.Unlock()
	dirtyPaths = st.dirtyPaths
	revalidateAll = st.revalidateAll
	generation = st.generation
	st.dirtyPaths = nil
	// revalidateAll is NOT cleared here; it is cleared only after a full
	// completed pass (02-REQ-6.7).
	return
}

// currentGeneration returns the generation counter as it is now.
func (st *symbolTable) currentGeneration() uint64 {
	st.markMu.Lock()
	defer st.markMu.Unlock()
	return st.generation
}

// clearRevalidateAll clears the whole-table mark if no new marks have arrived
// since genAtStart. Must be called under markMu.
func (st *symbolTable) clearRevalidateAllIfUnchanged(genAtStart uint64) {
	st.markMu.Lock()
	defer st.markMu.Unlock()
	if st.generation == genAtStart {
		st.revalidateAll = false
	}
}

// needsRefresh returns true if the table needs a build or refresh before
// answering a query.
func (st *symbolTable) needsRefresh() bool {
	st.lockBlocking()
	built := st.built
	complete := st.complete
	st.unlock()

	st.markMu.Lock()
	revalAll := st.revalidateAll
	hasDirty := len(st.dirtyPaths) > 0
	st.markMu.Unlock()

	return !built || !complete || revalAll || hasDirty
}

// refreshDirtyPaths handles dirty paths without a full walk. It is called
// under the table's mu lock when the table is complete and only specific paths
// are dirty (no whole-table revalidation needed).
//
// For each dirty path:
//   - If it is already in the table: stat it, re-outline if still regular,
//     drop if gone or not regular.
//   - If it is NOT in the table or has base name .gitignore: escalate to
//     whole-table revalidation (return false).
//
// When ctx ends during the refresh, the paths not yet refreshed keep their
// entries and are marked dirty again, so the next call refreshes them; a
// cancelled call never drops a file that is still there (02-REQ-6.4,
// 02-REQ-7.4). The caller answers aborted.
//
// Returns true if all dirty paths were handled without escalation.
func (st *symbolTable) refreshDirtyPaths(ctx context.Context, ft *fileTools, paths map[string]bool) bool {
	if len(paths) == 0 {
		return true
	}

	// Check if any dirty path requires escalation.
	for rel := range paths {
		// A .gitignore change can affect which files are visible.
		if filepath.Base(rel) == ".gitignore" {
			st.markRevalidateAll()
			return false
		}
		// A path not in the table is a new file; only the walk's ignore
		// engine can say whether it is visible.
		if _, ok := st.entries[rel]; !ok {
			st.markRevalidateAll()
			return false
		}
	}

	// All dirty paths are known files. Re-outline or drop each one.
	pending := make(map[string]bool, len(paths))
	for rel := range paths {
		pending[rel] = true
	}
	// remark gives back the marks of the paths this call did not get to: the
	// snapshot that handed them over has already cleared them.
	remark := func() {
		for rel := range pending {
			st.markDirty(rel)
		}
	}
	for rel := range paths {
		if ctx.Err() != nil {
			remark()
			return true
		}

		abs := filepath.Join(ft.ws.Root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil || !fi.Mode().IsRegular() {
			// File is gone or no longer regular: drop it.
			delete(st.entries, rel)
			delete(pending, rel)
			continue
		}

		// Re-outline the file.
		ofile, err := outline.Outline(ctx, abs, nil, outline.Options{Root: ft.ws.Root})
		if err != nil {
			if ctx.Err() != nil {
				// The call ended, not the file: keep the entry and the mark.
				remark()
				return true
			}
			// On error, drop the entry rather than serving stale data.
			delete(st.entries, rel)
			delete(pending, rel)
			continue
		}

		now := time.Now()
		st.entries[rel] = &symbolEntry{
			file:      ofile,
			size:      fi.Size(),
			mtime:     fi.ModTime(),
			indexedAt: now,
		}
		delete(pending, rel)
	}

	return true
}

// computeMetrics returns the current table metrics without any walk or outline.
// The caller must hold st.mu.
func (st *symbolTable) computeMetrics(scopePrefix string) buildResult {
	result := buildResult{
		backends: make(map[string]int),
	}
	for rel, entry := range st.entries {
		if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
			continue
		}
		result.filesIndexed++
		result.backends[string(entry.file.Backend)]++
	}

	if !st.complete {
		result.partial = true
		result.partialReason = st.partialReason
	}

	return result
}

// buildResult carries the outcome of a build or refresh pass.
type buildResult struct {
	partial       bool
	partialReason string // "files" or "time"
	filesIndexed  int
	backends      map[string]int
}

// defaultMaxFiles is the file-count bound when SymbolOptions.MaxFiles is zero
// or negative.
const defaultMaxFiles = 50000

// defaultMaxDuration is the wall-time bound when SymbolOptions.MaxDuration is
// zero or negative.
const defaultMaxDuration = 2 * time.Second

// outlineBatchSize is the maximum number of files passed to OutlineMany per call.
const outlineBatchSize = 100

// walkFn is the walk function used by the symbol table. It is a variable so
// tests can replace it with a recording implementation.
var walkFn = Walk

// buildOrRefresh builds or refreshes the symbol table. It is called under the
// table's mu lock by find_symbol before answering.
//
// Parameters:
//   - ctx: the call context; cancellation returns an error
//   - ft: the fileTools providing workspace and ignore
//   - scopePath: when non-empty, only files under this directory are indexed
//     (but the walk starts from the workspace root for ignore layers)
//   - revalidating: true when the pass is a revalidation (whole-table mark
//     was set), which enables the racy-mtime window check
//   - genAtStart: the generation counter value at the start of this pass,
//     used to decide which marks to clear at the end
func (st *symbolTable) buildOrRefresh(ctx context.Context, ft *fileTools, scopePath string, revalidating bool, genAtStart uint64) buildResult {
	maxFiles := ft.symOpts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	maxDur := ft.symOpts.MaxDuration
	if maxDur <= 0 {
		maxDur = defaultMaxDuration
	}

	// Create a derived context with the time bound.
	deadlineCtx, cancel := context.WithTimeout(ctx, maxDur)
	defer cancel()

	result := buildResult{
		backends: make(map[string]int),
	}

	// Determine the scope prefix for path filtering.
	var scopePrefix string
	if scopePath != "" {
		// scopePath is an absolute path; convert to workspace-relative.
		scopePrefix = filepath.ToSlash(ft.ws.Rel(scopePath))
		if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
			scopePrefix += "/"
		}
	}

	// Collect files to outline.
	type fileToIndex struct {
		rel string
		abs string
	}
	var toIndex []fileToIndex
	filesSeen := 0
	hitFileLimit := false

	// Track which files we see in this walk (for dropping stale entries).
	seen := make(map[string]bool)

	walkErr := walkFn(deadlineCtx, ft.ws, ft.ws.Root, WalkOptions{
		Ignore:        ft.ig,
		IncludeHidden: false,
	}, func(rel string, d fs.DirEntry) error {
		if deadlineCtx.Err() != nil {
			return deadlineCtx.Err()
		}

		if d.IsDir() {
			// When the table is incomplete and a scope path is given,
			// skip directories that are neither ancestors of nor inside
			// the scope path.
			if scopePrefix != "" && !st.complete {
				dirRel := rel + "/"
				// Keep if: dirRel is a prefix of scopePrefix (ancestor)
				// or scopePrefix is a prefix of dirRel (inside scope)
				isAncestor := strings.HasPrefix(scopePrefix, dirRel)
				isInside := strings.HasPrefix(dirRel, scopePrefix)
				if !isAncestor && !isInside {
					return filepath.SkipDir
				}
			}
			return nil
		}

		// Only regular files.
		if !d.Type().IsRegular() {
			return nil
		}

		// Apply scope filter for files.
		if scopePrefix != "" {
			if !strings.HasPrefix(rel, scopePrefix) {
				return nil
			}
		}

		seen[rel] = true

		// Check if the file is already indexed and unchanged.
		abs := filepath.Join(ft.ws.Root, filepath.FromSlash(rel))
		fi, err := os.Stat(abs)
		if err != nil {
			return nil // skip unreadable files
		}

		if existing, ok := st.entries[rel]; ok {
			// Skip if size and mtime are unchanged.
			// During revalidation, the racy window (02-REQ-6.6) forces
			// re-outline for files whose mtime is within 2 s of their
			// indexedAt, however long ago that was. During a build pass
			// (table not yet complete), already-indexed unchanged files are
			// always skipped so repeated calls make progress.
			if existing.size == fi.Size() && existing.mtime.Equal(fi.ModTime()) {
				if !revalidating || !existing.racy() {
					return nil
				}
			}
		}

		// Count every regular file the walk yields that needs indexing
		// toward the file limit. Already-indexed unchanged files were
		// skipped above and do not count, so repeated calls make progress.
		filesSeen++
		if filesSeen > maxFiles {
			hitFileLimit = true
			return filepath.SkipAll
		}

		toIndex = append(toIndex, fileToIndex{rel: rel, abs: abs})
		return nil
	})

	// If the caller's context was cancelled, return immediately.
	if walkErr != nil && ctx.Err() != nil {
		return result
	}

	// Check if the time bound was hit during the walk.
	if deadlineCtx.Err() != nil && !hitFileLimit {
		result.partial = true
		result.partialReason = "time"
	}

	// Process files in batches of outlineBatchSize.
	for i := 0; i < len(toIndex); i += outlineBatchSize {
		if deadlineCtx.Err() != nil {
			// Time bound hit during outlining.
			if !result.partial {
				result.partial = true
				result.partialReason = "time"
			}
			break
		}

		end := i + outlineBatchSize
		if end > len(toIndex) {
			end = len(toIndex)
		}
		batch := toIndex[i:end]

		srcs := make([]outline.Source, len(batch))
		for j, f := range batch {
			srcs[j] = outline.Source{Abs: f.abs}
		}
		if ft.testOutlineHook != nil {
			ft.testOutlineHook(deadlineCtx)
		}

		files, err := outline.OutlineMany(deadlineCtx, srcs, outline.Options{Root: ft.ws.Root})
		if err != nil {
			// If the deadline caused the error, mark as partial.
			if deadlineCtx.Err() != nil {
				if !result.partial {
					result.partial = true
					result.partialReason = "time"
				}
				break
			}
			// Other errors: skip this batch.
			continue
		}

		now := time.Now()
		for j, f := range files {
			rel := batch[j].rel
			abs := batch[j].abs
			fi, err := os.Stat(abs)
			if err != nil {
				continue
			}
			st.entries[rel] = &symbolEntry{
				file:      f,
				size:      fi.Size(),
				mtime:     fi.ModTime(),
				indexedAt: now,
			}
		}
	}

	if hitFileLimit {
		result.partial = true
		result.partialReason = "files"
	}

	// Drop entries for files not seen in a completed walk of the full
	// workspace. A pass stopped at a bound never drops entries (02-REQ-6.6).
	// A path-scoped pass also never drops entries outside its scope.
	if !result.partial && scopePrefix == "" {
		for rel := range st.entries {
			if !seen[rel] {
				delete(st.entries, rel)
			}
		}
	}

	st.built = true
	if !result.partial {
		if scopePrefix == "" {
			st.complete = true
			// Clear the whole-table mark only when the pass covered the
			// whole workspace and completed within bounds, and no new
			// marks arrived during the pass. 02-REQ-6.7, 02-REQ-6.8.
			st.clearRevalidateAllIfUnchanged(genAtStart)
		}
	}
	st.partialReason = result.partialReason

	// Compute result metrics from the current table state.
	for rel, entry := range st.entries {
		// If scope is set, only count files in scope.
		if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
			continue
		}
		result.filesIndexed++
		result.backends[string(entry.file.Backend)]++
	}

	return result
}
