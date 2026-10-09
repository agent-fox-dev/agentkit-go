package policy

import (
	"os"
	"os/exec"
	"testing"
)

// inMakeCheck marks a test binary that make check itself started, so the
// smoke test below does not run make check again from inside it.
const inMakeCheck = "AGENTKIT_IN_MAKE_CHECK"

// TS-09-20 (smoke, 09-PATH-1): make check — gofmt, go vet, lint and the
// tests of the root module and codesearch, this package's allowlist among
// them — exits zero.
//
// It runs the real Makefile with the real toolchain, so it costs a whole
// second suite run: skipped under -short, and skipped in the run make check
// starts, which is the one already being measured.
func TestMakeCheckSucceeds_TS09_20(t *testing.T) {
	if testing.Short() {
		t.Skip("make check is skipped under -short")
	}
	if os.Getenv(inMakeCheck) != "" {
		t.Skip("already running inside make check")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	cmd := exec.Command("make", "check")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), inMakeCheck+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make check: %v\n%s", err, out)
	}
	if code := cmd.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("make check exited %d\n%s", code, out)
	}
}
