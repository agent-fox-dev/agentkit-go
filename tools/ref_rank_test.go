package tools

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

func TestReferenceSitesAreRankedFilteredAndLimited(t *testing.T) {
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
		{Path: "a.go", Line: 1, Column: 1, Confidence: "odd"},
	}
	sortReferenceSites(sites)
	// Confidence, then non-test before test, then path, line and column.
	want := []string{"pkg/a.go:10:1 resolved", "pkg/a.go:10:2 resolved", "pkg/a.go:20:1 resolved", "pkg/b.go:1:1 resolved",
		"pkg/b_test.go:10:4 resolved", "pkg/c.go:1:1 lexical", "pkg/c_test.go:1:1 lexical", "pkg/a.go:5:1 text",
		"pkg/z.go:5:1 text", "test/helper.go:2:1 text", "a.go:1:1 odd"}
	if got := siteList(ReferenceResult{Sites: sites}); !slices.Equal(got, want) {
		t.Fatalf("order %v\nwant %v", got, want)
	}
	nonTest := []string{"pkg/a.go:10:1 resolved", "pkg/a.go:10:2 resolved", "pkg/a.go:20:1 resolved", "pkg/b.go:1:1 resolved",
		"pkg/c.go:1:1 lexical", "pkg/a.go:5:1 text", "pkg/z.go:5:1 text", "a.go:1:1 odd"}
	if got := siteList(ReferenceResult{Sites: filterTestSites(sites, false)}); !slices.Equal(got, nonTest) {
		t.Fatalf("without tests %v", got)
	}
	if got := filterTestSites(sites, true); len(got) != len(sites) {
		t.Fatal("include tests keeps every site")
	}

	many := make([]ReferenceSite, 120)
	for i := range many {
		many[i] = ReferenceSite{Path: fmt.Sprintf("f%03d.go", i), Line: 1}
	}
	for _, c := range []struct{ n, max, want int }{{120, 20, 20}, {120, 0, 30}, {120, -1, 30}, {120, 150, 100}, {15, 20, 15}, {30, 30, 30}} {
		got, truncated := applyResultLimits(many[:c.n], c.max)
		if len(got) != c.want || truncated != (c.n > c.want) {
			t.Errorf("%d sites, max %d: %d, truncated %v; want %d", c.n, c.max, len(got), truncated, c.want)
		}
	}
}

func TestTheReferenceBudget(t *testing.T) {
	b := newRefBudget(SymbolOptions{MaxFiles: 2, MaxDuration: time.Hour})
	for i, want := range []bool{true, true, false, false} {
		if got := b.take(); got != want {
			t.Fatalf("take %d = %v; two files fit a two-file budget, and then none", i+1, got)
		}
	}
	if b.partialReason() != "files" || !b.expired() || !b.exhausted() {
		t.Fatalf("an exhausted budget: %q", b.partialReason())
	}
	late := newRefBudget(SymbolOptions{MaxDuration: time.Nanosecond})
	testutil.Eventually(t, "the deadline to pass", late.expired)
	if late.take() || late.partialReason() != "time" {
		t.Fatalf("an expired budget: %q", late.partialReason())
	}
	var unbounded *refBudget
	if !unbounded.take() || unbounded.expired() || unbounded.exhausted() || unbounded.partialReason() != "" {
		t.Fatal("a nil budget is unbounded")
	}
	if d := newRefBudget(SymbolOptions{}); d.maxFiles != defaultMaxFiles || time.Until(d.deadline) > defaultMaxDuration {
		t.Fatalf("the defaults are the symbol table's: %+v", d)
	}
}
