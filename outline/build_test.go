package outline_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// moduleRoot returns the module root directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("locating module root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// goListJSON is the subset of `go list -json` output we need.
type goListJSON struct {
	ImportPath string
	Imports    []string
	GoFiles    []string
	Dir        string
}

// TS-01-64: outline imports only the standard library and has no
// build-constrained or cgo file.
func TestOutline_StdlibOnly_TS_01_64(t *testing.T) {
	root := moduleRoot(t)

	// Run go list -json on the outline package.
	cmd := exec.Command("go", "list", "-json", "./outline")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("go list -json ./outline failed: %v\n%s", err, stderr)
	}

	var pkg goListJSON
	if err := json.Unmarshal(out, &pkg); err != nil {
		t.Fatalf("parsing go list output: %v", err)
	}

	// Check every import is a standard-library package.
	// Standard library packages do not contain a dot in the first path element.
	for _, imp := range pkg.Imports {
		first := imp
		if idx := strings.Index(imp, "/"); idx >= 0 {
			first = imp[:idx]
		}
		if strings.Contains(first, ".") {
			t.Errorf("outline imports non-stdlib package: %s", imp)
		}
	}

	// Scan source files for build constraints and cgo.
	outlineDir := filepath.Join(root, "outline")
	entries, err := os.ReadDir(outlineDir)
	if err != nil {
		t.Fatalf("reading outline dir: %v", err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		// Skip test files for the build-constraint check on filenames,
		// but still check their content for import "C".
		isTest := strings.HasSuffix(name, "_test.go")

		// Check for GOOS/GOARCH filename suffix (e.g. foo_linux.go, foo_amd64.go).
		if !isTest {
			base := strings.TrimSuffix(name, ".go")
			parts := strings.Split(base, "_")
			if len(parts) >= 2 {
				last := parts[len(parts)-1]
				secondLast := ""
				if len(parts) >= 3 {
					secondLast = parts[len(parts)-2]
				}
				goosValues := map[string]bool{
					"linux": true, "darwin": true, "windows": true,
					"freebsd": true, "openbsd": true, "netbsd": true,
					"android": true, "ios": true, "js": true, "wasip1": true,
				}
				goarchValues := map[string]bool{
					"amd64": true, "arm64": true, "arm": true,
					"386": true, "ppc64": true, "ppc64le": true,
					"mips": true, "mipsle": true, "mips64": true,
					"mips64le": true, "riscv64": true, "s390x": true,
					"wasm": true,
				}
				if goosValues[last] || goarchValues[last] ||
					goosValues[secondLast] || goarchValues[secondLast] {
					t.Errorf("outline file %s has a GOOS/GOARCH filename suffix", name)
				}
			}
		}

		// Read file content and check for build constraints and cgo.
		data, err := os.ReadFile(filepath.Join(outlineDir, name))
		if err != nil {
			t.Errorf("reading %s: %v", name, err)
			continue
		}
		content := string(data)

		// Check for //go:build or +build lines (only in non-test files).
		if !isTest {
			for _, line := range strings.Split(content, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "//go:build ") {
					t.Errorf("outline file %s has //go:build constraint: %s", name, trimmed)
				}
				if strings.HasPrefix(trimmed, "// +build ") {
					t.Errorf("outline file %s has +build constraint: %s", name, trimmed)
				}
				// Stop scanning after the package line (constraints must be before it).
				if strings.HasPrefix(trimmed, "package ") {
					break
				}
			}
		}

		// Check for import "C" (cgo).
		if strings.Contains(content, `"C"`) {
			// More precise check: look for import "C" or import ( ... "C" ... )
			for _, line := range strings.Split(content, "\n") {
				trimmed := strings.TrimSpace(line)
				if trimmed == `import "C"` || trimmed == `"C"` {
					t.Errorf("outline file %s imports C (cgo)", name)
				}
			}
		}
	}
}

