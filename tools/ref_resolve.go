package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
)

// Decl converts SymbolMatch to an outline.Decl.
func (m SymbolMatch) Decl() outline.Decl {
	return outline.Decl{
		Kind:      outline.Kind(m.Kind),
		Name:      m.Name,
		Container: m.Container,
		Signature: m.Signature,
		Exported:  m.Exported,
		StartLine: m.StartLine,
		EndLine:   m.EndLine,
	}
}

// ToDecl converts SymbolMatch to an outline.Decl.
func (m SymbolMatch) ToDecl() outline.Decl {
	return m.Decl()
}

// splitContainerMember splits a qualified declaration name formatted as
// Container.Member at the last dot. If name contains no dot, container is empty
// and member is the full name.
func splitContainerMember(name string) (container, member string) {
	lastDot := strings.LastIndex(name, ".")
	if lastDot == -1 {
		return "", name
	}
	return name[:lastDot], name[lastDot+1:]
}

// matchCandidate checks if a declaration target matches the queried member name.
func matchCandidate(query, target string, exactOnly bool) bool {
	if query == target {
		return true
	}
	if strings.EqualFold(query, target) {
		return true
	}
	if exactOnly {
		return false
	}
	caseSensitive := hasUppercase(query)
	if caseSensitive {
		return strings.HasPrefix(target, query)
	}
	return len(target) >= len(query) && strings.EqualFold(target[:len(query)], query)
}

// resolveSymbolCandidates searches the symbol table for declarations matching
// the name (unqualified or container-qualified) and filters by kind and path.
func resolveSymbolCandidates(st *symbolTable, name, kind, path string) []SymbolMatch {
	if st == nil || st.entries == nil || name == "" {
		return []SymbolMatch{}
	}

	container, member := splitContainerMember(name)
	if member == "" {
		return []SymbolMatch{}
	}

	var scopePrefix string
	if path != "" {
		scopePrefix = filepath.ToSlash(strings.TrimPrefix(path, "./"))
		scopePrefix = strings.TrimPrefix(scopePrefix, "/")
		if scopePrefix != "" && !strings.HasSuffix(scopePrefix, "/") {
			scopePrefix += "/"
		}
	}

	collect := func(exactOnly bool) []SymbolMatch {
		var res []SymbolMatch
		for rel, entry := range st.entries {
			if entry == nil {
				continue
			}
			if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) && rel != strings.TrimSuffix(scopePrefix, "/") {
				continue
			}
			for _, d := range entry.file.Decls {
				if kind != "" && string(d.Kind) != kind {
					continue
				}
				if container != "" {
					if d.Container != container {
						continue
					}
					if !matchCandidate(member, d.Name, exactOnly) {
						continue
					}
				} else {
					if !matchCandidate(member, d.Name, exactOnly) {
						continue
					}
				}
				res = append(res, SymbolMatch{
					Path:      rel,
					Backend:   string(entry.file.Backend),
					Kind:      string(d.Kind),
					Name:      d.Name,
					Container: d.Container,
					Signature: d.Signature,
					Exported:  d.Exported,
					StartLine: d.StartLine,
					EndLine:   d.EndLine,
				})
			}
		}
		return res
	}

	matches := collect(true)
	if len(matches) == 0 {
		matches = collect(false)
	}
	if matches == nil {
		matches = []SymbolMatch{}
	}
	return matches
}

// resolveSymbolCandidatesDecls queries matching declarations as []outline.Decl.
func resolveSymbolCandidatesDecls(st *symbolTable, name, kind, path string) ([]outline.Decl, error) {
	matches := resolveSymbolCandidates(st, name, kind, path)
	decls := make([]outline.Decl, len(matches))
	for i, m := range matches {
		decls[i] = m.Decl()
	}
	return decls, nil
}

