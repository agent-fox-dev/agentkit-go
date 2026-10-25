package tools

import (
	"strings"
	"testing"
)

func TestTruncationMarkersNameACallThatWorks(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{ReadMarker(2000, 8000), "[Showing lines 1-2000 of 8000. Use offset=2001 to continue.]"},
		{ReadOffsetMarker(1, 2000, 8000), "[Showing lines 1-2000 of 8000. Use offset=2001 to continue.]"},
		{ReadOffsetMarker(7991, 8000, 8000), "[Showing lines 7991-8000 of 8000.]"},
		{FindMarker(300), "[300 results limit reached. Use limit=600 for more, or refine the pattern or search a narrower path]"},
		{FindMarker(700), "[700 results limit reached. Use limit=1000 for more, or refine the pattern or search a narrower path]"},
		{FindMarker(FindResultCap), "[1000 results limit reached, which is the maximum limit; refine the pattern or search a narrower path]"},
		{ListMarker(ListEntryCap), "[500 entries limit reached, which is the maximum limit; list a subdirectory, or use find_files with a pattern]"},
		{SearchMarker(50), "[50 matches limit reached. Use max_matches=100 for more, or refine the pattern, narrow the path or use file_glob]"},
		{SearchMarker(SearchMatchCap), "[100 matches limit reached, which is the maximum max_matches; refine the pattern, narrow the path or use file_glob]"},
		{SearchBytesMarker(7, DefaultByteLimit), "[Showing 7 matches; 50.0KB limit reached. Refine the pattern, narrow the path or file_glob, or reduce context_lines.]"},
		{SymbolMarker(20), "[20 results limit reached. Use max_results=40 for more, or narrow with path, kind or exact]"},
		{SymbolMarker(50), "[50 results limit reached, which is the maximum max_results; narrow with path, kind or exact]"},
		{CapMarker("files", "max_files", 5, 25, "narrow"), "[5 files limit reached. Use max_files=10 for more, or narrow]"},
		{SymbolPartialMarker("files"), "[partial result: file limit reached. Narrow with path or use search_files]"},
		{SymbolPartialMarker("time"), "[partial result: time limit reached. Narrow with path or use search_files]"},
		{SymbolPartialMarker(""), "[partial result. Narrow with path or use search_files]"},
		// The path is pasted into a shell, so it is single-quoted.
		{LongLineMarker(3, 3<<20, DefaultByteLimit, "it's a $file.txt"),
			`[Line 3 is 3.0MB, exceeds 50.0KB limit. Use execute: sed -n '3p' 'it'\''s a $file.txt' | head -c 51200]`},
		{LongLineMarker(1, 900, 512, "a"), "[Line 1 is 900B, exceeds 512B limit. Use execute: sed -n '1p' 'a' | head -c 512]"},
	} {
		if c.got != c.want {
			t.Errorf("marker = %q\n    want %q", c.got, c.want)
		}
	}
	if m := InvalidUTF8Marker("my file"); !strings.Contains(m, "edit_file cannot match") || !strings.HasSuffix(m, "iconv -f latin1 -t utf-8 'my file']") {
		t.Errorf("InvalidUTF8Marker = %q", m)
	}
}

func TestClampLimitAppliesTheDefaultAndTheCap(t *testing.T) {
	for _, c := range []struct{ n, want int }{{0, 10}, {-5, 10}, {5, 5}, {100, 100}, {200, 100}} {
		if got := ClampLimit(c.n, 10, 100); got != c.want {
			t.Errorf("ClampLimit(%d, 10, 100) = %d, want %d", c.n, got, c.want)
		}
	}
}
