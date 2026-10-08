package outline_test

import (
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
		// Skip smoke_test.go — smoke tests are explicitly allowed to use real
		// ctags (they skip when ctags is absent, per the spec).
		if name == "build_test.go" || name == "smoke_test.go" {
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
