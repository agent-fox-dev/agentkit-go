package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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

// strippedPath is a directory of symlinks to every executable on PATH, first
// found first, except those named in hide.
func strippedPath(t *testing.T, hide ...string) string {
	t.Helper()
	bin := t.TempDir()
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if slices.Contains(hide, name) {
				continue
			}
			if _, err := os.Lstat(filepath.Join(bin, name)); err == nil {
				continue
			}
			info, err := os.Stat(filepath.Join(dir, name))
			if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				continue
			}
			if err := os.Symlink(filepath.Join(dir, name), filepath.Join(bin, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return bin
}

// TS-19-66: make check, every gatewayd test included, passes with no
// container engine, no protoc and no module download; the opt-in engine
// tests skip. 19-REQ-9.7.
func TestMakeCheckOffline_TS19_66(t *testing.T) {
	if testing.Short() {
		t.Skip("make check is skipped under -short")
	}
	if os.Getenv(inMakeCheck) != "" {
		t.Skip("already running inside make check")
	}
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not on PATH")
	}
	bin := strippedPath(t, "podman", "docker", "protoc")
	root := repoRoot(t)
	env := append(withPath(bin), "GOPROXY=off", inMakeCheck+"=1")
	cmd := exec.Command(filepath.Join(bin, "make"), "check")
	cmd.Dir, cmd.Env = root, env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make check offline: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd") {
		t.Errorf("make check did not run the gatewayd tests:\n%s", out)
	}
	opt := exec.Command(filepath.Join(bin, "go"), "test", "-count=1", "-v", "-run", "Podman", ".")
	opt.Dir, opt.Env = filepath.Join(root, "sandbox", "gatewayd"), env
	vout, err := opt.CombinedOutput()
	if err != nil {
		t.Fatalf("the opt-in tests offline: %v\n%s", err, vout)
	}
	if !strings.Contains(string(vout), "--- SKIP") || strings.Contains(string(vout), "--- PASS") {
		t.Errorf("the opt-in engine tests did not skip with no engine:\n%s", vout)
	}
}
