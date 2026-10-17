package policy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The markers of the child processes below: a test binary re-executed to run
// one test that is meant to fail, selected by an environment variable so it
// does nothing in the ordinary run.
const (
	ts24MissingDir  = "AGENTKIT_TS24_MISSING_DIR"
	ts24RequireLine = "AGENTKIT_TS24_REQUIRE_LINE"
)

// runTS24Child re-executes this test binary to run the one test named test,
// with env added, and returns what it printed and how it ended.
func runTS24Child(t *testing.T, test string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^"+test+"$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// requireLine fails the test when lines, the exported API of the package in
// pkg, is empty or has no line that begins with prefix. The message names the
// package and the prefix.
func requireLine(t *testing.T, pkg string, lines []string, prefix string) {
	t.Helper()
	if len(lines) == 0 {
		t.Errorf("the exported API of %s is empty, want a line beginning %q", pkg, prefix)
		return
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, prefix) }) {
		t.Errorf("the exported API of %s has no line beginning %q", pkg, prefix)
	}
}

// TS-24-1: apiPackages is the ten packages PRD 12 changed or added, existing
// entries first. 24-REQ-1.1.
func TestAPIPackagesAreTheTenExpected_TS24_1(t *testing.T) {
	want := []string{".", "core", "tools", "codemode", "workspace", "guard", "provider/faux", "sandbox", "codesearch", "mcpserve"}
	if len(apiPackages) != len(want) {
		t.Errorf("apiPackages has %d entries, want %d: %q", len(apiPackages), len(want), apiPackages)
	}
	for i := range min(len(apiPackages), len(want)) {
		if apiPackages[i] != want[i] {
			t.Errorf("apiPackages[%d] = %q, want %q", i, apiPackages[i], want[i])
		}
	}
}

// TS-24-2: exportedAPI sees the identifiers that made guard, provider/faux,
// sandbox and mcpserve join the golden. 24-REQ-2.1, 2.2, 2.3 and 2.5.
func TestExportedAPIHasThePRD12Identifiers_TS24_2(t *testing.T) {
	root := repoRoot(t)
	for _, c := range []struct {
		pkg      string
		prefixes []string
	}{
		{"guard", []string{"func Allow("}},
		{"provider/faux", []string{"func ToolCall(", "func Text("}},
		{"sandbox", []string{"func Open("}},
		{"mcpserve", []string{"func New(", "type Endpoint"}},
	} {
		lines := exportedAPI(t, filepath.Join(root, c.pkg))
		for _, prefix := range c.prefixes {
			requireLine(t, c.pkg, lines, prefix)
		}
	}
}

// TS-24-3: codesearch's exported API takes New over a *workspace.Workspace,
// and lists the Windows stub's declarations beside the real ones, exactly as
// exportedAPI prints them. 24-REQ-2.4.
func TestCodesearchAPIListsTheStubBesideTheRealOnes_TS24_3(t *testing.T) {
	lines := exportedAPI(t, filepath.Join(repoRoot(t), "codesearch"))
	if len(lines) == 0 {
		t.Fatal("the exported API of codesearch is empty")
	}
	var newOverWorkspace, realReceiver, stubReceiver bool
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "func New(") && strings.Contains(l, "*workspace.Workspace"):
			newOverWorkspace = true
		case strings.HasPrefix(l, "func (idx "):
			realReceiver = true
		case strings.HasPrefix(l, "func (*Index) "):
			stubReceiver = true
		}
		if strings.HasPrefix(l, "type Options ") && strings.Contains(l, "Ignore") {
			t.Errorf("codesearch.Options has an Ignore field again: %s", l)
		}
	}
	if !newOverWorkspace {
		t.Error("no line begins func New( and takes a *workspace.Workspace")
	}
	if !realReceiver {
		t.Error("no method line has the real receiver name (idx)")
	}
	if !stubReceiver {
		t.Error("no method line has the Windows stub's unnamed receiver (*Index)")
	}
}

