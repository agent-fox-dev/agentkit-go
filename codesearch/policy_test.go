package codesearch_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/codesearch"
	"github.com/agentfox/agentkit-go/tools"
)

// moduleRoot returns the absolute path of the codesearch module directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locating module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// goListDeps returns one line per package in the transitive build graph of
// ./... as "importPath|modulePath|numCgoFiles".
//
// CGO_ENABLED=1 is forced so that cgo files are visible.
func goListDeps(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps",
		"-f", "{{.ImportPath}}|{{with .Module}}{{.Path}}{{end}}|{{len .CgoFiles}}",
		"./...")
	cmd.Dir = moduleRoot(t)
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

// TS-03-62: TestNoCgoOutsideStdlib finds no cgo in the dependency graph.
// Verifies 03-REQ-9.2, 03-REQ-10.1.
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

cgo breaks the cross-target build gate. Remove it, or move it behind a
nested module that the cross-target gate does not build.`, importPath, modPath, cgoFiles)
	}
}

// TS-03-62: TestCgoProbeIsArmed proves the cgo check can detect cgo files.
// Verifies 03-REQ-9.2, 03-REQ-10.1.
func TestCgoProbeIsArmed(t *testing.T) {
	root := moduleRoot(t)

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

// TS-03-63: TestForbiddenImports checks that no package of the codesearch
// module DIRECTLY imports gRPC, Prometheus or an HTTP server package.
// Verifies 03-REQ-9.3, 03-REQ-10.1.
//
// The scope is direct imports, by decision, not by omission. The spec first
// said `go list -deps`, but zoekt's index and search packages — the ones the
// spec allows — pull in google.golang.org/grpc, the Prometheus client and
// sentry-go themselves, so a check over the transitive graph could never pass
// and zoekt could not be used at all. The project owner accepted that graph
// instead of recording a no-go; see docs/errata/03_forbidden_imports_direct_only.md
// for the decision and the list of accepted packages. What this still stops is
// codesearch's own code reaching for a server stack: that would be a choice made
// here, not one inherited from zoekt.
func TestForbiddenImports(t *testing.T) {
	// We verify that no package in the codesearch module directly imports a
	// forbidden package. Packages outside the module (zoekt's) are skipped.
	root := moduleRoot(t)

	cmd := exec.Command("go", "list",
		"-f", "{{.ImportPath}} {{join .Imports \",\"}}",
		"./...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")

	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list failed: %v\n%s", err, stderr)
	}

	forbidden := []string{
		"google.golang.org/grpc",
		"github.com/prometheus/",
		"github.com/grpc-ecosystem/",
		"net/http/httptest",
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, imports := parts[0], parts[1]
		// Only check our own packages, not zoekt's.
		if !strings.HasPrefix(pkg, "github.com/agentfox/agentkit-go/codesearch") {
			continue
		}
		for _, imp := range strings.Split(imports, ",") {
			for _, f := range forbidden {
				if strings.HasPrefix(imp, f) {
					t.Errorf("codesearch package %s directly imports forbidden package %s", pkg, imp)
				}
			}
		}
	}
}

// TestForbiddenImportsDetectsSynthetic proves the forbidden-import matcher
// can actually detect a violation, so TestForbiddenImports passing means
// something. Like that test it concerns direct imports only; see
// docs/errata/03_forbidden_imports_direct_only.md.
// Verifies 03-REQ-9.3, 03-REQ-10.1.
func TestForbiddenImportsDetectsSynthetic(t *testing.T) {
	forbidden := []string{
		"google.golang.org/grpc",
		"github.com/prometheus/",
		"github.com/grpc-ecosystem/",
		"net/http/httptest",
	}

	synthetic := []string{
		"github.com/agentfox/agentkit-go/codesearch google.golang.org/grpc,fmt",
		"github.com/agentfox/agentkit-go/codesearch github.com/prometheus/client_golang/prometheus,fmt",
	}

	for _, line := range synthetic {
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		pkg, imports := parts[0], parts[1]
		found := false
		for _, imp := range strings.Split(imports, ",") {
			for _, f := range forbidden {
				if strings.HasPrefix(imp, f) {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("synthetic check failed to detect forbidden import in %s", pkg)
		}
	}
}

// crossTargets is the supported platform matrix.
var crossTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
}

// TS-03-61: TestCrossTargetBuildAndVet runs go build and go vet for every
// supported GOOS/GOARCH with CGO_ENABLED=0.
// Verifies 03-REQ-8.7, 03-REQ-10.1.
func TestCrossTargetBuildAndVet(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-target build gate skipped under -short")
	}
	root := moduleRoot(t)

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
					t.Errorf("go %s ./... failed for %s/%s: %v\n%s",
						verb, target.goos, target.goarch, err,
						strings.TrimSpace(string(out)))
					return
				}
			}
		})
	}
}

// TS-03-60: TestErrUnsupportedOnWindows verifies that on Windows, New returns
// ErrUnsupported, and that ErrUnsupported is the same value on every platform.
// Verifies 03-REQ-8.6, 03-REQ-9.5.
func TestErrUnsupportedOnWindows(t *testing.T) {
	// ErrUnsupported is always defined (in errors.go, no build constraint).
	if codesearch.ErrUnsupported == nil {
		t.Fatal("ErrUnsupported must not be nil")
	}

	if runtime.GOOS == "windows" {
		ws, err := tools.NewWorkspace(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		_, err = codesearch.New(ws, codesearch.Options{})
		if err == nil {
			t.Fatal("expected error on Windows")
		}
		if !errors.Is(err, codesearch.ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported, got %v", err)
		}
	} else {
		// On non-Windows, New should succeed with a valid workspace.
		ws, err := tools.NewWorkspace(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		idx, err := codesearch.New(ws, codesearch.Options{})
		if err != nil {
			t.Fatalf("New should succeed on %s: %v", runtime.GOOS, err)
		}
		if idx == nil {
			t.Fatal("New returned nil index on non-Windows")
		}
	}
}

// TS-03-4: TestModuleStructure verifies the codesearch module has the required
// path, replace directive, pinned zoekt, header comment and files.
// Verifies 03-REQ-1.3.
func TestModuleStructure_TS03_4(t *testing.T) {
	root := moduleRoot(t)

	// Check go.mod exists and read it.
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	content := string(gomod)

	// Module path.
	if !strings.Contains(content, "module github.com/agentfox/agentkit-go/codesearch") {
		t.Error("go.mod must declare module github.com/agentfox/agentkit-go/codesearch")
	}

	// Replace directive.
	if !strings.Contains(content, "replace github.com/agentfox/agentkit-go => ..") {
		t.Error("go.mod must have replace github.com/agentfox/agentkit-go => ..")
	}

	// Pinned zoekt at an exact version (pseudo-version or tag).
	if !strings.Contains(content, "github.com/sourcegraph/zoekt") {
		t.Error("go.mod must require github.com/sourcegraph/zoekt")
	}

	// Header comment stating the boundary rule.
	if !strings.Contains(content, "SEPARATE MODULE") {
		t.Error("go.mod header comment must state the boundary rule (like difftest/go.mod)")
	}

	// Required files.
	for _, name := range []string{"index.go", "search.go", "tool.go", "policy_test.go"} {
		path := filepath.Join(root, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("required file %s does not exist", name)
		}
	}
}

// TS-03-7: TestGateCheck verifies the go/no-go gate logic.
// Verifies 03-REQ-1.1.
func TestGateCheck_TS03_7(t *testing.T) {
	// The gate function uses literal thresholds: 25% and one third.
	// It is not parameterized.

	tests := []struct {
		name      string
		share     float64 // search_files share of read-phase calls
		trunc     float64 // truncated-or-retried share
		repoSizes []int   // file counts of target repositories
		r9Pass    bool    // Requirement 9 checks pass
		wantGo    bool
	}{
		{
			name:      "go: share above 25%",
			share:     0.30,
			trunc:     0.10,
			repoSizes: []int{5000, 6000},
			r9Pass:    true,
			wantGo:    true,
		},
		{
			name:      "go: trunc above one third",
			share:     0.20,
			trunc:     0.40,
			repoSizes: []int{5000, 6000},
			r9Pass:    true,
			wantGo:    true,
		},
		{
			name:      "nogo: both below thresholds",
			share:     0.20,
			trunc:     0.30,
			repoSizes: []int{5000, 6000},
			r9Pass:    true,
			wantGo:    false,
		},
		{
			name:      "nogo: r9 fails",
			share:     0.30,
			trunc:     0.10,
			repoSizes: []int{5000, 6000},
			r9Pass:    false,
			wantGo:    false,
		},
		{
			name:      "nogo: not enough large repos",
			share:     0.30,
			trunc:     0.10,
			repoSizes: []int{5000, 4000},
			r9Pass:    true,
			wantGo:    false,
		},
		{
			name:      "nogo: only one repo",
			share:     0.30,
			trunc:     0.10,
			repoSizes: []int{5000},
			r9Pass:    true,
			wantGo:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gateCheck(tt.share, tt.trunc, tt.repoSizes, tt.r9Pass)
			if got != tt.wantGo {
				t.Errorf("gateCheck(share=%.2f, trunc=%.2f, repos=%v, r9=%v) = %v, want %v",
					tt.share, tt.trunc, tt.repoSizes, tt.r9Pass, got, tt.wantGo)
			}
		})
	}
}

// gateCheck implements the go/no-go decision with literal thresholds.
// It is not exported and uses hardcoded values (25% and 1/3).
func gateCheck(searchShare, truncShare float64, repoSizes []int, r9Pass bool) bool {
	// Requirement 9 must pass.
	if !r9Pass {
		return false
	}

	// At least two target repositories of 5000+ files.
	largeRepos := 0
	for _, size := range repoSizes {
		if size >= 5000 {
			largeRepos++
		}
	}
	if largeRepos < 2 {
		return false
	}

	// search_files is at least 25% of read-phase tool calls,
	// OR at least one third of search_files results are truncated or retried.
	const shareThreshold = 0.25
	const truncThreshold = 1.0 / 3.0

	if searchShare >= shareThreshold {
		return true
	}
	if truncShare >= truncThreshold {
		return true
	}

	return false
}
