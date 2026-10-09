package tools

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/outline"
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
	if strings.EqualFold(query, target) {
		return true
	}
	if exactOnly {
		return false
	}
	if hasUppercase(query) {
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
				if container != "" && d.Container != container {
					continue
				}
				if !matchCandidate(member, d.Name, exactOnly) {
					continue
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

// disambiguateSymbol selects the highest-ranked declaration candidate for
// query applying find_symbol ranking precedence: exact match over
// prefix/case-variant, exported over unexported, non-test over test per
// isTestFile, shorter path, path order, and line order.
func disambiguateSymbol(candidates []SymbolMatch, query string) SymbolMatch {
	if len(candidates) == 0 {
		return SymbolMatch{}
	}
	ranked := slices.Clone(candidates)
	rankSymbols(ranked, query, strings.Contains(query, "."), hasUppercase(query))
	return ranked[0]
}
