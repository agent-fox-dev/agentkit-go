package tools

import (
	"fmt"
	"strings"
	"testing"
)

// TS-05-30: Reference ranker sorts sites deterministically by confidence tier, test status, path, line, and column.
// Verifies: 05-REQ-6.1
func TestRefRanker_TS05_30(t *testing.T) {
	sites := []ReferenceSite{
		{Path: "pkg/z.go", Line: 5, Column: 1, Confidence: "text"},
		{Path: "pkg/b_test.go", Line: 10, Column: 4, Confidence: "resolved"},
		{Path: "pkg/a.go", Line: 10, Column: 2, Confidence: "resolved"},
		{Path: "pkg/a.go", Line: 20, Column: 1, Confidence: "resolved"},
		{Path: "pkg/a.go", Line: 10, Column: 1, Confidence: "resolved"},
		{Path: "pkg/b.go", Line: 1, Column: 1, Confidence: "resolved"},
		{Path: "pkg/c.go", Line: 1, Column: 1, Confidence: "lexical"},
		{Path: "pkg/c_test.go", Line: 1, Column: 1, Confidence: "lexical"},
		{Path: "pkg/a.go", Line: 5, Column: 1, Confidence: "text"},
		{Path: "test/helper.go", Line: 2, Column: 1, Confidence: "text"},
	}

	sortReferenceSites(sites)

	// Verify that each consecutive pair satisfies siteOrderLess.
	for i := 0; i < len(sites)-1; i++ {
		if !siteOrderLess(sites[i], sites[i+1]) {
			t.Fatalf("sites[%d] (%+v) is not strictly less than sites[%d] (%+v)",
				i, sites[i], i+1, sites[i+1])
		}
	}

	// Verify the overall expected ordering:
	// 1. resolved non-test: pkg/a.go:10:1, pkg/a.go:10:2, pkg/a.go:20:1, pkg/b.go:1:1
	// 2. resolved test: pkg/b_test.go:10:4
	// 3. lexical non-test: pkg/c.go:1:1
	// 4. lexical test: pkg/c_test.go:1:1
	// 5. text non-test: pkg/a.go:5:1, pkg/z.go:5:1
	// 6. text test: test/helper.go:2:1
	expected := []struct {
		path       string
		line       int
		col        int
		confidence string
	}{
		{"pkg/a.go", 10, 1, "resolved"},
		{"pkg/a.go", 10, 2, "resolved"},
		{"pkg/a.go", 20, 1, "resolved"},
		{"pkg/b.go", 1, 1, "resolved"},
		{"pkg/b_test.go", 10, 4, "resolved"},
		{"pkg/c.go", 1, 1, "lexical"},
		{"pkg/c_test.go", 1, 1, "lexical"},
		{"pkg/a.go", 5, 1, "text"},
		{"pkg/z.go", 5, 1, "text"},
		{"test/helper.go", 2, 1, "text"},
	}

	if len(sites) != len(expected) {
		t.Fatalf("got %d sites, want %d", len(sites), len(expected))
	}

	for i, exp := range expected {
		s := sites[i]
		if s.Path != exp.path || s.Line != exp.line || s.Column != exp.col || s.Confidence != exp.confidence {
			t.Errorf("site[%d] = {%s, L%d, C%d, %s}, want {%s, L%d, C%d, %s}",
				i, s.Path, s.Line, s.Column, s.Confidence, exp.path, exp.line, exp.col, exp.confidence)
		}
	}
}

// TS-05-31: Reference ranker excludes test files when include_tests is false.
// Verifies: 05-REQ-6.2
func TestRefRanker_TS05_31(t *testing.T) {
	sites := []ReferenceSite{
		{Path: "pkg/client.go", Line: 10, Column: 5, Confidence: "resolved"},
		{Path: "pkg/client_test.go", Line: 15, Column: 2, Confidence: "resolved"},
		{Path: "test/helper.go", Line: 20, Column: 3, Confidence: "lexical"},
		{Path: "tests/suite.go", Line: 25, Column: 1, Confidence: "text"},
		{Path: "src/calc_spec.rb", Line: 30, Column: 1, Confidence: "lexical"},
		{Path: "pkg/service.go", Line: 40, Column: 1, Confidence: "text"},
	}

	// Filter with includeTests = false
	filtered := filterTestSites(sites, false)
	for _, s := range filtered {
		if isTestFile(s.Path) {
			t.Errorf("expected non-test site, but got test site: %s", s.Path)
		}
	}

	if len(filtered) != 2 {
		t.Fatalf("got %d filtered sites, want 2 (pkg/client.go and pkg/service.go)", len(filtered))
	}
	if filtered[0].Path != "pkg/client.go" || filtered[1].Path != "pkg/service.go" {
		t.Fatalf("unexpected filtered paths: %v, %v", filtered[0].Path, filtered[1].Path)
	}

	// Filter with includeTests = true should retain all
	allSites := filterTestSites(sites, true)
	if len(allSites) != len(sites) {
		t.Fatalf("include_tests=true got %d sites, want %d", len(allSites), len(sites))
	}
}

// TS-05-32: Reference ranker truncates sites to max_results and appends CapMarker.
// Verifies: 05-REQ-6.3, 05-REQ-6.5
func TestRefRanker_TS05_32(t *testing.T) {
	// Create 50 reference sites
	sites := make([]ReferenceSite, 50)
	for i := 0; i < 50; i++ {
		sites[i] = ReferenceSite{
			Path:       fmt.Sprintf("pkg/file_%02d.go", i),
			Line:       i + 1,
			Column:     1,
			Confidence: "resolved",
		}
	}

	// Apply limit of 20
	limited, truncated := applyResultLimits(sites, 20)
	if len(limited) != 20 {
		t.Fatalf("expected 20 sites, got %d", len(limited))
	}
	if !truncated {
		t.Fatal("expected Truncated = true")
	}
	text := renderReferencesResult("Run", ReferenceResult{Sites: limited, Backend: "go/types", Truncated: truncated}).Text
	if want := CapMarker("references", "max_results", 20, 100, "narrow with path or kind"); !strings.HasSuffix(text, "\n"+want) {
		t.Fatalf("text does not end with %q:\n%s", want, text)
	}

	// Test default clamp: max_results = 0 defaults to 30
	limited, truncated = applyResultLimits(sites, 0)
	if len(limited) != 30 || !truncated {
		t.Fatalf("max_results=0: expected 30 sites and truncated, got %d and %v", len(limited), truncated)
	}

	// Test max clamp: 120 sites with max_results = 150 clamped to 100
	manySites := make([]ReferenceSite, 120)
	for i := 0; i < 120; i++ {
		manySites[i] = ReferenceSite{
			Path:       fmt.Sprintf("pkg/file_%03d.go", i),
			Line:       i + 1,
			Column:     1,
			Confidence: "resolved",
		}
	}
	limited, truncated = applyResultLimits(manySites, 150)
	if len(limited) != 100 || !truncated {
		t.Fatalf("max_results=150: expected 100 sites and truncated, got %d and %v", len(limited), truncated)
	}
	text = renderReferencesResult("Run", ReferenceResult{Sites: limited, Backend: "go/types", Truncated: truncated}).Text
	if want := CapMarker("references", "max_results", 100, 100, "narrow with path or kind"); !strings.HasSuffix(text, "\n"+want) {
		t.Fatalf("text does not end with %q", want)
	}

	// Test no truncation when count <= max_results
	limited, truncated = applyResultLimits(sites[:15], 20)
	if len(limited) != 15 || truncated {
		t.Fatalf("expected 15 sites, not truncated, got %d, %v", len(limited), truncated)
	}
}
