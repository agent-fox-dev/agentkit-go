//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/tools"

	zoekt "github.com/sourcegraph/zoekt"
	"github.com/sourcegraph/zoekt/query"
	"github.com/sourcegraph/zoekt/search"
)

// maxQueryBytes is the maximum length of a query string.
const maxQueryBytes = 1024

// defaultMaxFiles is the default max_files for code_search results.
const defaultMaxFiles = 10

// maxMaxFiles is the cap for max_files.
const maxMaxFiles = 25

// defaultContextLines is the default context_lines for code_search results.
const defaultContextLines = 2

// defaultQueryTimeout is the default maximum wall time for a single search.
const defaultQueryTimeout = 10 * time.Second

// codeSearchDescription is the tool description.
const codeSearchDescription = `Search the codebase using zoekt query syntax. Returns ranked, file-grouped results.

Examples:
  sym:Runner
  retry file:^src/ -file:test
  lang:python "def load"
  (compaction or summarize) case:no`

// codeSearchGuideline is the PromptGuideline for code_search.
const codeSearchGuideline = "Use code_search for ranked questions about the codebase; search_files for an exhaustive regex scan."

// Tools implements tools.Index. It returns the code_search tool.
func (idx *Index) Tools() []core.Tool {
	return []core.Tool{idx.codeSearchTool()}
}

// codeSearchTool builds the code_search tool definition.
func (idx *Index) codeSearchTool() core.Tool {
	return core.Tool{
		Name:        "code_search",
		Description: codeSearchDescription,
		Builtin:     true,
		// ExecutionMode defaults to Parallel (zero value).
		InputSchema: schema.Object(
			schema.Prop("query", schema.String("The search query in zoekt query syntax.")),
			schema.Opt("path", schema.String("Restrict search to this directory or file path.")),
			schema.Opt("max_files", schema.Int("Maximum number of files to return.")),
			schema.Opt("context_lines", schema.Int("Number of context lines around each match.")),
		),
		Execute:          idx.executeCodeSearch,
		PromptGuidelines: []string{codeSearchGuideline},
	}
}

// codeSearchArgs are the parsed arguments for code_search.
type codeSearchArgs struct {
	Query        string `json:"query"`
	Path         string `json:"path"`
	MaxFiles     int    `json:"max_files"`
	ContextLines *int   `json:"context_lines"`
}

