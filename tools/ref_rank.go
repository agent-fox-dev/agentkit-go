package tools

import "sort"

// confidenceRank maps a confidence string to an integer rank.
// 05-REQ-6.1: resolved < lexical < text.
func confidenceRank(confidence string) int {
	switch confidence {
	case "resolved":
		return 1
	case "lexical":
		return 2
	case "text":
		return 3
	default:
		return 4
	}
}

// siteOrderLess reports whether reference site a should precede reference site b
// according to the 4-tier ranking rules (05-REQ-6.1):
//  1. Confidence tier: resolved < lexical < text
//  2. Non-test files before test files (per isTestFile)
//  3. Alphabetical workspace-relative path
//  4. Ascending 1-based line number, then ascending 1-based column.
func siteOrderLess(a, b ReferenceSite) bool {
	if ca, cb := confidenceRank(a.Confidence), confidenceRank(b.Confidence); ca != cb {
		return ca < cb
	}
	if aTest, bTest := isTestFile(a.Path), isTestFile(b.Path); aTest != bTest {
		return !aTest
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Column < b.Column
}

// sortReferenceSites sorts reference sites deterministically using the 4-tier ranking order.
func sortReferenceSites(sites []ReferenceSite) {
	sort.SliceStable(sites, func(i, j int) bool {
		return siteOrderLess(sites[i], sites[j])
	})
}

// filterTestSites removes reference sites in test files or directories when includeTests is false.
// Verifies 05-REQ-6.2.
func filterTestSites(sites []ReferenceSite, includeTests bool) []ReferenceSite {
	if includeTests {
		return sites
	}
	filtered := make([]ReferenceSite, 0, len(sites))
	for _, s := range sites {
		if !isTestFile(s.Path) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// applyResultLimits truncates sites to maxResults, clamped per
// clampMaxResults (05-REQ-6.3, 05-REQ-6.5), and reports whether it did.
func applyResultLimits(sites []ReferenceSite, maxResults int) ([]ReferenceSite, bool) {
	if limit := clampMaxResults(maxResults); len(sites) > limit {
		return sites[:limit], true
	}
	return sites, false
}
