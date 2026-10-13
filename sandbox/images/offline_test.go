package images

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// skipHeavy skips the subprocess checks under -short and in a nested run.
func skipHeavy(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("runs go test in a subprocess; skipped under -short")
	}
	if os.Getenv(nestedVar) != "" {
		t.Skip("nested run")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go is not on PATH")
	}
	return goBin
}

// goIn runs go with args in dir with env and returns its combined output
// and error.
func goIn(goBin, dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command(goBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// TS-16-27: the offline sandbox tests pass while podman and docker stubs
// record that no container engine was invoked. 16-REQ-8.4. They run in
// the nested sandbox module (docs/errata/16_sandbox_images.md).
func TestOfflineTestsInvokeNoEngine_TS16_27(t *testing.T) {
	goBin := skipHeavy(t)
	marker := filepath.Join(t.TempDir(), "marker")
	stubs := engineStubs(t, marker, "1")
	out, err := goIn(goBin, filepath.Join(repoRoot(t), "sandbox"), subprocessEnv("PATH="+stubs+":"+os.Getenv("PATH")),
		"test", "-count=1", "./internal/buildver/", ".", "./images/")
	if err != nil {
		t.Fatalf("go test: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("a container engine ran: %s", b)
	}
}

// TS-16-28: sandbox/images holds only test files, and the cross-target and
// host-cgo gates still pass. 16-REQ-8.5.
func TestGatesWithImagesPackage_TS16_28(t *testing.T) {
	goBin := skipHeavy(t)
	root := repoRoot(t)
	env := subprocessEnv("PATH=" + os.Getenv("PATH"))
	out, err := goIn(goBin, filepath.Join(root, "sandbox"), env, "list", "-f", "{{len .GoFiles}} {{len .CgoFiles}} {{len .TestGoFiles}} {{len .XTestGoFiles}}", "./images/")
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	var gofiles, cgofiles, tests, xtests int
	if _, err := fmt.Sscan(strings.TrimSpace(out), &gofiles, &cgofiles, &tests, &xtests); err != nil || gofiles != 0 || cgofiles != 0 || tests+xtests < 1 {
		t.Errorf("sandbox/images files: %q", out)
	}
	out, err = goIn(goBin, root, env, "test", "-count=1", "-v", "-run", "TestCrossTargetBuildAndVet|TestHostCgoBuildAndVet", "./internal/policy/")
	if err != nil || !strings.Contains(out, "--- PASS: TestCrossTargetBuildAndVet") || !strings.Contains(out, "--- PASS: TestHostCgoBuildAndVet") {
		t.Errorf("the gates: %v\n%s", err, out)
	}
}

// TS-16-29: no Go dependency is added and the line budgets hold.
// 16-REQ-8.6. The new code is in the nested sandbox module, so its
// dependencies are checked against that module's requirements, which
// TS-14-42 and TS-15-40 pin to the allowlist.
func TestNoNewDependency_TS16_29(t *testing.T) {
	goBin := skipHeavy(t)
	root := repoRoot(t)
	env := subprocessEnv("PATH=" + os.Getenv("PATH"))
	for _, c := range []struct{ dir, run, pkg string }{
		{root, "TestRemovedDependenciesPruned_TS09_15|TestDirectDependenciesAreApproved_TS09_16", "./internal/policy/"},
		{root, "TestLineBudgets_TS12_14", "./core/"},
	} {
		out, err := goIn(goBin, c.dir, env, "test", "-count=1", "-v", "-run", c.run, c.pkg)
		if err != nil || strings.Contains(out, "--- FAIL") || !strings.Contains(out, "--- PASS") {
			t.Errorf("%s: %v\n%s", c.run, err, out)
		}
	}
	sb := filepath.Join(root, "sandbox")
	out, err := goIn(goBin, sb, env, "list", "-deps", "-test", "-f", "{{if not .Standard}}{{.Module.Path}}{{end}}", "./images/", "./internal/buildver/")
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	required := map[string]bool{"github.com/agent-fox-dev/agentkit-go": true, "github.com/agent-fox-dev/agentkit-go/sandbox": true}
	for _, m := range regexp.MustCompile(`(?m)^\s*(\S+) v\S+`).FindAllStringSubmatch(readRepo(t, "sandbox/go.mod"), -1) {
		required[m[1]] = true
	}
	for _, mod := range strings.Fields(out) {
		if !required[mod] {
			t.Errorf("the image tests or buildver depend on %s, which sandbox/go.mod does not require", mod)
		}
	}
}