// executeCodeSearch is the Execute handler for code_search.
func (idx *Index) executeCodeSearch(ctx context.Context, in json.RawMessage) core.ToolResult {
	// Check for cancelled context early.
	if err := ctx.Err(); err != nil {
		return core.ErrResult("aborted", "Operation aborted")
	}

	// Parse arguments.
	var a codeSearchArgs
	if err := json.Unmarshal(in, &a); err != nil {
		return core.ErrResult("invalid_arguments", err.Error())
	}

	// Validate query: empty after trimming.
	trimmed := strings.TrimSpace(a.Query)
	if trimmed == "" {
		return core.ErrResult("invalid_arguments", "query must not be empty")
	}

	// Validate query: over 1024 bytes.
	if len(a.Query) > maxQueryBytes {
		return core.ErrResult("invalid_arguments",
			fmt.Sprintf("query exceeds %d byte limit (%d bytes)", maxQueryBytes, len(a.Query)))
	}

	// Validate context_lines: negative is invalid.
	if a.ContextLines != nil && *a.ContextLines < 0 {
		return core.ErrResult("invalid_arguments", "context_lines must not be negative")
	}

	// Parse the query with zoekt's parser.
	parsedQ, err := query.Parse(trimmed)
	if err != nil {
		return core.ErrResult("invalid_arguments", fmt.Sprintf("query parse error: %s", err.Error()))
	}

	// Default and clamp context_lines and max_files.
	// Only an ABSENT context_lines takes the default; 0 means no context,
	// as it does for search_files.
	contextLines := defaultContextLines
	if a.ContextLines != nil {
		contextLines = *a.ContextLines
	}
	if contextLines > tools.MaxSearchContextLines {
		contextLines = tools.MaxSearchContextLines
	}
	maxFiles := tools.ClampLimit(a.MaxFiles, defaultMaxFiles, maxMaxFiles)

	// Validate and resolve path before any build.
	var pathQuery query.Q
	if a.Path != "" {
		abs, err := idx.ws.Resolve(a.Path)
		if err != nil {
			return core.ErrResult("path_not_allowed", err.Error())
		}
		fi, err := os.Stat(abs)
		if err != nil {
			return core.ErrResult("read_failed", err.Error())
		}

		rel := filepath.ToSlash(idx.ws.Rel(abs))

		// Build the file: constraint as a query node.
		if rel != "." && rel != "" {
			pathQ, err := pathConstraint(rel, fi.IsDir())
			if err != nil {
				return core.ErrResult("invalid_arguments",
					fmt.Sprintf("path constraint error: %s", err.Error()))
			}
			pathQuery = pathQ
		}
		// Root adds no constraint.
	}

	// Check for cancelled context again before building.
	if err := ctx.Err(); err != nil {
		return core.ErrResult("aborted", "Operation aborted")
	}

	// Check if closed.
	idx.mu.RLock()
	closed := idx.closed
	idx.mu.RUnlock()
	if closed {
		return core.ErrResult("index_closed", "index has been closed")
	}

	// Ensure the index is built (lazy build on first call).
	if err := idx.ensureBuilt(ctx); err != nil {
		if ctx.Err() != nil {
			return core.ErrResult("aborted", "Operation aborted")
		}
		errStr := err.Error()
		if strings.HasPrefix(errStr, "index_failed:") || strings.HasPrefix(errStr, "index_closed") {
			code := "index_failed"
			detail := strings.TrimPrefix(errStr, "index_failed: ")
			if strings.HasPrefix(errStr, "index_closed") {
				code = "index_closed"
				detail = "index has been closed"
			}
			return core.ErrResult(code, detail)
		}
		return core.ErrResult("index_failed", errStr)
	}

	// Handle dirty files: revalidation and dirty-path processing. A call whose
	// context ends during the walk is aborted.
	if err := idx.handleDirty(ctx); err != nil {
		return core.ErrResult("aborted", "Operation aborted")
	}

	// Check if a rebuild is needed (>5% dirty) or in progress.
	if err := idx.waitForRebuildIfNeeded(ctx); err != nil {
		if ctx.Err() != nil {
			return core.ErrResult("aborted", "Operation aborted")
		}
		errStr := err.Error()
		if strings.HasPrefix(errStr, "index_closed") {
			return core.ErrResult("index_closed", "index has been closed")
		}
		return core.ErrResult("index_failed", errStr)
	}

	// Keep the run directory young for a sibling index's sweep.
	idx.touchRunDir()

	// Build the final query with path conjunction.
	finalQ := parsedQ
	if pathQuery != nil {
		finalQ = query.NewAnd(parsedQ, pathQuery)
	}

	// Track this query as in-flight so Close waits for it.
	idx.inFlight.Add(1)
	defer idx.inFlight.Done()

	// Re-check closed after registering in-flight (Close sets closed
	// before waiting for in-flight to drain).
	idx.mu.RLock()
	if idx.closed {
		idx.mu.RUnlock()
		return core.ErrResult("index_closed", "index has been closed")
	}
	idx.mu.RUnlock()

	// Search with the test hook or the real searcher.
	queryTimeout := idx.queryTimeout
	if queryTimeout <= 0 {
		queryTimeout = defaultQueryTimeout
	}

	searchCtx, searchCancel := context.WithTimeout(ctx, queryTimeout)
	defer searchCancel()

	var resultFiles []searchResultFile
	var totalFiles int

	// The dirty set is read once, before the search: the indexed hits are
	// filtered against it before the page is cut, and the overlay searches
	// the same set.
	dirtySet := idx.dirty.dirtyPathSet()

	if idx.testSearchHook != nil {
		hookResult, err := idx.testSearchHook(searchCtx, trimmed)
		if err != nil {
			if searchCtx.Err() != nil && ctx.Err() == nil {
				// The search timed out but the parent context is still alive.
				return core.ErrResult("search_failed",
					"query timed out; narrow the query or add a path constraint")
			}
			if ctx.Err() != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}
			return core.ErrResult("search_failed", err.Error())
		}
		if hookResult != nil {
			resultFiles = hookResult.files
			totalFiles = hookResult.totalFiles
			if totalFiles < len(resultFiles) {
				totalFiles = len(resultFiles)
			}
		}
	} else {
		// Real zoekt search.
		files, total, err := idx.searchZoekt(searchCtx, finalQ, maxFiles, contextLines, dirtySet)
		if err != nil {
			if searchCtx.Err() != nil && ctx.Err() == nil {
				return core.ErrResult("search_failed",
					"query timed out; narrow the query or add a path constraint")
			}
			if ctx.Err() != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}
			return core.ErrResult("search_failed", err.Error())
		}
		resultFiles = files
		totalFiles = total
	}

	// Remove dirty paths from indexed results and search dirty files.
	var overlayNote string
	if len(dirtySet) > 0 {
		resultFiles, totalFiles, overlayNote = idx.filterAndSearchDirty(searchCtx, resultFiles, totalFiles, dirtySet, finalQ, maxFiles, contextLines)
	}

	// Build the result data.
	return idx.buildResult(resultFiles, maxFiles, totalFiles, overlayNote)
}