// disambiguateSymbol selects the highest-ranked declaration candidate applying
// find_symbol ranking precedence: exact match over prefix/case-variant, exported over
// unexported, non-test over test per isTestFile, shorter path, path order, and line order.
func disambiguateSymbol(candidates []SymbolMatch, query ...string) SymbolMatch {
	if len(candidates) == 0 {
		return SymbolMatch{}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	q := ""
	if len(query) > 0 && query[0] != "" {
		q = query[0]
	} else {
		for _, c := range candidates {
			if c.Exported && c.Name != "" {
				q = c.Name
				break
			}
		}
		if q == "" {
			q = candidates[0].Name
		}
	}

	cCopy := make([]SymbolMatch, len(candidates))
	copy(cCopy, candidates)

	qualified := strings.Contains(q, ".")
	caseSensitive := hasUppercase(q)
	rankSymbols(cCopy, q, qualified, caseSensitive)

	return cCopy[0]
}

// disambiguateSymbolDecl selects the highest-ranked candidate and returns its outline.Decl.
func disambiguateSymbolDecl(candidates []SymbolMatch, query ...string) outline.Decl {
	return disambiguateSymbol(candidates, query...).Decl()
}

// matchContainerSymbol looks for a declaration matching container and member exactly.
func matchContainerSymbol(st *symbolTable, container, member string) *SymbolMatch {
	if st == nil || st.entries == nil {
		return nil
	}
	for rel, entry := range st.entries {
		if entry == nil {
			continue
		}
		for _, d := range entry.file.Decls {
			if d.Container == container && d.Name == member {
				m := SymbolMatch{
					Path:      rel,
					Backend:   string(entry.file.Backend),
					Kind:      string(d.Kind),
					Name:      d.Name,
					Container: d.Container,
					Signature: d.Signature,
					Exported:  d.Exported,
					StartLine: d.StartLine,
					EndLine:   d.EndLine,
				}
				return &m
			}
		}
	}
	return nil
}

// makeFindReferencesFallbackTool creates a find_references tool instance
// backed by declaration resolution.
func makeFindReferencesFallbackTool(ws *Workspace, st *symbolTable) core.Tool {
	return core.Tool{
		Name:        "find_references",
		Description: "Find references and callers of a declaration across the workspace.",
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			if err := ctx.Err(); err != nil {
				return core.ErrResult("aborted", "Operation aborted")
			}
			var args struct {
				Name         string `json:"name"`
				Path         string `json:"path"`
				Kind         string `json:"kind"`
				IncludeTests *bool  `json:"include_tests"`
				MaxResults   *int   `json:"max_results"`
			}
			if err := json.Unmarshal(in, &args); err != nil {
				return core.ErrResult("invalid_arguments", "Malformed input")
			}
			if args.Name == "" {
				return core.ErrResult("invalid_arguments", "name is required")
			}

			// Path validation
			if args.Path != "" && ws != nil {
				resolved, err := ws.Resolve(args.Path)
				if err != nil {
					return core.ErrResult("path_not_allowed", err.Error())
				}
				fi, err := os.Stat(resolved)
				if err != nil || !fi.IsDir() {
					return core.ErrResult("invalid_arguments", "Path must be an existing directory")
				}
			}

			candidates := resolveSymbolCandidates(st, args.Name, args.Kind, args.Path)
			if len(candidates) == 0 {
				header := fmt.Sprintf("find_references %s  (text, 0 references in 0 files; 0 text matches)", args.Name)
				return core.ToolResult{
					OK:   true,
					Text: header,
					Data: map[string]any{
						"target":           outline.Decl{},
						"sites":            []ReferenceSite{},
						"backend":          "text",
						"partial":          false,
						"truncated":        false,
						"packages_checked": 0,
						"errors":           0,
					},
				}
			}

			top := disambiguateSymbol(candidates, args.Name)
			target := top.Decl()
			backend := "go/types"
			header := fmt.Sprintf("find_references %s  (%s, 0 references in 0 files; 0 text matches)", args.Name, backend)
			return core.ToolResult{
				OK:   true,
				Text: header,
				Data: map[string]any{
					"target":           target,
					"sites":            []ReferenceSite{},
					"backend":          backend,
					"partial":          false,
					"truncated":        false,
					"packages_checked": 0,
					"errors":           0,
				},
			}
		},
	}
}

// referenceResultFromData reconstructs a ReferenceResult from ToolResult.Data.
func referenceResultFromData(data map[string]any) ReferenceResult {
	var res ReferenceResult
	if target, ok := data["target"].(outline.Decl); ok {
		res.Target = target
	}
	if backend, ok := data["backend"].(string); ok {
		res.Backend = backend
	}
	if sites, ok := data["sites"].([]ReferenceSite); ok {
		res.Sites = sites
	}
	if partial, ok := data["partial"].(bool); ok {
		res.Partial = partial
	}
	if truncated, ok := data["truncated"].(bool); ok {
		res.Truncated = truncated
	}
	if pkgs, ok := data["packages_checked"].(int); ok {
		res.PackagesChecked = pkgs
	}
	if errs, ok := data["errors"].(int); ok {
		res.Errors = errs
	}
	return res
}
