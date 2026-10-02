package tools

import (
	"strings"
	"unicode"

	"github.com/agentfox/agentkit-go/outline"
)

// SymbolMatch is one match from find_symbol.
type SymbolMatch struct {
	Path      string `json:"path"`
	Backend   string `json:"backend"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Container string `json:"container"`
	Signature string `json:"signature"`
	Exported  bool   `json:"exported"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// matchSymbols searches the symbol table for declarations matching the query.
// It applies name matching (prefix/exact, smart-case, qualified), kind filter
// and path filter, returning all matches.
func matchSymbols(st *symbolTable, name, kind, scopePrefix string, exact bool) []SymbolMatch {
	qualified := strings.Contains(name, ".")
	caseSensitive := hasUppercase(name)

	var matches []SymbolMatch
	for rel, entry := range st.entries {
		// Path filter: only files under the scope directory.
		if scopePrefix != "" && !strings.HasPrefix(rel, scopePrefix) {
			continue
		}

		for _, d := range entry.file.Decls {
			// Kind filter.
			if kind != "" && string(d.Kind) != kind {
				continue
			}

			if matchDecl(name, d, exact, qualified, caseSensitive) {
				matches = append(matches, SymbolMatch{
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
	}
	return matches
}

// matchDecl checks whether a single declaration matches the query.
func matchDecl(name string, d outline.Decl, exact, qualified, caseSensitive bool) bool {
	if qualified {
		return matchQualified(name, d, exact, caseSensitive)
	}
	return matchName(name, d.Name, exact, caseSensitive)
}

// matchQualified compares name against Container+"."+Name for declarations
// that have a container. A dotted name never matches a declaration without a
// container.
func matchQualified(name string, d outline.Decl, exact, caseSensitive bool) bool {
	if d.Container == "" {
		return false
	}
	qualifiedName := d.Container + "." + d.Name
	return matchName(name, qualifiedName, exact, caseSensitive)
}

// matchName performs the actual string comparison: exact or prefix, with
// case sensitivity determined by smart-case rules.
func matchName(query, target string, exact, caseSensitive bool) bool {
	if exact {
		// Exact match is always case-sensitive.
		return query == target
	}
	if caseSensitive {
		return strings.HasPrefix(target, query)
	}
	// Case-insensitive prefix.
	return len(target) >= len(query) &&
		strings.EqualFold(target[:len(query)], query)
}

// hasUppercase returns true if s contains any uppercase rune.
func hasUppercase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}