// ensureBuilt triggers a lazy build if the index has not been built yet.
// A partial index is not retried; only the dirty-threshold rebuild replaces it.
// A call arriving during the first build waits for it and abandons the wait
// when ctx ends.
func (idx *Index) ensureBuilt(ctx context.Context) error {
	return idx.build(ctx, true)
}

// handleDirty runs revalidation if needed. It checks whether a revalidation
// walk is required (unknown paths, .gitignore changes, or Invalidate("")),
// and if so, walks the workspace to discover changed, new and removed files.
// It returns ctx.Err() when ctx ends during the walk.
func (idx *Index) handleDirty(ctx context.Context) error {
	if !idx.dirty.hasDirty() {
		return nil
	}

	idx.mu.RLock()
	indexedFiles := idx.indexedFiles
	fileInfos := idx.fileInfos
	idx.mu.RUnlock()

	if idx.dirty.needsRevalidation(indexedFiles) {
		if idx.testRevalHook != nil {
			idx.testRevalHook()
		}
		if err := idx.dirty.revalidate(ctx, idx.ws, idx.opts.Ignore, indexedFiles, fileInfos); err != nil {
			return err
		}
		idx.revalCount.Add(1)

		// A walk can hide or reveal files without changing the dirty set the
		// overlay is keyed on, so the cached overlay may be stale.
		idx.mu.Lock()
		ov := idx.overlay
		idx.overlay = nil
		idx.mu.Unlock()
		idx.retireOverlay(ov)
	}
	return nil
}

// rebuildThreshold is the fraction of indexed files that must be dirty before
// a full rebuild is triggered instead of using an overlay.
const rebuildThreshold = 0.05

