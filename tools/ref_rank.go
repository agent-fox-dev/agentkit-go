package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/outline"
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
	ca, cb := confidenceRank(a.Confidence), confidenceRank(b.Confidence)
	if ca != cb {
		return ca < cb
	}

	aTest := isTestFile(a.Path)
	bTest := isTestFile(b.Path)
	if aTest != bTest {
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
// When includeTests is true, all sites are retained.
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

// filterSites is an alias for filterTestSites.
func filterSites(sites []ReferenceSite, includeTests bool) []ReferenceSite {
	return filterTestSites(sites, includeTests)
}

// LimitedResult holds reference sites along with truncation information.
type LimitedResult struct {
	Sites      []ReferenceSite
	Truncated  bool
	MarkerText string
}

// applyResultLimits clamps maxResults (between 1 and 100, default 30 per 05-REQ-6.5)
// and truncates sites if length exceeds clamped maxResults (05-REQ-6.3).
func applyResultLimits(sites []ReferenceSite, maxResults int) LimitedResult {
	limit := clampMaxResults(maxResults)
	if len(sites) > limit {
		marker := CapMarker("references", "max_results", limit, 100, "narrow with path or kind")
		return LimitedResult{
			Sites:      sites[:limit],
			Truncated:  true,
			MarkerText: marker,
		}
	}
	return LimitedResult{
		Sites:      sites,
		Truncated:  false,
		MarkerText: "",
	}
}

// referenceBounds tracks file-count and execution-duration bounds during traversal.
// Verifies 05-REQ-6.4.
type referenceBounds struct {
	maxFiles    int
	maxDuration time.Duration
	filesSeen   int
	deadline    time.Time
	partial     bool
	reason      string // "files" or "time"
}

func newReferenceBounds(opts SymbolOptions) *referenceBounds {
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}
	maxDur := opts.MaxDuration
	if maxDur <= 0 {
		maxDur = defaultMaxDuration
	}
	return &referenceBounds{
		maxFiles:    maxFiles,
		maxDuration: maxDur,
		deadline:    time.Now().Add(maxDur),
	}
}

// checkFile records an inspected file and reports whether traversal can continue.
func (b *referenceBounds) checkFile(ctx context.Context) bool {
	if b.partial {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		b.partial = true
		b.reason = "time"
		return false
	}
	if !b.deadline.IsZero() && time.Now().After(b.deadline) {
		b.partial = true
		b.reason = "time"
		return false
	}
	b.filesSeen++
	if b.filesSeen > b.maxFiles {
		b.partial = true
		b.reason = "files"
		return false
	}
	return true
}

// checkTimeout checks whether context or duration limit has expired.
func (b *referenceBounds) checkTimeout(ctx context.Context) bool {
	if b.partial {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		b.partial = true
		b.reason = "time"
		return false
	}
	if !b.deadline.IsZero() && time.Now().After(b.deadline) {
		b.partial = true
		b.reason = "time"
		return false
	}
	return true
}

// countDistinctFiles returns the number of distinct files among reference sites.
func countDistinctFiles(sites []ReferenceSite) int {
	seen := make(map[string]bool)
	for _, s := range sites {
		seen[s.Path] = true
	}
	return len(seen)
}

// boundedReferenceFinder encapsulates a workspace and symbol options for bounded reference searches.
type boundedReferenceFinder struct {
	Workspace *Workspace
	Symbols   SymbolOptions
}

// newBoundedReferenceFinder creates a boundedReferenceFinder.
func newBoundedReferenceFinder(ws *Workspace, symOpts SymbolOptions) *boundedReferenceFinder {
	return &boundedReferenceFinder{
		Workspace: ws,
		Symbols:   symOpts,
	}
}