// TS-01-65: go.mod gains no require and the internal/policy tests pass unmodified.
func TestGoMod_NoRequire_TS_01_65(t *testing.T) {
	root := moduleRoot(t)

	// Check go.mod has no require line.
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for _, line := range strings.Split(string(gomod), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "require") {
			t.Fatalf("go.mod has a require line: %s", trimmed)
		}
	}

	// Run internal/policy tests to confirm they pass unmodified.
	cmd := exec.Command("go", "test", "-count=1", "./internal/policy/...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("internal/policy tests failed: %v\n%s", err, string(out))
	}
}

// TS-01-66: outline builds and vets for all four cross targets with CGO_ENABLED=0.
func TestOutline_CrossTargetBuild_TS_01_66(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-target build check skipped under -short")
	}

	root := moduleRoot(t)

	// The same cross targets as internal/policy/crosstarget_test.go.
	crossTargets := []struct{ goos, goarch string }{
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"darwin", "arm64"},
		{"windows", "amd64"},
	}

	for _, target := range crossTargets {
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			for _, verb := range []string{"build", "vet"} {
				cmd := exec.Command("go", verb, "./outline/...")
				cmd.Dir = root
				cmd.Env = append(os.Environ(),
					"GOOS="+target.goos,
					"GOARCH="+target.goarch,
					"CGO_ENABLED=0",
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Errorf("go %s ./outline/... failed for %s/%s: %v\n%s",
						verb, target.goos, target.goarch, err,
						strings.TrimSpace(string(out)))
				}
			}
		})
	}
}

// TS-01-67: Tests needing real ctags skip when ctags is absent or not universal
// and the default run still passes.
//
// This test verifies:
//  1. The outline package's ctags tests use injected fake runners, not real ctags.
//  2. The tools/ctags tests that use shell-script fakes skip on Windows and
//     do not require universal-ctags on PATH.
//  3. Running the tools ctags tests passes (they use fake scripts, not real ctags).
func TestCtagsTests_SkipWhenAbsent_TS_01_67(t *testing.T) {
	root := moduleRoot(t)

	// Part 1: Verify that outline ctags tests do not call exec.LookPath("ctags")
	// or exec.Command("ctags", ...) — they use injected runners only.
	outlineDir := filepath.Join(root, "outline")
	entries, err := os.ReadDir(outlineDir)
	if err != nil {
		t.Fatalf("reading outline dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Skip this file itself — it mentions ctags strings in assertions.
		if name == "build_test.go" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(outlineDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		content := string(data)
		// Test files should not spawn a real ctags process.
		if strings.Contains(content, `LookPath("ctags")`) {
			t.Errorf("outline test %s calls LookPath(\"ctags\"); tests should use injected runners", name)
		}
		if strings.Contains(content, `Command("ctags"`) {
			t.Errorf("outline test %s calls Command(\"ctags\"); tests should use injected runners", name)
		}
	}

	// Part 2: Verify that tools/ctags_test.go tests are properly guarded.
	// All tests that create fake ctags scripts skip on Windows.
	ctagsTestFile := filepath.Join(root, "tools", "ctags_test.go")
	data, err := os.ReadFile(ctagsTestFile)
	if err != nil {
		t.Fatalf("reading ctags_test.go: %v", err)
	}
	content := string(data)

	// Every test function that creates a fake ctags script should skip on Windows.
	if !strings.Contains(content, `runtime.GOOS == "windows"`) {
		t.Error("tools/ctags_test.go does not contain Windows skip guard")
	}

	// The tests should use CtagsRunner (not exec.LookPath("ctags") directly)
	// to ensure they go through the proper skip/error path.
	if !strings.Contains(content, "CtagsRunner(") {
		t.Error("tools/ctags_test.go does not use CtagsRunner")
	}

	// Part 3: Run the tools/ctags tests specifically to confirm they pass.
	// These tests use fake shell scripts, not real ctags.
	// Use ./tools (not ./tools/...) to avoid expanding to sub-packages.
	cmd := exec.Command("go", "test", "-count=1", "-run", "CtagsRunner", "./tools")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go test -run CtagsRunner ./tools failed: %v\n%s", err, string(out))
	}
}
