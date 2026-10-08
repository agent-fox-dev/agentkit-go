package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestMiddlewareExampleRunsKeyless runs every keyless section and checks the
// behaviours its README describes.
func TestMiddlewareExampleRunsKeyless(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out, false); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"C (registered last) →  B →  A (registered first) →",
		"retry: backing off 200ms before attempt 2",
		`answer: "Recovered on the second attempt." after 2 provider calls`,
		"false 429 rate limit: insufficient_quota",
		"call 4: spent so far $0.012 → refused",
		"provider calls made: 3 of 4",
		"provider calls: 2; Level 2 hits 1, misses 2",
		"provider reported 50000 cache-read tokens",
		"cache.hit=true",
		"skipped: pass --real",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q\n%s", want, out.String())
		}
	}
}
