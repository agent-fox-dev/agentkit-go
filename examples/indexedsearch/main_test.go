package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// demo runs the demo in process with the temporary directory pointed at a
// directory of the test's own, which is returned too, so that what the demo
// leaves behind can be counted and none of it reaches the real one.
func demo(t *testing.T) (out, tmp string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("zoekt cannot run on windows: the demo prints that and stops")
	}
	tmp = t.TempDir()
	t.Setenv("TMPDIR", tmp)

	// zoekt logs through the standard logger. The demo points it nowhere for
	// the run, so a line that reaches this buffer is one it let through.
	var logs strings.Builder
	was := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(was)

	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	if logs.Len() > 0 {
		t.Errorf("the demo let the standard logger write:\n%s", logs.String())
	}
	return b.String(), tmp
}

// part is the text of out from the line that starts with start up to the next
// line that starts with a heading, so a check on one step cannot be satisfied
// by the output of another. It fails the test when start is not there.
func part(t *testing.T, out, start string) string {
	t.Helper()
	i := strings.Index(out, "\n"+start)
	if i < 0 {
		t.Fatalf("the output has no %q", start)
	}
	rest := out[i+1:]
	if j := strings.Index(rest[len(start):], "\n#"); j >= 0 {
		return rest[:len(start)+j]
	}
	return rest
}

// contains fails the test for each of wants that block lacks.
func contains(t *testing.T, name, block string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(block, want) {
			t.Errorf("%s lacks %q:\n%s", name, want, block)
		}
	}
}

// The demo shows each part of the index: the opt-in, the lazy build, the query
// language, the three arguments, the refusals, find_symbol before and after
// the build, the three ways the index learns of an edit, the bounds, an agent
// and the end of the index's life.
func TestDemoShowsEveryCapabilityOfTheIndex(t *testing.T) {
	out, _ := demo(t)

	contains(t, "1. opt in", part(t, out, "## 1."),
		"tools.For returned 12 tools; the last is code_search",
		"New built nothing yet: builds 0, files indexed 0",
		"idx.Symbols before any build: ok=false",
		"(index: ") // find_symbol's own table names itself
	contains(t, "2. first query", part(t, out, "## 2."),
		"builds 1; indexed 67 files; skipped: 1 binary, 1 too small, 1 over 1 MiB, 1 with too many trigrams",
		`{"binary":1,"oversized":1,"too_many_trigrams":1,"too_small":1}`,
		"the index lives in 1 shard file(s)")

	// 3. The query language, one step each.
	contains(t, "a lower-case word", part(t, out, "# a word is a substring"), "checkout/checkout.go (6 matches)", "docs/checkout.md")
	contains(t, "a capital", part(t, out, "# a capital makes it case-sensitive"), "checkout/checkout.go (2 matches)")
	contains(t, "sym:", part(t, out, "# sym: matches"), "cart/cart.go (1 matches)", `path cart/cart.go, 1 matches, symbols ["Cart"]`)
	contains(t, "file:", part(t, out, "# file: limits"), "[1 files matched", "payments/stripe.go (1 matches)")
	contains(t, "lang:", part(t, out, "# lang: limits"), "[1 files matched", "scripts/report.py (1 matches)")
	if strings.Contains(part(t, out, "# lang: limits"), "docs/checkout.md") {
		t.Error("lang:python returned the markdown note")
	}
	contains(t, "or and case:no", part(t, out, "# or, and, parentheses"), "checkout/checkout.go", "payments/stripe.go (1 matches)")
	contains(t, "a regular expression", part(t, out, "# a regular expression"), "payments/stripe.go (3 matches)", "checkout/checkout.go (3 matches)")

	// 4. path, ignore rules, max_files, context_lines.
	contains(t, "path:", part(t, out, "# path: only under"), "cart/cart.go (2 matches)", "cart/cart_test.go (3 matches)")
	contains(t, "ignore rules", part(t, out, "# the workspace's ignore rules"), "[0 files matched", "No matches found.")
	contains(t, "max_files", part(t, out, "# max_files: more files"), "[3 files matched", "[3 files limit reached. Use max_files=6 for more")
	contains(t, "context_lines", part(t, out, "# context_lines: 0 for"), "19: func (s Stripe) Pay(cents int)", "13- type Stripe struct")

	// 5. A refusal is a result.
	contains(t, "an empty query", part(t, out, "# an empty query"), "error invalid_arguments: query must not be empty")
	contains(t, "an unparsable query", part(t, out, "# a query zoekt cannot parse"), "error invalid_arguments: query parse error")
	contains(t, "a path outside", part(t, out, "# a path outside the workspace"), "error path_not_allowed")

	// 6. find_symbol is answered by the index, which names itself.
	contains(t, "6. symbols", part(t, out, "## 6."),
		"idx.Symbols now: ok=true", "(codesearch index: 67 files)", "cart/cart.go:11-13  type  type Cart struct")
	if strings.Contains(part(t, out, "## 6."), "(index: ") {
		t.Error("section 6 still shows find_symbol's own table")
	}

	// 7. The three ways the index learns of an edit.
	contains(t, "write_file", part(t, out, "# write_file through the tool set"),
		"1 dirty files overlaid",
		"builds 1 (no rebuild), overlay builds 1, dirty files 1",
		"checkout/discount.go:4  func  func LoyaltyDiscount(total int) int")
	contains(t, "an outside edit", part(t, out, "# an edit made outside the tool set"),
		"after os.WriteFile:   0 file(s) found", `after Invalidate("cart/cart.go"): 1 file(s) found`)
	contains(t, "more than 5%", part(t, out, "# more than 5%"), "builds 2 (it rebuilt), dirty files 0")
	m := regexp.MustCompile(`revalidation walks: (\d+) -> (\d+)`).FindStringSubmatch(part(t, out, `# Invalidate("") says`))
	if m == nil {
		t.Fatal("the output lacks the revalidation walk count")
	}
	before, _ := strconv.Atoi(m[1])
	after, _ := strconv.Atoi(m[2])
	if after != before+1 {
		t.Errorf("Invalidate(\"\") took the walks from %d to %d, want one more", before, after)
	}

	// 8. Each bound stops a build, and a partial index does not answer find_symbol.
	contains(t, "MaxFiles", part(t, out, "# Options{MaxFiles: 10}"), `partial=true, limit "files"`, "Symbols ok=false")
	contains(t, "MaxBytes", part(t, out, "# Options{MaxBytes: 4000}"), `partial=true, limit "bytes"`, "Symbols ok=false")

	// 9. The model asks for code_search and is given the result.
	contains(t, "9. agent", part(t, out, "## 9."),
		"tools the model sees: code_search, find_symbol, read_file, write_file, edit_file",
		"code_search returned to the model:\n    [1 files matched",
		"time.Sleep(s.Wait << attempt) // retry with backoff",
		"model: Retries happen in payments.Stripe.Pay")

	// 10. Close ends the index's life.
	contains(t, "10. close", part(t, out, "## 10."),
		"run directory present before Close: true, after: false; a second Close returns <nil>",
		"error index_closed: index has been closed",
		"Code search example completed successfully")
}