// waitForRebuildIfNeeded checks whether more than 5% of indexed files are
// dirty and, if so, triggers a full rebuild. If a rebuild is already in
// progress, the caller waits abandonably (ctx select) and uses the new index.
// Returns nil when the index is ready for querying.
func (idx *Index) waitForRebuildIfNeeded(ctx context.Context) error {
	for {
		idx.mu.Lock()
		if idx.closed {
			idx.mu.Unlock()
			return fmt.Errorf("index_closed")
		}

		// If a rebuild is already in progress, wait for it.
		if idx.rebuilding {
			done := idx.rebuildDone
			idx.mu.Unlock()
			select {
			case <-done:
				// Rebuild finished; loop to re-check state.
				continue
			case <-ctx.Done():
				return fmt.Errorf("aborted: %w", ctx.Err())
			}
		}

		// Check if we need a rebuild.
		indexedCount := len(idx.indexedFiles)
		if indexedCount == 0 {
			idx.mu.Unlock()
			return nil
		}

		dirtyCount := idx.dirty.dirtyCount()
		threshold := float64(indexedCount) * rebuildThreshold
		if float64(dirtyCount) <= threshold {
			idx.mu.Unlock()
			return nil
		}

		// Need a rebuild. Mark as rebuilding and start.
		idx.rebuilding = true
		idx.rebuildDone = make(chan struct{})
		oldRunDir := idx.runDir
		oldOverlay := idx.overlay
		idx.overlay = nil
		idx.mu.Unlock()

		// Perform the rebuild.
		err := idx.rebuild(ctx, oldRunDir, oldOverlay)

		idx.mu.Lock()
		idx.rebuilding = false
		close(idx.rebuildDone)
		idx.mu.Unlock()

		if err != nil {
			return err
		}
		return nil
	}
}

// rebuild performs a full index rebuild under the Requirement 5 bounds.
// It builds into a new run state without holding the main lock during the
// build, swaps only when ready, and cleans up the old shards after the swap.
func (idx *Index) rebuild(ctx context.Context, oldRunDir string, oldOverlay *overlayState) error {
	// Snapshot the dirty generation so we can clear only marks older than
	// this rebuild when it finishes.
	_, _, rebuildGenStart := idx.dirty.snapshot()

	// Create a derived context with the wall-time bound.
	buildCtx, buildCancel := context.WithTimeout(ctx, idx.opts.MaxBuildTime)
	defer buildCancel()

	// Generate a new run ID for the rebuild.
	rebuildRunID := newRunID()
	rebuildRunDir := filepath.Join(idx.hashDirPath(), rebuildRunID)

	hashDir := idx.hashDirPath()
	if err := os.MkdirAll(hashDir, 0o700); err != nil {
		return fmt.Errorf("index_failed: %w", err)
	}
	if err := os.Mkdir(rebuildRunDir, 0o700); err != nil {
		return fmt.Errorf("index_failed: %w", err)
	}

	// Walk, outline and build — all without holding the main lock.
	result, err := idx.doBuild(buildCtx, ctx, rebuildRunDir)
	if err != nil {
		os.RemoveAll(rebuildRunDir)
		return err
	}

	// Swap in the new index state under the lock — unless the index was
	// closed meanwhile: Close has already taken the run directory it will
	// remove, and a new one published now would never be removed.
	idx.mu.Lock()
	if idx.closed {
		idx.mu.Unlock()
		os.RemoveAll(rebuildRunDir)
		return fmt.Errorf("index_closed")
	}
	idx.runID = rebuildRunID
	idx.runDir = rebuildRunDir
	idx.lastTouch = time.Now()
	idx.indexedFiles = result.indexedFiles
	idx.outlineFiles = result.outlineFiles
	idx.stats = result.stats
	idx.fileInfos = result.fileInfos
	idx.built = true
	idx.partial = result.partial
	idx.partialReason = result.partialReason
	idx.ctagsAvailable = result.ctagsAvailable
	idx.overlay = nil
	idx.buildCount.Add(1)
	idx.mu.Unlock()

	// Clear dirty marks that predate this rebuild.
	idx.dirty.clearOlderThan(rebuildGenStart)

	// Retire the old run directory and overlay: removed now, or when the
	// last query that read them before the swap is done.
	idx.retireOverlay(oldOverlay)
	idx.retireDir(oldRunDir)

	return nil
}