// TS-24-6: a directory in apiPackages that does not exist fails the test with
// its path. It does not skip. 24-REQ-1.4. The child is TS-13-67's own call,
// exportedAPI, on a directory that is not there.
func TestMissingAPIDirectoryFails_TS24_6(t *testing.T) {
	missing := filepath.Join(repoRoot(t), "no-such-pkg")
	out, err := runTS24Child(t, "TestMissingAPIDirectoryChild_TS24_6", ts24MissingDir+"="+missing)
	if err == nil {
		t.Fatalf("the child passed on a directory that does not exist:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("the child did not report FAIL:\n%s", out)
	}
	if strings.Contains(out, "SKIP") {
		t.Errorf("the child skipped:\n%s", out)
	}
	if !strings.Contains(out, missing) {
		t.Errorf("the output does not name %s:\n%s", missing, out)
	}
}

// TestMissingAPIDirectoryChild_TS24_6 is the child of TS-24-6.
func TestMissingAPIDirectoryChild_TS24_6(t *testing.T) {
	dir := os.Getenv(ts24MissingDir)
	if dir == "" {
		t.Skip("runs only as the child of TS-24-6")
	}
	exportedAPI(t, dir)
}

// TS-24-7: TS-17-61 keeps its own guard list, led by Allow, and every entry
// of it is among guard's exported lines. 24-REQ-1.5. TS-17-61 itself compares
// the lists.
func TestPreSpec17GuardIsKept_TS24_7(t *testing.T) {
	if got, want := preSpec17Guard[0], "func Allow(programs ...string) core.BeforeToolCall"; got != want {
		t.Errorf("preSpec17Guard[0] = %q, want %q", got, want)
	}
	lines := exportedAPI(t, filepath.Join(repoRoot(t), "guard"))
	for _, entry := range preSpec17Guard {
		if !slices.Contains(lines, entry) {
			t.Errorf("preSpec17Guard has %q, which guard does not export", entry)
		}
	}
}

// TS-24-8: requireLine fails and names the package and the prefix when the
// list is empty. 24-REQ-2.6.
func TestRequireLineNamesThePackageAndPrefix_TS24_8(t *testing.T) {
	out, err := runTS24Child(t, "TestRequireLineChild_TS24_8", ts24RequireLine+"=1")
	if err == nil {
		t.Fatalf("the child passed on an empty list:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("the child did not report FAIL:\n%s", out)
	}
	for _, want := range []string{"guard", "func Allow("} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not contain %q:\n%s", want, out)
		}
	}

	// A list that is there but lacks the prefix fails too, with both names.
	out, err = runTS24Child(t, "TestRequireLineChild_TS24_8", ts24RequireLine+"=lacks")
	if err == nil {
		t.Fatalf("the child passed on a list without the prefix:\n%s", out)
	}
	for _, want := range []string{"guard", "func Allow("} {
		if !strings.Contains(out, want) {
			t.Errorf("the output for a list without the prefix does not contain %q:\n%s", want, out)
		}
	}
}

// TestRequireLineChild_TS24_8 is the child of TS-24-8.
func TestRequireLineChild_TS24_8(t *testing.T) {
	switch os.Getenv(ts24RequireLine) {
	case "":
		t.Skip("runs only as the child of TS-24-8")
	case "lacks":
		requireLine(t, "guard", []string{"func Check(argv []string, o Options) Decision"}, "func Allow(")
	default:
		requireLine(t, "guard", nil, "func Allow(")
	}
}

// goldenFile is the committed exported-API golden.
func goldenFile(root string) string {
	return filepath.Join(root, "internal", "policy", "testdata", "exported-api.golden")
}

