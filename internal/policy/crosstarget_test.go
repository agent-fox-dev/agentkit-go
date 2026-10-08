// Package policy holds AgentKit's executable build invariants. It carries no
// non-test source and nothing imports it.
//
// cgo is allowed, but only in files constrained by //go:build cgo, and every
// package keeps a pure-Go fallback (docs/prd/09-replace-hand-rolled-code-with-libraries.md,
// decision D1). Two gates follow from that: TestCrossTargetBuildAndVet builds
// the pure-Go fallback for every supported target, and TestHostCgoBuildAndVet
// builds the cgo variant for the host.
package policy

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locating module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// crossTargets is the supported matrix of NFR-COMPAT-06. It is a list in a
// test, not a line in a Makefile: the README promised a cross-target gate and
// nothing ran it, and a gate that exists only as prose is indistinguishable
// from no gate.
var crossTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
}

// TestCrossTargetBuildAndVet runs `go build ./...` and `go vet ./...` for every
// supported GOOS/GOARCH, UNCONDITIONALLY — not only when a build-constrained
// file changes.
//
// The unconditional part is the requirement's whole argument. Platform-
// constrained files are called from unconstrained code, so an ordinary rename
// breaks a target while touching no constrained file and leaving the host
// suite fully green. A gate that keys on "did a _windows.go file change" cannot
// see that; only building the target can.
//
// CGO_ENABLED=0 is forced. A cross build needs it anyway (there is no cross C
// toolchain here), and it is the honest setting: NFR-COMPAT-06 is a promise
// about the pure-Go build, which is the fallback every cgo-tagged file must
// have.
func TestCrossTargetBuildAndVet(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-target build gate skipped under -short")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH; the cross-target gate needs the toolchain")
	}
	root := repoRoot(t)

	for _, target := range crossTargets {
		target := target
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			for _, verb := range []string{"build", "vet"} {
				cmd := exec.Command("go", verb, "./...")
				cmd.Dir = root
				cmd.Env = append(os.Environ(),
					"GOOS="+target.goos,
					"GOARCH="+target.goarch,
					"CGO_ENABLED=0",
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Errorf(`go %s ./... failed for %s/%s: %v
%s
NFR-COMPAT-06 requires every release gate to build and vet all four supported
targets. This target is broken on the host's green suite, which is exactly the
failure the requirement names: platform-constrained files are called from
unconstrained code, so the host build cannot see a break in another target.
Fix the target; do not narrow the matrix.`, verb, target.goos, target.goarch, err,
						strings.TrimSpace(string(out)))
					return
				}
			}
		})
	}
}

// TestHostCgoBuildAndVet builds and vets the host target with cgo ON, which is
// the only build that compiles the //go:build cgo files. The cross-target gate
// cannot see them, so without this a broken cgo file would leave the suite
// green whenever the tests themselves run with cgo off.
func TestHostCgoBuildAndVet(t *testing.T) {
	if testing.Short() {
		t.Skip("host cgo build gate skipped under -short")
	}
	root := repoRoot(t)
	for _, verb := range []string{"build", "vet"} {
		cmd := exec.Command("go", verb, "./...")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("go %s ./... with CGO_ENABLED=1 failed: %v\n%s", verb, err,
				strings.TrimSpace(string(out)))
		}
	}
}