// The demo removes its sample project and every shard it wrote, including
// those of the two bounded indexes it builds on the side.
func TestDemoLeavesNothingBehind(t *testing.T) {
	_, tmp := demo(t)
	left, err := filepath.Glob(filepath.Join(tmp, "agentkit-codesearch-*", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) > 0 {
		t.Errorf("left behind: %v", left)
	}
	sample, _ := filepath.Glob(filepath.Join(tmp, "agentkit-indexedsearch-example-*"))
	if len(sample) > 0 {
		t.Errorf("the sample project was not removed: %v", sample)
	}
}

// The sample holds one file of each kind the index cannot search, and the
// rest it can: this is what the demo's counts stand on.
func TestSampleHoldsOneFileOfEachSkipReason(t *testing.T) {
	dir := t.TempDir()
	if err := writeSample(dir); err != nil {
		t.Fatal(err)
	}
	size := func(rel string) int {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return int(fi.Size())
	}
	if n := size("data/flag.txt"); n >= 3 {
		t.Errorf("data/flag.txt is %d bytes, want fewer than 3 (too small)", n)
	}
	if n := size("data/orders.csv"); n <= 1<<20 {
		t.Errorf("data/orders.csv is %d bytes, want over 1 MiB", n)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "assets", "logo.png")); !strings.Contains(string(raw), "\x00") {
		t.Error("assets/logo.png holds no NUL byte (binary)")
	}
	bundle, _ := os.ReadFile(filepath.Join(dir, "web", "app.min.js"))
	trigrams := map[string]bool{}
	for i := 0; i+3 <= len(bundle); i++ {
		trigrams[string(bundle[i:i+3])] = true
	}
	if len(trigrams) <= 20000 {
		t.Errorf("web/app.min.js has %d distinct trigrams, want over zoekt's 20000", len(trigrams))
	}
	if gen, _ := filepath.Glob(filepath.Join(dir, "gen", "gen_*.go")); len(gen) != genFiles {
		t.Errorf("%d generated files, want %d", len(gen), genFiles)
	}
}

// The demo runs as a program, from its own module, and ends with its line.
func TestDemoRunsAsAProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("zoekt cannot run on windows")
	}
	cmd := exec.Command("go", "run", ".")
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Code search example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}
}

// Where zoekt cannot run the demo says so and succeeds, which is the fallback
// an embedder writes: carry on with Options.Index nil.
func TestDemoOnAPlatformWithoutZoekt(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("only windows has no zoekt")
	}
	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "code search is not available on this platform") {
		t.Errorf("output:\n%s", b.String())
	}
}
