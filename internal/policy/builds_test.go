// Package policy holds the portability gates: builds only a real toolchain
// run proves. It carries no non-test source and nothing imports it.
//
// cgo is allowed only in files constrained by //go:build cgo, and every package
// keeps a pure-Go fallback (docs/prd/09-replace-hand-rolled-code-with-libraries.md,
// decision D1). TestCrossTargetBuildAndVet builds that fallback for every
// supported target; TestHostCgoBuildAndVet builds the cgo variant for the host.
// Both take minutes, so they skip under -short and run in make check-release.
package policy

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// crossTargets are the supported GOOS/GOARCH pairs.
var crossTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
}

// modules are the repository's Go modules, read from the Makefile's MODULES,
// as directories under the repository root.
func modules(t *testing.T) []string {
	t.Helper()
	root := testutil.RepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^MODULES\s*:?=(.*)$`).FindSubmatch(b)
	if m == nil || len(strings.Fields(string(m[1]))) == 0 {
		t.Fatal("the Makefile defines no MODULES")
	}
	var dirs []string
	for _, mod := range strings.Fields(string(m[1])) {
		dirs = append(dirs, filepath.Join(root, mod))
	}
	return dirs
}

// gate runs `go build ./...` and `go vet ./...` in each module with env added
// to the environment, and reports each failure with its output.
func gate(t *testing.T, mods []string, env ...string) {
	t.Helper()
	for _, dir := range mods {
		for _, args := range [][]string{buildArgs(t, dir), {"vet", "./..."}} {
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), env...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("go %s in %s with %v: %v\n%s", strings.Join(args, " "), dir, env, err, strings.TrimSpace(string(out)))
			}
		}
	}
}

// buildArgs is the build of ./... in dir. A module with a main package writes
// its binaries to a temporary directory, since a lone main package would land
// in the source tree; a library module takes a plain build, because go build
// -o refuses one with "no main packages to build".
func buildArgs(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f", "{{.Name}}", "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing the packages of %s: %v", dir, err)
	}
	if slices.Contains(strings.Fields(string(out)), "main") {
		return []string{"build", "-o", t.TempDir(), "./..."}
	}
	return []string{"build", "./..."}
}

// TestCrossTargetBuildAndVet builds and vets every module with cgo off for
// every supported target, unconditionally: platform-constrained files are
// called from unconstrained code, so a rename can break another target while
// the host suite stays green, and only building that target shows it.
func TestCrossTargetBuildAndVet(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-target build gate skipped under -short")
	}
	mods := modules(t)
	for _, target := range crossTargets {
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			gate(t, mods, "GOOS="+target.goos, "GOARCH="+target.goarch, "CGO_ENABLED=0")
		})
	}
}

// TestHostCgoBuildAndVet builds and vets every module for the host with cgo on,
// the only build that compiles the //go:build cgo files.
func TestHostCgoBuildAndVet(t *testing.T) {
	if testing.Short() {
		t.Skip("host cgo build gate skipped under -short")
	}
	gate(t, modules(t), "CGO_ENABLED=1")
}