// filterAndSearchDirty removes dirty paths from indexed results and searches
// dirty files through an overlay shard built by the same zoekt engine.
// Overlay hits are merged with indexed hits by score, not appended.
func (idx *Index) filterAndSearchDirty(
	ctx context.Context,
	resultFiles []searchResultFile,
	totalFiles int,
	dirtySet map[string]bool,
	q query.Q,
	maxFiles, contextLines int,
) ([]searchResultFile, int, string) {
	// Remove dirty paths from indexed results.
	var clean []searchResultFile
	for _, f := range resultFiles {
		if !dirtySet[f.path] {
			clean = append(clean, f)
		}
	}

	// Adjust totalFiles: subtract dirty files that were in the results.
	removed := len(resultFiles) - len(clean)
	totalFiles -= removed
	if totalFiles < 0 {
		totalFiles = 0
	}
	capped := func() []searchResultFile {
		if len(clean) > maxFiles {
			return clean[:maxFiles]
		}
		return clean
	}
	// A failure here loses the fresh hits of every edited file, so it is
	// said, not swallowed: the model would otherwise read "no match" for a
	// file it just changed.
	unsearched := func(err error) string {
		return fmt.Sprintf("%d edited file(s) could not be searched (%v); use search_files for them.",
			len(dirtySet), err)
	}

	// Build or reuse the overlay shard, ONE query at a time: the check, the
	// build and the store are a single step. Unserialised, concurrent queries
	// over one dirty set each built their own overlay, and one that saw a
	// changed set retired the shard another was about to open. The wait is
	// abandonable. The overlay in use is leased, with the run directory it
	// lives in, for as long as this query reads it.
	select {
	case idx.overlaySem <- struct{}{}:
	case <-ctx.Done():
		return capped(), totalFiles, unsearched(ctx.Err())
	}
	idx.mu.Lock()
	ov := idx.overlay
	reusable := ov != nil && dirtySetEqual(ov.dirtySet, dirtySet)
	if reusable {
		defer idx.leaseDir(ov.runDir)()
		defer idx.leaseDir(ov.dir)()
	}
	idx.mu.Unlock()

	if !reusable {
		// Retire the old overlay; a query still reading it keeps it.
		idx.retireOverlay(ov)

		newOv, err := idx.buildOverlayShard(ctx, dirtySet)
		if err != nil {
			<-idx.overlaySem
			return capped(), totalFiles, unsearched(err)
		}
		ov = newOv
		idx.mu.Lock()
		idx.overlay = ov
		defer idx.leaseDir(ov.runDir)()
		defer idx.leaseDir(ov.dir)()
		idx.mu.Unlock()
		idx.overlayBuildCount.Add(1)
	}
	<-idx.overlaySem

	// Search the overlay shard.
	note := ""
	if ov.dir != "" {
		overlayFiles, err := searchOverlay(ctx, ov.dir, q, maxFiles, contextLines, ov.outlineFiles)
		switch {
		case err != nil:
			note = unsearched(err)
		case len(overlayFiles) > 0:
			// Merge by score.
			clean = mergeByScore(clean, overlayFiles, maxFiles)
			// Adjust totalFiles for overlay hits.
			totalFiles += len(overlayFiles)
		}
	}

	return capped(), totalFiles, note
}

// sortResultLines sorts result lines by line number.
func sortResultLines(lines []resultLine) {
	for i := 1; i < len(lines); i++ {
		for j := i; j > 0 && lines[j].lineNo < lines[j-1].lineNo; j-- {
			lines[j], lines[j-1] = lines[j-1], lines[j]
		}
	}
}

