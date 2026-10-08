package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/schema"
)

// errResult returns an error ToolResult with both Detail and Text populated.
func errResult(code, detail string) core.ToolResult {
	r := core.ErrResult(code, detail)
	r.Text = detail
	return r
}

// ensureSymbolTable builds or refreshes the shared symbol table if needed.
func (f *fileTools) ensureSymbolTable(ctx context.Context) error {
	st := f.getTable()
	if st == nil {
		return nil
	}
	if !st.lock(ctx) {
		return ctx.Err()
	}
	defer st.unlock()

	dirtyPaths, revalAll, gen := st.snapshotMarks()
	switch {
	case st.built && st.complete && !revalAll && len(dirtyPaths) == 0:
		// Table is complete and clean.
	case st.built && st.complete && !revalAll && len(dirtyPaths) > 0:
		if !st.refreshDirtyPaths(ctx, f, dirtyPaths) {
			st.buildOrRefresh(ctx, f, "", true, st.currentGeneration())
		}
	default:
		st.buildOrRefresh(ctx, f, "", revalAll, gen)
	}
	return nil
}

// findReferencesTool returns the find_references tool.
func (f *fileTools) findReferencesTool() core.Tool {
	return core.Tool{
		Name: "find_references",
		Description: "Find references and callers of a declaration across the workspace. " +
			"Returns matching reference sites with file paths, line numbers, and enclosing declarations.",
		Builtin:       true,
		ExecutionMode: core.Parallel,
		InputSchema: schema.Object(
			schema.Prop("name", schema.String("Declaration name to find references for. May be unqualified or qualified with container.")),
			schema.Opt("path", schema.String("Directory path to scope the reference search within")),
			schema.Opt("kind", schema.String("Declaration kind filter (func, method, type, class, interface, enum, trait, const, var, module, macro)")),
			schema.Opt("include_tests", schema.Bool("When false, reference sites in test files are omitted (default true)")),
			schema.Opt("max_results", schema.Int("Maximum reference sites to return (default 30, cap 100)")),
		),
		PromptGuidelines: []string{
			"Use find_symbol for where a name is declared, find_references for who uses it, and search_files for text.",
		},
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			if ctx.Err() != nil {
				return errResult("aborted", "Operation aborted")
			}

			var a struct {
				Name         string `json:"name"`
				Path         string `json:"path"`
				Kind         string `json:"kind"`
				IncludeTests *bool  `json:"include_tests"`
				MaxResults   *int   `json:"max_results"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return errResult("invalid_arguments", "Malformed input: "+err.Error())
			}

			// Validate name
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return errResult("invalid_arguments", "name is required and must be non-empty")
			}
			if len(a.Name) > 256 {
				return errResult("invalid_arguments", "name must be at most 256 bytes")
			}

			// Validate kind
			var kind string
			if a.Kind != "" {
				k := strings.TrimSpace(strings.ToLower(a.Kind))
				if !validSymbolKind(k) {
					return errResult("invalid_arguments",
						fmt.Sprintf("kind must be one of: %s", strings.Join(validKinds, ", ")))
				}
				kind = k
			}

			// Validate path
			if a.Path != "" {
				abs, err := f.ws.Resolve(a.Path)
				if err != nil {
					return errResult("path_not_allowed", err.Error())
				}
				fi, err := os.Stat(abs)
				if err != nil {
					return errResult("invalid_arguments", fmt.Sprintf("directory not found: %s", a.Path))
				}
				if !fi.IsDir() {
					return errResult("invalid_arguments", fmt.Sprintf("path is not a directory: %s", a.Path))
				}
			}

			includeTests := true
			if a.IncludeTests != nil {
				includeTests = *a.IncludeTests
			}

			var maxResults int // clamped by executeReferenceSearch
			if a.MaxResults != nil {
				maxResults = *a.MaxResults
			}

			fail := func(err error) core.ToolResult {
				if ctx.Err() != nil {
					return errResult("aborted", "Operation aborted")
				}
				return errResult("internal_error", err.Error())
			}

			// Ensure reference cache and symbol table freshness
			rc := f.getRefCache()
			if err := rc.refresh(ctx); err != nil {
				return fail(err)
			}
			if err := f.ensureSymbolTable(ctx); err != nil {
				return fail(err)
			}

			target := outline.Decl{Name: a.Name}
			backend := "text"
			if candidates := resolveSymbolCandidates(f.getTable(), a.Name, kind, a.Path); len(candidates) > 0 {
				top := disambiguateSymbol(candidates, a.Name)
				target = top.Decl()
				backend = "lexical"
				if strings.HasSuffix(top.Path, ".go") {
					backend = "go/types"
				}
			}

			opts := ReferenceOptions{Path: a.Path, IncludeTests: includeTests, MaxResults: maxResults}
			refRes, err := executeReferenceSearch(ctx, f.ws, target, backend, opts, rc)
			if err != nil {
				return fail(err)
			}
			return renderReferencesResult(a.Name, refRes)
		},
	}
}
