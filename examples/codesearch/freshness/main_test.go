//go:build !windows

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestFreshness pins what the walkthrough prints at each step. Step 5 runs
// within two seconds of the first build, so every file is in the racy window;
// it must still overlay the two files that changed and not rebuild.
func TestFreshness(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"sym:Retry      -> pkg/retry.go\n",
		"2. retry.go rewritten, index not told\n   sym:RetryN     -> no matches\n",
		"3. Invalidate(\"pkg/retry.go\")\n   sym:RetryN     -> pkg/retry.go\n",
		"4. Symbols(RetryN) -> ok=true matches=1 pkg/retry.go:4\n",
		"sym:Backoff    -> gen/backoff.go\n",
		"sym:Helper00   -> no matches\n   builds=1 overlays=2 revalidations=1\n",
		"sym:Renamed    -> pkg/file01.go, pkg/file02.go, pkg/file03.go, pkg/file04.go, pkg/file05.go\n   builds=2 overlays=2 revalidations=1\n",
		"7. after Close\n   sym:Retry      -> error index_closed\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %q:\n%s", want, got)
		}
	}
}