// searchZoekt performs the actual zoekt search and returns the total number
// of files that matched (which may exceed maxFiles).
//
// Dirty paths are dropped BEFORE the page is cut, and zoekt is asked for
// enough files to fill the page without them. Cutting first and filtering
// after shrank the page by every dirty file among the top hits, and took the
// cap marker with it while more files still matched.
func (idx *Index) searchZoekt(ctx context.Context, q query.Q, maxFiles, contextLines int, dirty map[string]bool) ([]searchResultFile, int, error) {
	idx.mu.RLock()
	runDir := idx.runDir
	outlineFiles := idx.outlineFiles
	// Leased under the same lock that read the path: a rebuild swapping
	// the run directory out now leaves it on disk until this search is done.
	release := idx.leaseDir(runDir)
	idx.mu.RUnlock()
	defer release()

	if runDir == "" {
		return nil, 0, fmt.Errorf("no index directory")
	}

	searcher, err := search.NewDirectorySearcher(runDir)
	if err != nil {
		return nil, 0, fmt.Errorf("open searcher: %w", err)
	}
	defer searcher.Close()

	// Ask for more files than maxFiles so we can detect truncation.
	opts := &zoekt.SearchOptions{
		MaxDocDisplayCount: maxFiles + 1 + len(dirty),
		NumContextLines:    contextLines,
		ChunkMatches:       true,
		MaxWallTime:        idx.effectiveQueryTimeout(),
	}

	result, err := searcher.Search(ctx, q, opts)
	if err != nil {
		return nil, 0, err
	}

	clean := result.Files[:0]
	for _, fm := range result.Files {
		if !dirty[fm.FileName] {
			clean = append(clean, fm)
		}
	}
	totalFiles := len(clean)
	files := convertFileMatches(clean, maxFiles, contextLines, outlineFiles)

	return files, totalFiles, nil
}

// effectiveQueryTimeout returns the query timeout to use.
func (idx *Index) effectiveQueryTimeout() time.Duration {
	if idx.queryTimeout > 0 {
		return idx.queryTimeout
	}
	return defaultQueryTimeout
}

// buildResult constructs the ToolResult from search results.
func (idx *Index) buildResult(files []searchResultFile, maxFiles, totalFiles int, overlayNote string) core.ToolResult {
	stats := idx.BuildStats()

	// Gather result info for the first line.
	info := idx.gatherResultInfo(stats)

	// Determine if more files matched than max_files.
	moreThanMax := totalFiles > maxFiles

	// The cap marker is appended after the byte cap is applied, so its room is
	// taken out of the budget first: a result that fits the limit stays within
	// it once the marker is on.
	var capMarkerText string
	budget := tools.DefaultByteLimit
	if moreThanMax {
		capMarkerText = tools.CapMarker("files", "max_files", maxFiles, maxMaxFiles, "narrow the query or add a path")
		budget -= len(capMarkerText) + 1
	}

	// Build the text.
	text := renderResult(files, info)

	// Apply byte cap.
	var bytesTruncated bool
	var bytesMarkerText string
	files, bytesMarkerText, bytesTruncated = applyByteCap(files, info, budget)
	if bytesTruncated {
		// Re-render with the truncated files.
		text = renderResult(files, info)
	}

	// Append markers to text.
	if bytesTruncated {
		text += "\n" + bytesMarkerText
	} else if capMarkerText != "" {
		text += "\n" + capMarkerText
	}
	if overlayNote != "" {
		text += "\n" + overlayNote
	}

	// Last resort, for a result the byte cap could not fit: keep whole lines
	// only, so no line or rune is split, and end with the bytes marker.
	if len(text) > tools.DefaultByteLimit {
		if bytesMarkerText == "" {
			bytesMarkerText = bytesMarker()
		}
		text = cutLines(text, tools.DefaultByteLimit-len(bytesMarkerText)-1) + "\n" + bytesMarkerText
		bytesTruncated = true
	}

	// Build structured data.
	fileData := make([]any, 0, len(files))
	for _, f := range files {
		chunkData := make([]any, 0, len(f.chunks))
		for _, chunk := range f.chunks {
			lineData := make([]any, 0, len(chunk.lines))
			for _, line := range chunk.lines {
				lineData = append(lineData, map[string]any{
					"line":  line.lineNo,
					"text":  line.text,
					"match": line.match,
				})
			}
			chunkData = append(chunkData, map[string]any{
				"lines": lineData,
			})
		}
		fileData = append(fileData, map[string]any{
			"path":    f.path,
			"score":   f.score,
			"matches": f.matchCount,
			"chunks":  chunkData,
			"symbols": f.symbols,
		})
	}

	note := ""
	if capMarkerText != "" {
		note = capMarkerText
	}
	if bytesMarkerText != "" {
		note = bytesMarkerText
	}
	// A partial index adds a note telling the model to narrow path or use
	// search_files, next to the truncation marker when the result has one.
	if info.partial {
		partialNote := fmt.Sprintf("Index is partial (%s limit). Narrow the path argument or use search_files for a full scan.", info.partialReason)
		if note == "" {
			note = partialNote
		} else {
			note += "\n" + partialNote
		}
	}
	if overlayNote != "" {
		if note != "" {
			note += "\n"
		}
		note += overlayNote
	}

	data := map[string]any{
		"files":           fileData,
		"truncated":       moreThanMax || bytesTruncated,
		"note":            note,
		"partial":         info.partial,
		"partial_reason":  info.partialReason,
		"symbol_sources":  info.symbolSources,
		"ctags_available": info.ctagsAvailable,
		"files_indexed":   stats.FilesIndexed,
		"dirty_files":     info.dirtyFiles,
		"skipped": map[string]any{
			"binary":            stats.BinarySkipped,
			"oversized":         stats.OversizedSkipped,
			"too_many_trigrams": stats.TooManyTrigramsSkipped,
			"too_small":         stats.TooSmallSkipped,
		},
	}

	r := core.OKResult(data)
	r.Text = text

	// Set metadata.
	if bytesTruncated {
		r.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: string(tools.TruncatedByBytes),
		}
	} else if moreThanMax {
		r.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: string(tools.TruncatedByLines),
		}
	}

	return r
}

