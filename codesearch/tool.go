//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
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
  retry file:\.go$ -file:_test
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
	ContextLines int    `json:"context_lines"`
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
	if a.ContextLines < 0 {
		return core.ErrResult("invalid_arguments", "context_lines must not be negative")
	}

	// Parse the query with zoekt's parser.
	parsedQ, err := query.Parse(trimmed)
	if err != nil {
		return core.ErrResult("invalid_arguments", fmt.Sprintf("query parse error: %s", err.Error()))
	}

	// Default and clamp context_lines and max_files.
	contextLines := a.ContextLines
	if contextLines == 0 {
		contextLines = defaultContextLines
	}
	if contextLines > tools.MaxSearchContextLines {
		contextLines = tools.MaxSearchContextLines
	}
	maxFiles := tools.ClampLimit(a.MaxFiles, defaultMaxFiles, maxMaxFiles)

	// Validate and resolve path before any build.
	var pathConstraint query.Q
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
			if fi.IsDir() {
				// Directory: anchored prefix with trailing slash.
				escaped := regexp.QuoteMeta(rel + "/")
				pathQ, err := query.RegexpQuery("^"+escaped, false, true)
				if err != nil {
					return core.ErrResult("invalid_arguments",
						fmt.Sprintf("path constraint error: %s", err.Error()))
				}
				pathConstraint = pathQ
			} else {
				// File: anchored to the end.
				escaped := regexp.QuoteMeta(rel)
				pathQ, err := query.RegexpQuery("^"+escaped+"$", false, true)
				if err != nil {
					return core.ErrResult("invalid_arguments",
						fmt.Sprintf("path constraint error: %s", err.Error()))
				}
				pathConstraint = pathQ
			}
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

	// Build the final query with path conjunction.
	finalQ := parsedQ
	if pathConstraint != nil {
		finalQ = query.NewAnd(parsedQ, pathConstraint)
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
		files, total, err := idx.searchZoekt(searchCtx, finalQ, maxFiles, contextLines)
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

	// Build the result data.
	return idx.buildResult(resultFiles, maxFiles, totalFiles)
}

// ensureBuilt triggers a lazy build if the index has not been built yet.
// A partial index is not retried; only the dirty-threshold rebuild replaces it.
func (idx *Index) ensureBuilt(ctx context.Context) error {
	idx.mu.RLock()
	built := idx.built
	idx.mu.RUnlock()
	if built {
		return nil
	}
	return idx.Build(ctx)
}

// searchZoekt performs the actual zoekt search and returns the total number
// of files that matched (which may exceed maxFiles).
func (idx *Index) searchZoekt(ctx context.Context, q query.Q, maxFiles, contextLines int) ([]searchResultFile, int, error) {
	idx.mu.RLock()
	runDir := idx.runDir
	outlineFiles := idx.outlineFiles
	idx.mu.RUnlock()

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
		MaxDocDisplayCount: maxFiles + 1,
		NumContextLines:    contextLines,
		ChunkMatches:       true,
		MaxWallTime:        idx.effectiveQueryTimeout(),
	}

	result, err := searcher.Search(ctx, q, opts)
	if err != nil {
		return nil, 0, err
	}

	totalFiles := len(result.Files)
	files := convertFileMatches(result.Files, maxFiles, contextLines, outlineFiles)

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
func (idx *Index) buildResult(files []searchResultFile, maxFiles, totalFiles int) core.ToolResult {
	stats := idx.BuildStats()

	// Gather result info for the first line.
	info := idx.gatherResultInfo(stats)

	// Determine if more files matched than max_files.
	moreThanMax := totalFiles > maxFiles

	// Build the text.
	text := renderResult(files, info)

	// Apply byte cap.
	var bytesTruncated bool
	var bytesMarkerText string
	files, bytesMarkerText, bytesTruncated = applyByteCap(files, info, tools.DefaultByteLimit)
	if bytesTruncated {
		// Re-render with the truncated files.
		text = renderResult(files, info)
	}

	// Append cap marker if more files matched than max_files.
	var capMarkerText string
	if moreThanMax {
		capMarkerText = tools.CapMarker("files", "max_files", maxFiles, maxMaxFiles, "narrow the query or add a path")
	}

	// Append markers to text.
	if bytesTruncated {
		text += "\n" + bytesMarkerText
	} else if capMarkerText != "" {
		text += "\n" + capMarkerText
	}

	// Final byte-limit check: ensure text doesn't exceed the limit.
	if len(text) > tools.DefaultByteLimit {
		// Truncate to fit.
		text = text[:tools.DefaultByteLimit-len(bytesMarkerText)-1] + "\n" + bytesMarkerText
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
	// A partial index adds a note telling the model to narrow path or use search_files.
	if info.partial && note == "" {
		note = fmt.Sprintf("Index is partial (%s limit). Narrow the path argument or use search_files for a full scan.", info.partialReason)
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
			"binary":    stats.BinarySkipped,
			"oversized": stats.OversizedSkipped,
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

	// Determine ctags availability.
	// If DisableCtags is set, ctags is unavailable.
	// If a Runner was provided that returns ErrCtagsUnavailable, ctags is unavailable.
	// We detect this by checking if any file used the ctags backend.
	ctagsAvailable := false
	for _, of := range outlineFiles {
		if of.Backend == outline.BackendCtags {
			ctagsAvailable = true
			break
		}
	}
	// If DisableCtags was explicitly set, ctags is unavailable.
	if idx.opts.DisableCtags {
		ctagsAvailable = false
	}
	// If a runner was provided but no file used ctags, check if the runner
	// is the one that returns ErrCtagsUnavailable.
	if !idx.opts.DisableCtags && idx.opts.Runner != nil && !ctagsAvailable {
		// Runner was provided but no ctags backend was used.
		// This means ctags was unavailable.
		ctagsAvailable = false
	}

	// Calculate index size from shard files.
	var indexSize int64
	if idx.runDir != "" {
		entries, err := os.ReadDir(idx.runDir)
		if err == nil {
			for _, e := range entries {
				if fi, err := e.Info(); err == nil {
					indexSize += fi.Size()
				}
			}
		}
	}

	return resultInfo{
		filesIndexed:   stats.FilesIndexed,
		indexSizeBytes: indexSize,
		symbolSources:  symbolSources,
		ctagsAvailable: ctagsAvailable,
		partial:        partial,
		partialReason:  partialReason,
		dirtyFiles:     0, // set by later tasks
	}
}
