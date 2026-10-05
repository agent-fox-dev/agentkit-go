// Package policy holds AgentKit's executable invariants. It carries no
// non-test source and nothing imports it.
//
// The dependency policy is: the standard library is preferred, a third-party
// module is allowed when docs/DEPS.md says why it earns its place, and cgo is
// never allowed. REQ-GO-11's earlier rule — the root module requires nothing
// outside the standard library, enforced by a hard allowlist — was relaxed
// (docs/errata/dependency_policy.md): holding every dependency to a test-file
// allowlist made the codebase harder to work in than the property was worth.
//
// What stays executable is what is not a matter of taste. A cgo dependency
// breaks cross-compilation, so TestNoCgoOutsideStdlib rejects one, and
// TestCrossTargetBuildAndVet builds the four supported targets. Module count
// is no longer gated; go.mod is the authority on what the root requires and
// docs/DEPS.md records why.
package policy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// goListDeps returns one line per package in the transitive build graph of
// ./... as "importPath|modulePath|numCgoFiles".
//
// CGO_ENABLED=1 is forced into the child environment and that is load-bearing,
// not incidental. With cgo disabled the toolchain excludes cgo files by build
// constraint, so a cgo-requiring dependency reports zero CgoFiles and this
// check passes while the dependency is present. Verified against the standard
// library on go1.24.7: `net` reports 5 CgoFiles under CGO_ENABLED=1 and 0
// under CGO_ENABLED=0. A cgo-off gate is not a weaker version of this check;
// it cannot see the thing it claims to check.
func goListDeps(t *testing.T) []string {
	t.Helper()

	cmd := exec.Command("go", "list", "-deps",
		"-f", "{{.ImportPath}}|{{with .Module}}{{.Path}}{{end}}|{{len .CgoFiles}}",
		"./...")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")

	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list -deps failed: %v\n%s", err, stderr)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locating module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// TestNoCgoOutsideStdlib fails on any non-stdlib package that ships cgo files.
// A cgo-requiring dependency breaks cross-compilation for the platform matrix
// of NFR-COMPAT-06, whatever else is said in its favour. This is the one
// property of the dependency graph that is still gated: cgo-freedom, not
// module count, is what determines whether AgentKit cross-compiles.
func TestNoCgoOutsideStdlib(t *testing.T) {
	for _, line := range goListDeps(t) {
		f := strings.Split(line, "|")
		if len(f) != 3 {
			continue
		}
		importPath, modPath, cgoFiles := f[0], f[1], f[2]
		if modPath == "" || cgoFiles == "0" {
			continue
		}
		t.Errorf(`cgo dependency: %s (module %s) ships %s cgo file(s)

cgo breaks the cross-target build gate of NFR-COMPAT-06 (linux/amd64,
linux/arm64, darwin/arm64, windows/amd64). Remove it, or move it behind a
nested module that the cross-target gate does not build.`, importPath, modPath, cgoFiles)
	}
}

// TestCgoProbeIsArmed guards the guard. If CGO_ENABLED did not reach the child
// process, TestNoCgoOutsideStdlib would silently pass on a cgo dependency.
//
// Earlier versions checked that at least one cgo-carrying stdlib package
// appeared in the project's own dep graph. Go 1.27 removed cgo from `net` and
// other packages this project reaches, so the probe now verifies the mechanism
// directly: CGO_ENABLED=1 must reach the child, and a known cgo-carrying
// stdlib package (runtime/cgo) must report cgo files under that setting.
func TestCgoProbeIsArmed(t *testing.T) {
	root := repoRoot(t)

	envCmd := exec.Command("go", "env", "CGO_ENABLED")
	envCmd.Dir = root
	envCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err := envCmd.Output()
	if err != nil {
		t.Fatalf("go env CGO_ENABLED: %v", err)
	}
	if strings.TrimSpace(string(out)) != "1" {
		t.Fatal("CGO_ENABLED=1 is not reaching the child process")
	}

	listCmd := exec.Command("go", "list", "-f", "{{len .CgoFiles}}", "runtime/cgo")
	listCmd.Dir = root
	listCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	out, err = listCmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list runtime/cgo: %v\n%s", err, stderr)
	}
	if strings.TrimSpace(string(out)) == "0" {
		t.Fatal(`runtime/cgo reports zero CgoFiles under CGO_ENABLED=1.

The toolchain is not honoring CGO_ENABLED=1, so TestNoCgoOutsideStdlib
cannot detect cgo dependencies.`)
	}
}
