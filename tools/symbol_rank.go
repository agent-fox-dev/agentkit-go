package tools

import (
	"sort"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// isExactMatch returns true when the query matches the target exactly under
// smart-case rules. An all-lowercase query that equals the target
// case-insensitively counts as exact.
func isExactMatch(query, target string, caseSensitive bool) bool {
	if caseSensitive {
		return query == target
	}
	return strings.EqualFold(query, target)
}

// rankSymbols sorts matches by the ranking tiers defined in 02-REQ-4.1:
//  1. exact before prefix (smart-case equality counts as exact)
//  2. exported before unexported
//  3. non-test files before test files
//  4. shorter path first
//  5. path (lexicographic)
//  6. StartLine
//
// The sort is stable so repeated calls return an identical order.
func rankSymbols(matches []SymbolMatch, query string, qualified bool, caseSensitive bool) {
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]

		// Tier 1: exact before prefix.
		aTarget := a.Name
		bTarget := b.Name
		if qualified {
			if a.Container != "" {
				aTarget = a.Container + "." + a.Name
			}
			if b.Container != "" {
				bTarget = b.Container + "." + b.Name
			}
		}
		aExact := isExactMatch(query, aTarget, caseSensitive)
		bExact := isExactMatch(query, bTarget, caseSensitive)
		if aExact != bExact {
			return aExact
		}

		// Tier 2: exported before unexported.
		if a.Exported != b.Exported {
			return a.Exported
		}

		// Tier 3: non-test files before test files.
		aTest := outline.IsTestFile(a.Path)
		bTest := outline.IsTestFile(b.Path)
		if aTest != bTest {
			return !aTest
		}

		// Tier 4: shorter path first.
		if len(a.Path) != len(b.Path) {
			return len(a.Path) < len(b.Path)
		}

		// Tier 5: path (lexicographic).
		if a.Path != b.Path {
			return a.Path < b.Path
		}

		// Tier 6: StartLine.
		return a.StartLine < b.StartLine
	})
}

// sanitizePath replaces control characters in a path with '?' for display.
func sanitizePath(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			b.WriteByte('?')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
