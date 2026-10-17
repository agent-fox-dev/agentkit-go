package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
