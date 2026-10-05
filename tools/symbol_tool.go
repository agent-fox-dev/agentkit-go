package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
)

// findSymbolTool returns the find_symbol tool.
func (f *fileTools) findSymbolTool() core.Tool {
	// The symbol table is shared with write_file, edit_file and the shell
	// tool wrappers so they can mark it dirty. It is created lazily here
	// on first use.
	st := f.getTable()

	return core.Tool{
		Name: "find_symbol",
		Description: "Find declarations by name across the workspace. " +
			"Returns matching symbols with file paths and line ranges.",
		Builtin: true,
		InputSchema: schema.Object(
			schema.Prop("name", schema.String("Symbol name to search for")),
			schema.Opt("kind", schema.String("Declaration kind filter (func, method, type, class, interface, enum, trait, const, var, module, macro)")),
			schema.Opt("path", schema.String("Directory to search within")),
			schema.Opt("exact", schema.Bool("Require exact name match (default false)")),
			schema.Opt("max_results", schema.Int(fmt.Sprintf("Maximum results (default %d, at most %d)",
				SymbolResultDefault, SymbolResultCap))),
		),
		PromptGuidelines: []string{
			"Use find_symbol to locate a declaration; search_files for usages and text.",
		},
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			// Check context cancellation first.
			if ctx.Err() != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}

			var a struct {
				Name       string `json:"name"`
				Kind       string `json:"kind"`
				Path       string `json:"path"`
				Exact      bool   `json:"exact"`
				MaxResults int    `json:"max_results"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return core.ErrResult("invalid_arguments", err.Error())
			}

			// Validate name.
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return core.ErrResult("invalid_arguments", "name is required and must be non-empty")
			}
			if len(name) > 256 {
				return core.ErrResult("invalid_arguments", "name must be at most 256 bytes")
			}

			// Validate kind.
			kind := strings.TrimSpace(strings.ToLower(a.Kind))
			if kind != "" && !validSymbolKind(kind) {
				return core.ErrResult("invalid_arguments",
					fmt.Sprintf("kind must be one of: %s", strings.Join(validKinds, ", ")))
			}

			// Validate path.
			var scopePath string
			if a.Path != "" {
				abs, err := f.ws.Resolve(a.Path)
				if err != nil {
					return core.ErrResult("path_not_allowed", err.Error())
				}
				fi, err := os.Stat(abs)
				if err != nil {
					return core.ErrResult("read_failed", err.Error())
				}
				if !fi.IsDir() {
					return core.ErrResult("invalid_arguments",
						fmt.Sprintf("%s is a file; use file_outline for a single file", f.ws.Rel(abs)))
				}
				scopePath = abs
			}

			// Determine scope prefix for path filtering.
			var scopePrefix string
			if scopePath != "" {
				scopePrefix = filepath.ToSlash(f.ws.Rel(scopePath))
				if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
					scopePrefix += "/"
				}
			}

			// --- Index branch: try the codesearch index first. ---
			// 03-REQ-7: When Options.Index is set, call Index.Symbols.
			// On ok=true, rank and truncate with find_symbol's own code
			// and name the index in the header. On ok=false, fall back
			// to the symbol table. find_symbol never triggers a build.
			if f.index != nil {
				q := SymbolQuery{
					Name:  name,
					Kind:  kind,
					Path:  scopePrefix,
					Exact: a.Exact,
				}
				ans, ok, err := f.index.Symbols(ctx, q)
				if err != nil {
					return core.ErrResult("aborted", "Operation aborted")
				}
				if ok {
					return f.renderSymbolResult(ans.Matches, name, a.MaxResults, a.Exact, scopePrefix, ans.FilesIndexed, ans.Backends, true)
				}
				// ok=false: fall through to the symbol table.
			}

			// Build or refresh the symbol table.
			// Acquire the context-abandonable lock; a waiter whose ctx
			// ends returns aborted without waiting for the build.
			if !st.lock(ctx) {
				return core.ErrResult("aborted", "Operation aborted")
			}
			var br buildResult

			// Snapshot the dirty state under markMu (never blocks on a build).
			dirtyPaths, revalAll, gen := st.snapshotMarks()

			switch {
			case st.built && st.complete && !revalAll && len(dirtyPaths) == 0:
				// Table is complete and nothing is marked: answer from memory.
				br = st.computeMetrics(scopePrefix)
			case st.built && st.complete && !revalAll && len(dirtyPaths) > 0:
				// Only specific dirty paths: try targeted refresh.
				if st.refreshDirtyPaths(ctx, f, dirtyPaths) {
					br = st.computeMetrics(scopePrefix)
				} else {
					// Escalated to revalidation.
					br = st.buildOrRefresh(ctx, f, scopePath, true, gen)
				}
			default:
				// Initial build, incomplete table, or revalidation needed.
				br = st.buildOrRefresh(ctx, f, scopePath, revalAll, gen)
			}
			st.unlock()

			if ctx.Err() != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}

			// Compute backends map scoped to the query.
			backends := br.backends
			filesIndexed := br.filesIndexed

			// Match symbols.
			qualified := strings.Contains(name, ".")
			caseSensitive := hasUppercase(name)

			if !st.lock(ctx) {
				return core.ErrResult("aborted", "Operation aborted")
			}
			matches := matchSymbols(st, name, kind, scopePrefix, a.Exact)
			st.unlock()

			// Rank matches.
			rankSymbols(matches, name, qualified, caseSensitive)

			// Apply limit.
			limit := clampLimit(a.MaxResults, SymbolResultDefault, SymbolResultCap)
			truncated := len(matches) > limit
			if truncated {
				matches = matches[:limit]
			}

			// Ensure symbols is never nil.
			if matches == nil {
				matches = []SymbolMatch{}
			}

			// Build Data.
			data := map[string]any{
				"symbols":       matches,
				"truncated":     truncated,
				"backends":      backends,
				"files_indexed": filesIndexed,
			}

			// Build Text.
			var textParts []string

			// Index line with sorted backends.
			indexLine := renderIndexLine(len(matches), name, filesIndexed, backends, truncated)
			textParts = append(textParts, indexLine)

			// No-match message.
			if len(matches) == 0 {
				textParts = append(textParts, "no symbols matched")
			}

			// Match lines.
			for _, m := range matches {
				textParts = append(textParts, renderMatchLine(m))
			}

			// Truncation marker.
			var r core.ToolResult
			if truncated {
				marker := SymbolMarker(limit)
				data["note"] = marker
				textParts = append(textParts, marker)
				r = core.OKResult(data)
				r.Metadata = &core.ToolMetadata{
					Truncated:   true,
					TruncatedBy: string(TruncatedByLines),
				}
			} else {
				r = core.OKResult(data)
			}

			// Partial note.
			if br.partial {
				data["partial"] = true
				data["partial_reason"] = br.partialReason
				partialNote := SymbolPartialMarker(br.partialReason)
				if !truncated {
					// Only set note if not already set by truncation.
					data["note"] = partialNote
				}
				textParts = append(textParts, partialNote)
			}

			r.Text = strings.Join(textParts, "\n")
			return r
		},
	}
}

// validKinds is the list of valid symbol kinds, matching outline.Kind constants.
var validKinds = []string{
	"func", "method", "type", "class", "interface",
	"enum", "trait", "const", "var", "module", "macro",
}

// validSymbolKind checks if a kind string is one of the valid outline kinds.
func validSymbolKind(kind string) bool {
	for _, k := range validKinds {
		if kind == k {
			return true
		}
	}
	return false
}

// renderIndexLine builds the header line for find_symbol results.
func renderIndexLine(matchCount int, name string, filesIndexed int, backends map[string]int, truncated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d symbols matching %q  (index: %d files", matchCount, name, filesIndexed)
	if len(backends) > 0 {
		// Sort backend names for deterministic output.
		keys := make([]string, 0, len(backends))
		for k := range backends {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("; ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s %d", k, backends[k])
		}
	}
	b.WriteString(")")
	return b.String()
}

// renderMatchLine builds one match line for find_symbol results.
// Format: <path>:<start>-<end>  <kind>  <signature>
func renderMatchLine(m SymbolMatch) string {
	path := sanitizePath(m.Path)
	var lineRange string
	if m.EndLine == 0 || m.EndLine == m.StartLine {
		lineRange = fmt.Sprintf("%d", m.StartLine)
	} else {
		lineRange = fmt.Sprintf("%d-%d", m.StartLine, m.EndLine)
	}
	return fmt.Sprintf("%s:%s  %s  %s", path, lineRange, m.Kind, m.Signature)
}

// renderSymbolResult ranks, truncates and renders a find_symbol result from
// matches provided by the codesearch index. It uses find_symbol's own ranking
// and truncation code so the contract is identical to the table-based path.
// When fromIndex is true, the header names the codesearch index as the backend.
func (f *fileTools) renderSymbolResult(
	matches []SymbolMatch,
	name string,
	maxResults int,
	exact bool,
	scopePrefix string,
	filesIndexed int,
	backends map[string]int,
	fromIndex bool,
) core.ToolResult {
	// Rank matches using find_symbol's own ranking.
	qualified := strings.Contains(name, ".")
	caseSensitive := hasUppercase(name)
	rankSymbols(matches, name, qualified, caseSensitive)

	// Apply limit.
	limit := clampLimit(maxResults, SymbolResultDefault, SymbolResultCap)
	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	// Ensure symbols is never nil.
	if matches == nil {
		matches = []SymbolMatch{}
	}

	// An Index that reports no backends for the scope leaves them to be
	// counted from the matches.
	if backends == nil {
		backends = make(map[string]int)
		for _, m := range matches {
			if m.Backend != "" {
				backends[m.Backend]++
			}
		}
	}

	// Build Data.
	data := map[string]any{
		"symbols":       matches,
		"truncated":     truncated,
		"backends":      backends,
		"files_indexed": filesIndexed,
	}

	// Build Text.
	var textParts []string

	// Header line: name the codesearch index as the backend.
	if fromIndex {
		textParts = append(textParts, renderCodesearchIndexLine(len(matches), name, filesIndexed, truncated))
	} else {
		textParts = append(textParts, renderIndexLine(len(matches), name, filesIndexed, backends, truncated))
	}

	// No-match message.
	if len(matches) == 0 {
		textParts = append(textParts, "no symbols matched")
	}

	// Match lines.
	for _, m := range matches {
		textParts = append(textParts, renderMatchLine(m))
	}

	// Truncation marker.
	var r core.ToolResult
	if truncated {
		marker := SymbolMarker(limit)
		data["note"] = marker
		textParts = append(textParts, marker)
		r = core.OKResult(data)
		r.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: string(TruncatedByLines),
		}
	} else {
		r = core.OKResult(data)
	}

	r.Text = strings.Join(textParts, "\n")
	return r
}

// renderCodesearchIndexLine builds the header line for find_symbol results
// when the codesearch index is the backend.
func renderCodesearchIndexLine(matchCount int, name string, filesIndexed int, truncated bool) string {
	return fmt.Sprintf("%d symbols matching %q  (codesearch index: %d files)", matchCount, name, filesIndexed)
}