// goldenText is the golden as TS-13-67 writes it: one "== <pkg>" header for
// each apiPackages entry, in order, then that package's exported lines.
func goldenText(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	for _, pkg := range apiPackages {
		b.WriteString("== " + pkg + "\n")
		for _, line := range exportedAPI(t, filepath.Join(root, pkg)) {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// goldenSections splits golden text into its sections, in file order: each
// header (without "== ") and the lines under it.
func goldenSections(golden string) (headers []string, lines map[string][]string) {
	lines = map[string][]string{}
	cur := ""
	for _, line := range strings.Split(strings.TrimSuffix(golden, "\n"), "\n") {
		if name, ok := strings.CutPrefix(line, "== "); ok {
			cur = name
			headers = append(headers, name)
			continue
		}
		lines[cur] = append(lines[cur], line)
	}
	return headers, lines
}

// withoutUpdateVariable is the environment of a go test child that compares
// the golden instead of writing it.
func withoutUpdateVariable() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "AGENTKIT_UPDATE_API_GOLDEN=") {
			env = append(env, kv)
		}
	}
	return env
}

// goTestPolicy runs go test over this package for the tests that match run, in
// the repository root with the update variable unset, and returns its output
// and how it ended.
func goTestPolicy(t *testing.T, run string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", "test", "./internal/policy", "-run", run, "-v", "-count=1")
	cmd.Dir, cmd.Env = repoRoot(t), withoutUpdateVariable()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TS-24-4: the golden's section headers are apiPackages, in order, and
// nothing else. 24-REQ-1.2.
func TestGoldenHeadersEqualAPIPackages_TS24_4(t *testing.T) {
	raw, err := os.ReadFile(goldenFile(repoRoot(t)))
	if err != nil {
		t.Fatal(err)
	}
	headers, _ := goldenSections(string(raw))
	if !slices.Equal(headers, apiPackages) {
		t.Errorf("the golden's sections are %q, want apiPackages %q", headers, apiPackages)
	}
}

// TS-24-9: the five sections spec 23 left are the exported API of their
// packages, in the first five positions, and the golden's change against HEAD
// adds lines after the workspace section and removes none. 24-REQ-3.1. (The
// comparison with HEAD is empty once the golden is committed; the sections
// check holds for good.)
func TestExistingGoldenSectionsAreUnchanged_TS24_9(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(goldenFile(root))
	if err != nil {
		t.Fatal(err)
	}
	headers, sections := goldenSections(string(raw))
	existing := []string{".", "core", "tools", "codemode", "workspace"}
	if len(headers) < len(existing) || !slices.Equal(headers[:len(existing)], existing) {
		t.Fatalf("the golden's first sections are %q, want %q", headers, existing)
	}
	for _, pkg := range existing {
		if want := exportedAPI(t, filepath.Join(root, pkg)); !slices.Equal(sections[pkg], want) {
			t.Errorf("the golden's %s section differs from the package's exported API", pkg)
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Run(); err != nil {
		t.Skipf("no commit to compare the golden with: %v", err)
	}
	// firstNew is the 1-based line of the first header after the workspace
	// section, 0 when there is none.
	firstNew, inWorkspace := 0, false
	for i, line := range strings.Split(string(raw), "\n") {
		if line == "== workspace" {
			inWorkspace = true
		} else if inWorkspace && strings.HasPrefix(line, "== ") {
			firstNew = i + 1
			break
		}
	}
	cmd := exec.Command("git", "diff", "--no-color", "-U0", "HEAD", "--", "internal/policy/testdata/exported-api.golden")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git diff: %v", err)
	}
	hunk := regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "---"), strings.HasPrefix(line, "+++"):
		case strings.HasPrefix(line, "-"):
			t.Errorf("the golden loses a line: %s", line)
		case hunk.MatchString(line):
			start, _ := strconv.Atoi(hunk.FindStringSubmatch(line)[1])
			if firstNew > 0 && start < firstNew {
				t.Errorf("the golden changes line %d, before the first appended section at line %d: %s", start, firstNew, line)
			}
		}
	}
}

