package tools_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot returns the repository root by walking up from the test file's
// directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine caller file")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repository root (go.mod)")
		}
		dir = parent
	}
}

// TS-02-59: README.md and docs/architecture.md describe the new tools and the symbol table
func TestDocsReadmeAndArchitecture_TS02_59(t *testing.T) {
	root := repoRoot(t)

	// --- README.md ---
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readmeStr := string(readme)

	// README.md must list file_outline and find_symbol in the tool list (the package table).
	if !strings.Contains(readmeStr, "file_outline") {
		t.Error("README.md does not mention file_outline")
	}
	if !strings.Contains(readmeStr, "find_symbol") {
		t.Error("README.md does not mention find_symbol")
	}

	// README.md's "What is not built" section. Spec 05_find_references built
	// references and callers, so they are no longer listed there.
	idx := strings.Index(readmeStr, "What is not built")
	if idx < 0 {
		t.Fatal("README.md has no 'What is not built' section")
	}
	notBuilt := readmeStr[idx:]
	// The old bullet should no longer say file_outline and find_symbol are not built.
	if strings.Contains(notBuilt, "file_outline and find_symbol tools") &&
		strings.Contains(notBuilt, "are not built yet") {
		t.Error("README.md 'What is not built' still says file_outline and find_symbol are not built")
	}

	// --- docs/architecture.md ---
	arch, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	archStr := string(arch)

	// architecture.md must describe the symbol table and its place under tools.
	if !strings.Contains(archStr, "symbol table") {
		t.Error("docs/architecture.md does not mention the symbol table")
	}
	// It should mention the tools package in connection with the symbol table.
	if !strings.Contains(archStr, "file_outline") {
		t.Error("docs/architecture.md does not mention file_outline")
	}
	if !strings.Contains(archStr, "find_symbol") {
		t.Error("docs/architecture.md does not mention find_symbol")
	}
}

// TS-02-60: configuration.md and GAPS.md are updated and api.md is untouched
func TestDocsConfigurationAndGaps_TS02_60(t *testing.T) {
	root := repoRoot(t)

	// --- docs/configuration.md ---
	config, err := os.ReadFile(filepath.Join(root, "docs", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	configStr := string(config)

	// Must have a Symbols row in the tools.Options table.
	if !strings.Contains(configStr, "Symbols") {
		t.Error("docs/configuration.md does not have a Symbols row")
	}
	// Must have both tool names in the tools.All sentence.
	if !strings.Contains(configStr, "file_outline") {
		t.Error("docs/configuration.md does not mention file_outline")
	}
	if !strings.Contains(configStr, "find_symbol") {
		t.Error("docs/configuration.md does not mention find_symbol")
	}
	// Must state the limits: 20 default, 50 cap, 50 000 files, 2 s.
	if !strings.Contains(configStr, "20") {
		t.Error("docs/configuration.md does not state the default 20 results")
	}
	if !strings.Contains(configStr, "50") {
		t.Error("docs/configuration.md does not state the cap of 50 results")
	}
	if !strings.Contains(configStr, "50 000") && !strings.Contains(configStr, "50,000") && !strings.Contains(configStr, "50000") {
		t.Error("docs/configuration.md does not state the 50 000 files limit")
	}
	if !strings.Contains(configStr, "2 s") && !strings.Contains(configStr, "2s") && !strings.Contains(configStr, "two seconds") {
		t.Error("docs/configuration.md does not state the 2 s time limit")
	}

	// --- docs/GAPS.md ---
	gaps, err := os.ReadFile(filepath.Join(root, "docs", "GAPS.md"))
	if err != nil {
		t.Fatal(err)
	}
	gapsStr := string(gaps)

	// Must record the references and callers row. Spec 05_find_references
	// moved it from Deferred (a non-goal of spec 02) to Fixed.
	if !strings.Contains(gapsStr, "references") || !strings.Contains(gapsStr, "callers") {
		t.Error("docs/GAPS.md does not record references and callers row")
	}
	if !strings.Contains(gapsStr, "05_find_references") {
		t.Error("docs/GAPS.md does not cite spec 05_find_references for references and callers")
	}

	// --- docs/api.md must NOT be modified ---
	// We verify it exists and has not been touched by checking it still starts
	// with the expected header.
	api, err := os.ReadFile(filepath.Join(root, "docs", "api.md"))
	if err != nil {
		t.Fatal(err)
	}
	apiStr := string(api)
	if !strings.HasPrefix(apiStr, "# Network API") {
		t.Error("docs/api.md has been modified (header changed)")
	}
	// api.md should not mention file_outline or find_symbol.
	if strings.Contains(apiStr, "file_outline") || strings.Contains(apiStr, "find_symbol") {
		t.Error("docs/api.md should not mention file_outline or find_symbol")
	}
}
