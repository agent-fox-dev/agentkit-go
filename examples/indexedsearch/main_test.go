package main

import (
	"context"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// The demo shows each part of the index: the opt-in, the lazy build, the query
// language, the three arguments, the refusals, find_symbol before and after
// the build, the three ways the index learns of an edit, the bounds, an agent
// and the end of the index's life. Each step's output holds what it shows.
func TestTheDemoShowsEveryCapabilityOfTheIndex(t *testing.T) {
	out, _ := demo(t)
	for _, c := range []struct {
		part  string
		wants []string
	}{
		{"## 1.", []string{"tools.For returned 12 tools; the last is code_search", "New built nothing yet: builds 0, files indexed 0",
			"idx.Symbols before any build: ok=false", "(index: "}}, // find_symbol's own table names itself
		{"## 2.", []string{"builds 1; indexed 67 files; skipped: 1 binary, 1 too small, 1 over 1 MiB, 1 with too many trigrams",
			`{"binary":1,"oversized":1,"too_many_trigrams":1,"too_small":1}`, "the index lives in 1 shard file(s)"}},
		{"# a word is a substring", []string{"checkout/checkout.go (6 matches)", "docs/checkout.md"}},
		{"# a capital makes it case-sensitive", []string{"checkout/checkout.go (2 matches)"}},
		{"# sym: matches", []string{"cart/cart.go (1 matches)", `path cart/cart.go, 1 matches, symbols ["Cart"]`}},
		{"# file: limits", []string{"[1 files matched", "payments/stripe.go (1 matches)"}},
		{"# lang: limits", []string{"[1 files matched", "scripts/report.py (1 matches)"}},
		{"# or, and, parentheses", []string{"checkout/checkout.go", "payments/stripe.go (1 matches)"}},
		{"# a regular expression", []string{"payments/stripe.go (3 matches)", "checkout/checkout.go (3 matches)"}},
		{"# path: only under", []string{"cart/cart.go (2 matches)", "cart/cart_test.go (3 matches)"}},
		{"# the workspace's ignore rules", []string{"[0 files matched", "No matches found."}},
		{"# max_files: more files", []string{"[3 files matched", "[3 files limit reached. Use max_files=6 for more"}},
		{"# context_lines: 0 for", []string{"19: func (s Stripe) Pay(cents int)", "13- type Stripe struct"}},
		{"# an empty query", []string{"error invalid_arguments: query must not be empty"}},
		{"# a query zoekt cannot parse", []string{"error invalid_arguments: query parse error"}},
		{"# a path outside the workspace", []string{"error path_not_allowed"}},
		{"## 6.", []string{"idx.Symbols now: ok=true", "(codesearch index: 67 files)", "cart/cart.go:11-13  type  type Cart struct"}},
		{"# write_file through the tool set", []string{"1 dirty files overlaid", "builds 1 (no rebuild), overlay builds 1, dirty files 1",
			"checkout/discount.go:4  func  func LoyaltyDiscount(total int) int"}},
		{"# an edit made outside the tool set", []string{"after os.WriteFile:   0 file(s) found", `after Invalidate("cart/cart.go"): 1 file(s) found`}},
		{"# more than 5%", []string{"builds 2 (it rebuilt), dirty files 0"}},
		{"# Options{MaxFiles: 10}", []string{`partial=true, limit "files"`, "Symbols ok=false"}},
		{"# Options{MaxBytes: 4000}", []string{`partial=true, limit "bytes"`, "Symbols ok=false"}},
		{"## 9.", []string{"tools the model sees: code_search, find_symbol, read_file, write_file, edit_file",
			"code_search returned to the model:\n    [1 files matched", "time.Sleep(s.Wait << attempt) // retry with backoff",
			"model: Retries happen in payments.Stripe.Pay"}},
		{"## 10.", []string{"run directory present before Close: true, after: false; a second Close returns <nil>",
			"error index_closed: index has been closed", "Code search example completed successfully"}},
	} {
		block := part(t, out, c.part)
		for _, want := range c.wants {
			if !strings.Contains(block, want) {
				t.Errorf("%s lacks %q:\n%s", c.part, want, block)
			}
		}
	}
	if strings.Contains(part(t, out, "# lang: limits"), "docs/checkout.md") {
		t.Error("lang:python returned the markdown note")
	}
	if strings.Contains(part(t, out, "## 6."), "(index: ") {
		t.Error("section 6 still shows find_symbol's own table")
	}
	m := regexp.MustCompile(`revalidation walks: (\d+) -> (\d+)`).FindStringSubmatch(part(t, out, `# Invalidate("") says`))
	if m == nil {
		t.Fatal("the output lacks the revalidation walk count")
	}
	if before, _ := strconv.Atoi(m[1]); m[2] != strconv.Itoa(before+1) {
		t.Errorf("Invalidate(\"\") took the walks from %s to %s, want one more", m[1], m[2])
	}
}

// The demo removes its sample project and every shard it wrote, including
// those of the two bounded indexes it builds on the side.
func TestTheDemoLeavesNothingBehind(t *testing.T) {
	_, tmp := demo(t)
	shards, _ := filepath.Glob(filepath.Join(tmp, "agentkit-codesearch-*", "*"))
	sample, _ := filepath.Glob(filepath.Join(tmp, "agentkit-indexedsearch-example-*"))
	if len(shards)+len(sample) > 0 {
		t.Errorf("left behind: %v %v", shards, sample)
	}
}

func TestTheProgramRunsTheDemo(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zoekt cannot run on windows")
	}
	env := testutil.Environ()
	env["TMPDIR"] = t.TempDir()
	if res := testutil.GoRun(t, "", ".", env); res.Exit != 0 || !strings.Contains(res.Stdout, "Code search example completed successfully") {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}

// Where zoekt cannot run the demo says so and succeeds, which is the fallback
// an embedder writes: carry on with Options.Index nil.
func TestOnAPlatformWithoutZoektTheDemoSaysSoAndSucceeds(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only windows has no zoekt")
	}
	var b strings.Builder
	if err := run(context.Background(), &b); err != nil || !strings.Contains(b.String(), "code search is not available on this platform") {
		t.Errorf("%v\n%s", err, b.String())
	}
}