// TS-24-10 (property): for every apiPackages entry, exportedAPI returns the
// same lines on a repeated call, sorted and without a duplicate, and the golden
// built from them twice is the same bytes. 24-REQ-3.2.
func TestExportedAPIIsStable_TS24_10(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range apiPackages {
		a := exportedAPI(t, filepath.Join(root, pkg))
		b := exportedAPI(t, filepath.Join(root, pkg))
		if !slices.Equal(a, b) {
			t.Errorf("exportedAPI(%s) differs between two calls", pkg)
		}
		if !slices.IsSorted(a) {
			t.Errorf("exportedAPI(%s) is not sorted", pkg)
		}
		if len(slices.Compact(slices.Clone(a))) != len(a) {
			t.Errorf("exportedAPI(%s) lists a line twice", pkg)
		}
	}
	first, second := goldenText(t, root), goldenText(t, root)
	if first != second {
		t.Error("the golden built twice in a row is not the same bytes")
	}
}

// TS-24-5: the four golden comparison tests pass, and none skips, with the
// update variable unset. 24-REQ-1.3.
func TestGoldenComparisonsPassWithUpdateUnset_TS24_5(t *testing.T) {
	out, err := goTestPolicy(t, "_TS13_67$|_TS14_44$|_TS15_42$|_TS17_61$")
	if err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
	for _, id := range []string{"TS13_67", "TS14_44", "TS15_42", "TS17_61"} {
		if !regexp.MustCompile(`--- PASS: \w+_` + id + ` `).MatchString(out) {
			t.Errorf("%s did not report PASS:\n%s", id, out)
		}
	}
	if strings.Contains(out, "SKIP") {
		t.Errorf("a golden comparison skipped:\n%s", out)
	}
}

// TS-24-11: a section that differs from the code fails TS-13-67, which lists
// the line the code gained instead of absorbing it. 24-REQ-3.3. The test
// removes one line of the core section from the golden and puts the golden
// back byte for byte; it is not parallel to any other test.
func TestGoldenDifferenceIsListed_TS24_11(t *testing.T) {
	golden := goldenFile(repoRoot(t))
	backup, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.WriteFile(golden, backup, 0o644); err != nil {
			t.Errorf("the golden is not restored: %v", err)
		}
	}
	t.Cleanup(restore)

	lines := strings.Split(string(backup), "\n")
	at := slices.Index(lines, "== core") + 1
	if at == 0 || at >= len(lines) || strings.HasPrefix(lines[at], "== ") {
		t.Fatal("the golden has no line in its core section to remove")
	}
	dropped := lines[at]
	lines = slices.Delete(lines, at, at+1)
	if err := os.WriteFile(golden, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := goTestPolicy(t, "_TS13_67$")
	if err == nil {
		t.Fatalf("TS-13-67 passed against a golden missing a line:\n%s", out)
	}
	if !strings.Contains(out, "FAIL") {
		t.Errorf("the child did not report FAIL:\n%s", out)
	}
	if want := "exported API gained: " + dropped; !strings.Contains(out, want) {
		t.Errorf("the output lacks %q:\n%s", want, out)
	}

	restore()
	if now, err := os.ReadFile(golden); err != nil || !bytes.Equal(now, backup) {
		t.Errorf("the golden is not byte-identical after the test: %v", err)
	}
}

// TS-24-12: the golden is committed, so internal/policy/testdata has no
// change, and TS-19-64 passes. 24-REQ-3.4.
func TestPolicyTestdataIsCommitted_TS24_12(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	root := repoRoot(t)
	if err := exec.Command("git", "-C", root, "rev-parse", "--verify", "HEAD").Run(); err != nil {
		t.Skipf("no commit to compare internal/policy/testdata with: %v", err)
	}
	cmd := exec.Command("git", "status", "--porcelain", "--", "internal/policy/testdata")
	cmd.Dir = root
	status, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if len(status) > 0 {
		t.Errorf("internal/policy/testdata has uncommitted changes:\n%s", status)
	}
	if out, err := goTestPolicy(t, "_TS19_64$"); err != nil || !regexp.MustCompile(`--- PASS: \w+_TS19_64 `).MatchString(out) {
		t.Errorf("TS-19-64 did not pass: %v\n%s", err, out)
	}
}
