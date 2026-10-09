package tools

import (
	"sort"
	"time"
)

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

// refBudget bounds one reference pass by SymbolOptions.MaxFiles and
// SymbolOptions.MaxDuration (05-REQ-6.4). A nil *refBudget is unbounded.
type refBudget struct {
	maxFiles int
	deadline time.Time
	files    int
	reason   string // "files" or "time" once a bound has been reached
}

// newRefBudget starts a budget with o's bounds, defaulted as the symbol
// table defaults them.
func newRefBudget(o SymbolOptions) *refBudget {
	maxFiles, maxDur := o.MaxFiles, o.MaxDuration
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	if maxDur <= 0 {
		maxDur = defaultMaxDuration
	}
	return &refBudget{maxFiles: maxFiles, deadline: time.Now().Add(maxDur)}
}

// take reports whether the pass may inspect one more file, counting it
// when it may. Once it has refused it keeps refusing.
func (b *refBudget) take() bool {
	if b == nil {
		return true
	}
	if b.reason == "" && b.files >= b.maxFiles {
		b.reason = "files"
	}
	if b.expired() {
		return false
	}
	b.files++
	return true
}

// expired reports whether a bound has been reached, recording the time
// bound when the deadline has passed.
func (b *refBudget) expired() bool {
	if b == nil {
		return false
	}
	if b.reason == "" && !time.Now().Before(b.deadline) {
		b.reason = "time"
	}
	return b.reason != ""
}

// exhausted reports whether a bound has been reached, without checking
// the clock.
func (b *refBudget) exhausted() bool {
	return b != nil && b.reason != ""
}

// partialReason is the SymbolPartialMarker reason of an exhausted budget.
func (b *refBudget) partialReason() string {
	if b == nil {
		return ""
	}
	return b.reason
}