// runBoundedReferenceSearch scans candidate files in ws for name, respecting file and duration bounds.
func runBoundedReferenceSearch(ctx context.Context, ws *Workspace, symOpts SymbolOptions, name string, includeTests bool, maxResults int) core.ToolResult {
	bounds := newReferenceBounds(symOpts)

	runCtx, cancel := context.WithTimeout(ctx, bounds.maxDuration)
	defer cancel()

	// Check if already expired before search
	if !bounds.checkTimeout(runCtx) {
		refRes := ReferenceResult{
			Target:  outline.Decl{Name: name},
			Sites:   []ReferenceSite{},
			Backend: "lexical",
			Partial: true,
		}
		data := map[string]any{
			"target":         refRes.Target,
			"sites":          refRes.Sites,
			"backend":        refRes.Backend,
			"partial":        true,
			"partial_reason": bounds.reason,
			"truncated":      false,
			"result":         refRes,
			"note":           SymbolPartialMarker(bounds.reason),
		}
		res := core.OKResult(data)
		res.Text = fmt.Sprintf("find_references %s  (lexical, 0 references in 0 files)\n%s", name, SymbolPartialMarker(bounds.reason))
		return res
	}

	candidates, err := findCandidateFilesCtx(runCtx, ws, name, nil)
	if err != nil && runCtx.Err() != nil {
		bounds.partial = true
		bounds.reason = "time"
	}

	var sites []ReferenceSite
	for _, rel := range candidates {
		if !bounds.checkFile(runCtx) {
			break
		}
		abs := filepath.Join(ws.Root, filepath.FromSlash(rel))
		content, readErr := os.ReadFile(abs)
		if readErr != nil {
			continue
		}
		matches := scanContentForMatches(rel, content, name, outline.Decl{Name: name})
		for _, m := range matches {
			sites = append(sites, m.Site())
		}
	}

	if !bounds.checkTimeout(runCtx) {
		// Time bound hit during processing
		bounds.partial = true
		if bounds.reason == "" {
			bounds.reason = "time"
		}
	}

	// Filter test files if requested
	sites = filterTestSites(sites, includeTests)

	// Sort sites deterministically
	sortReferenceSites(sites)

	// Apply max_results limit
	limited := applyResultLimits(sites, maxResults)

	refRes := ReferenceResult{
		Target:    outline.Decl{Name: name},
		Sites:     limited.Sites,
		Backend:   "lexical",
		Partial:   bounds.partial,
		Truncated: limited.Truncated,
	}

	var textParts []string
	header := fmt.Sprintf("find_references %s  (%s, %d references in %d files)",
		name, refRes.Backend, len(refRes.Sites), countDistinctFiles(refRes.Sites))
	textParts = append(textParts, header)
	for _, s := range refRes.Sites {
		textParts = append(textParts, fmt.Sprintf("  %s:%d:%d  %s  %s", s.Path, s.Line, s.Column, s.Confidence, s.Source))
	}
	if limited.Truncated {
		textParts = append(textParts, limited.MarkerText)
	}
	if bounds.partial {
		textParts = append(textParts, SymbolPartialMarker(bounds.reason))
	}

	data := map[string]any{
		"target":    refRes.Target,
		"sites":     refRes.Sites,
		"backend":   refRes.Backend,
		"partial":   refRes.Partial,
		"truncated": refRes.Truncated,
		"result":    refRes,
	}
	if bounds.partial {
		data["partial_reason"] = bounds.reason
		if !limited.Truncated {
			data["note"] = SymbolPartialMarker(bounds.reason)
		} else {
			data["note"] = limited.MarkerText
		}
	} else if limited.Truncated {
		data["note"] = limited.MarkerText
	}

	res := core.OKResult(data)
	res.Text = strings.Join(textParts, "\n")
	if limited.Truncated {
		res.Metadata = &core.ToolMetadata{
			Truncated:   true,
			TruncatedBy: string(TruncatedByLines),
		}
	}
	return res
}

// executeWithShortTimeout executes a reference query against fr, halting gracefully if bounds expire.
func executeWithShortTimeout(ctx context.Context, fr any, name string) core.ToolResult {
	switch v := fr.(type) {
	case *boundedReferenceFinder:
		return runBoundedReferenceSearch(ctx, v.Workspace, v.Symbols, name, true, 30)
	case *Workspace:
		return runBoundedReferenceSearch(ctx, v, SymbolOptions{}, name, true, 30)
	case func(context.Context, string) core.ToolResult:
		return v(ctx, name)
	case func(context.Context, json.RawMessage) core.ToolResult:
		payload, _ := json.Marshal(map[string]any{"name": name})
		return v(ctx, payload)
	case core.Tool:
		payload, _ := json.Marshal(map[string]any{"name": name})
		if v.Execute != nil {
			return v.Execute(ctx, payload)
		}
		if v.Handler != nil {
			raw, err := v.Handler(ctx, payload)
			if err != nil {
				return core.ErrResult("tool_failed", err.Error())
			}
			return core.OKResult(map[string]any{"raw": string(raw)})
		}
		return core.ErrResult("invalid_arguments", "tool has no handler")
	default:
		return core.ErrResult("invalid_arguments", "unsupported reference finder type")
	}
}
