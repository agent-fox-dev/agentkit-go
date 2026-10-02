package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
)

// findSymbolTool returns the find_symbol tool.
func (f *fileTools) findSymbolTool() core.Tool {
	// The symbol table is created empty and built on first use.
	st := newSymbolTable()

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

			// Build or refresh the symbol table.
			st.mu.Lock()
			br := st.buildOrRefresh(ctx, f, scopePath)
			st.mu.Unlock()

			if ctx.Err() != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}

			// Compute backends map scoped to the query.
			backends := br.backends
			filesIndexed := br.filesIndexed

			// Determine scope prefix for path filtering.
			var scopePrefix string
			if scopePath != "" {
				scopePrefix = filepath.ToSlash(f.ws.Rel(scopePath))
				if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
					scopePrefix += "/"
				}
			}

			// Match symbols.
			st.mu.Lock()
			matches := matchSymbols(st, name, kind, scopePrefix, a.Exact)
			st.mu.Unlock()

			// Build the result.
			// Ranking and limiting come in task 5.
			data := map[string]any{
				"symbols":       matches,
				"backends":      backends,
				"files_indexed": filesIndexed,
			}

			var textParts []string
			indexLine := fmt.Sprintf("%d symbols matching %q  (index: %d files", len(matches), name, filesIndexed)
			if len(backends) > 0 {
				var parts []string
				for b, n := range backends {
					parts = append(parts, fmt.Sprintf("%s %d", b, n))
				}
				indexLine += "; " + strings.Join(parts, ", ")
			}
			indexLine += ")"
			textParts = append(textParts, indexLine)

			if br.partial {
				data["partial"] = true
				data["partial_reason"] = br.partialReason
				note := SymbolPartialMarker(br.partialReason)
				data["note"] = note
				textParts = append(textParts, note)
			}

			r := core.OKResult(data)
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

// SymbolResultDefault is the default max_results for find_symbol.
const SymbolResultDefault = 20

// SymbolResultCap is the maximum max_results for find_symbol.
const SymbolResultCap = 50
