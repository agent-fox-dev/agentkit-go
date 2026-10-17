package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TS-23-52: adding mcpserve leaves the root module's go.mod, its module graph
// snapshot, internal/policy/testdata and the exported-API golden as they were.
// The root go.mod does not mention mcpserve, and the files are unchanged against
// the checked-out commit; the graph snapshot and the golden are checked by
// TS-13-65, TS-19-64 and TS-20-62 against their testdata. 23-REQ-9.4.
func TestRootIsUnchangedByMcpserve_TS23_52(t *testing.T) {
	root := repoRoot(t)
	if reqs := parseGoMod(t, root); reqs[mcpservePath].version != "" {
		t.Errorf("the root go.mod requires %s", mcpservePath)
	}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "mcpserve") {
		t.Error("the root go.mod mentions mcpserve")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	head := exec.Command("git", "rev-parse", "--verify", "HEAD")
	head.Dir = root
	if err := head.Run(); err != nil {
		t.Skipf("no commit to compare with: %v", err)
	}
	cmd := exec.Command("git", "status", "--porcelain", "--", "go.mod", "go.sum", "internal/policy/testdata")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if len(out) > 0 {
		t.Errorf("the root go.mod, go.sum or internal/policy/testdata has changes:\n%s", out)
	}
}

// TS-23-53 (integration): make check passes offline in every module, mcpserve
// included, with its go.sum committed. It runs the real Makefile with the real
// toolchain and GOPROXY=off, so it costs a whole fast-suite run: skipped under
// -short, and in the run it starts itself. It is TS-19-66's check for the fast
// gate; TS-19-66 itself runs make check-release. 23-REQ-9.5.
func TestMakeCheckOfflineWithMcpserve_TS23_53(t *testing.T) {
	if testing.Short() {
		t.Skip("runs make check; skipped under -short")
	}
	if os.Getenv(inMakeCheck) != "" {
		t.Skip("already running inside make check")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "mcpserve", "go.sum")); err != nil {
		t.Fatalf("mcpserve/go.sum is not committed: %v", err)
	}
	cmd := exec.Command("make", "check")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off", inMakeCheck+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make check offline: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "go test -short ./... (mcpserve)") {
		t.Errorf("make check did not run the mcpserve tests:\n%s", out)
	}
}