// gatherResultInfo collects metadata for the result first line.
func (idx *Index) gatherResultInfo(stats BuildStatsResult) resultInfo {
	idx.mu.RLock()
	outlineFiles := idx.outlineFiles
	partial := idx.partial
	partialReason := idx.partialReason
	ctagsAvailable := idx.ctagsAvailable
	idx.mu.RUnlock()

	// Count symbol sources.
	symbolSources := make(map[string]int)
	for _, of := range outlineFiles {
		backend := string(of.Backend)
		if backend == "" {
			backend = "none"
		}
		symbolSources[backend]++
	}

	// Calculate index size from shard files. The run directory is read under
	// the lock (a rebuild swaps it) and leased while its entries are listed;
	// overlay subdirectories are not shards of this index.
	idx.mu.RLock()
	runDir := idx.runDir
	release := idx.leaseDir(runDir)
	idx.mu.RUnlock()
	var indexSize int64
	if runDir != "" {
		entries, err := os.ReadDir(runDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if fi, err := e.Info(); err == nil {
					indexSize += fi.Size()
				}
			}
		}
	}
	release()

	// Count dirty files.
	dirtyCount := 0
	if idx.dirty != nil {
		dirtyCount = len(idx.dirty.dirtyPathSet())
	}

	return resultInfo{
		filesIndexed:   stats.FilesIndexed,
		indexSizeBytes: indexSize,
		symbolSources:  symbolSources,
		ctagsAvailable: ctagsAvailable,
		partial:        partial,
		partialReason:  partialReason,
		dirtyFiles:     dirtyCount,
	}
}

// pathConstraint is the file-name query for a path argument: a directory is
// an anchored prefix with a trailing slash, a file is anchored at both ends.
// It is CASE-SENSITIVE: Workspace.Resolve validated this exact path, and
// query.RegexpQuery's case-insensitive regexp let `src` match `Src/` too.
func pathConstraint(rel string, isDir bool) (query.Q, error) {
	pattern := "^" + regexp.QuoteMeta(rel) + "$"
	if isDir {
		pattern = "^" + regexp.QuoteMeta(rel+"/")
	}
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	return &query.Regexp{Regexp: re, FileName: true, CaseSensitive: true}, nil
}
