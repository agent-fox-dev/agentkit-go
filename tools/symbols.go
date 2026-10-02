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

// symbolTable is the in-memory per-file store of outline.File, size,
// modification time and index time behind find_symbol. It is created empty
// with the tool set, built on the first find_symbol call and discarded with
// the tool set.
type symbolTable struct {
	mu sync.Mutex

	// entries maps workspace-relative slash paths to their symbol entries.
	entries map[string]*symbolEntry

	// built is true once at least one build pass has run.
	built bool
	// complete is true when the last build pass finished without hitting a bound.
	complete bool

	// partialReason records why the last pass was partial ("files" or "time").
	partialReason string

	// dirtyPaths tracks workspace-relative paths that need re-indexing.
	dirtyPaths map[string]bool
	// revalidateAll is set when the whole table needs revalidation.
	revalidateAll bool

	// generation is advanced by every mark; a pass clears only marks that
	// predate its start.
	generation uint64

	// totalWalked counts the total regular files seen across all passes.
	totalWalked int
}

// newSymbolTable creates an empty symbol table. No walk, subprocess or file
// read happens at construction time.
func newSymbolTable() *symbolTable {
	return &symbolTable{
		entries: make(map[string]*symbolEntry),
	}
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
// table's lock by find_symbol before answering.
//
// Parameters:
//   - ctx: the call context; cancellation returns an error
//   - ft: the fileTools providing workspace, ignore and runner
//   - scopePath: when non-empty, only files under this directory are indexed
//     (but the walk starts from the workspace root for ignore layers)
func (st *symbolTable) buildOrRefresh(ctx context.Context, ft *fileTools, scopePath string) buildResult {
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
			// During revalidation (st.revalidateAll), the racy window
			// forces re-outline for recently indexed files. During a
			// build pass (table not yet complete), already-indexed
			// unchanged files are always skipped so repeated calls
			// make progress.
			if existing.size == fi.Size() && existing.mtime.Equal(fi.ModTime()) {
				if !st.revalidateAll || time.Since(existing.indexedAt) > 2*time.Second {
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
	runner := ft.outlineRunner()
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

		files, _, err := outline.OutlineMany(deadlineCtx, srcs, outline.Options{
			Root:   ft.ws.Root,
			Runner: runner,
		})
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

	// Drop entries for files not seen in a completed walk of the full workspace.
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
