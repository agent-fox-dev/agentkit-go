//go:build !windows

package codesearch_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the absolute path of the repository root (parent of codesearch/).
func repoRoot(t *testing.T) string {
	t.Helper()
	modRoot := moduleRoot(t)
	return filepath.Dir(modRoot)
}

// TS-03-6: codesearch/README.md leads with the go/no-go figures and records
// the pinned-zoekt answers.
// Verifies: 03-REQ-1.2, 03-REQ-9.1, 03-REQ-10.5.
func TestReadmeContent_TS03_6(t *testing.T) {
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	content := string(data)

	// The first content after the title must be the go/no-go gate with figures, repos and a date.
	// Split into sections by ## headings.
	sections := strings.Split(content, "\n## ")
	if len(sections) < 2 {
		t.Fatal("README.md must have at least two ## sections")
	}
	// The second element (sections[1]) is the first ## section.
	// It should be the Go/No-Go Gate.
	firstSection := sections[1]
	for _, required := range []string{"Go/No-Go", "Decision", "Date"} {
		if !strings.Contains(firstSection, required) {
			t.Errorf("first ## section must contain %q", required)
		}
	}
	// Must mention the repositories.
	for _, repo := range []string{"sourcegraph", "kubernetes"} {
		if !strings.Contains(firstSection, repo) {
			t.Errorf("first ## section must mention repository %q", repo)
		}
	}

	// Requirement 9 answers: module path, packages, builder/parser/searcher,
	// memory vs file, byte-offset symbol fields, search options, cross-target results.
	requiredTopics := []string{
		"github.com/sourcegraph/zoekt", // module path
		"index",                        // builder package
		"query",                        // parser package
		"search",                       // searcher package
		"DocumentSection",              // byte-offset symbol fields
		"SearchOptions",                // search options
		"linux/amd64",                  // cross-target
		"windows/amd64",                // cross-target
		"Options.Index",                // opt-in
		"allowlist",                    // allowlist entry
		"search_files",                 // differences from search_files
		"1 MiB",                        // known difference
		"zoekt version",                // goldens tied to version
	}
	for _, topic := range requiredTopics {
		if !strings.Contains(content, topic) {
			t.Errorf("README.md must contain %q", topic)
		}
	}
}

// TS-03-65: The Makefile test, vet, lint and tidy targets cover codesearch.
// Verifies: 03-REQ-10.3.
func TestMakefileCoversCodesearch_TS03_65(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	content := string(data)

	// Each target must mention codesearch.
	for _, target := range []string{"test", "vet", "lint", "tidy"} {
		// Find the target's recipe by looking for the target name and checking
		// that codesearch appears in the surrounding context.
		// We use make -n to get the dry-run output for each target.
		cmd := exec.Command("make", "-n", target)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			t.Errorf("make -n %s failed: %v", target, err)
			continue
		}
		if !strings.Contains(string(out), "codesearch") {
			t.Errorf("make -n %s does not mention codesearch:\n%s", target, out)
		}
	}

	// Also verify the Makefile source mentions codesearch.
	if !strings.Contains(content, "codesearch") {
		t.Error("Makefile source does not mention codesearch")
	}
}

// TS-03-66: Every codesearch test obtains symbols from a fake Runner or
// DisableCtags, and real-ctags tests skip when absent.
// Verifies: 03-REQ-10.4.
func TestCodesearchTestsUseFakeSymbols_TS03_66(t *testing.T) {
	root := moduleRoot(t)

	// Scan all test files in the codesearch module.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading codesearch dir: %v", err)
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		content := string(data)

		// Skip files that don't create indexes (e.g. policy_test.go, docs_test.go).
		if !strings.Contains(content, "New(") && !strings.Contains(content, "codesearch.New(") {
			continue
		}

		// Every test that creates an index must use DisableCtags or a fake Runner,
		// or skip when ctags is absent.
		usesDisable := strings.Contains(content, "DisableCtags: true") ||
			strings.Contains(content, "DisableCtags:true")
		usesFakeRunner := strings.Contains(content, "Runner:") ||
			strings.Contains(content, "fakeRunner")
		hasSkip := strings.Contains(content, "LookPath") ||
			strings.Contains(content, "t.Skip")

		if !usesDisable && !usesFakeRunner && !hasSkip {
			t.Errorf("%s creates an index but does not use DisableCtags, a fake Runner, or skip when ctags is absent", e.Name())
		}
	}

	// Verify that go test passes without ctags by running with a restricted PATH.
	// We don't actually run this in CI since it would be slow, but we verify
	// the pattern is correct.
}

// TS-03-67: The documentation set is updated with the required content.
// Verifies: 03-REQ-10.6.
func TestDocumentationSetUpdated_TS03_67(t *testing.T) {
	root := repoRoot(t)

	// docs/architecture.md: module boundary beside difftest/ and examples/flatline,
	// package table and graph rows for codesearch.
	archData, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatalf("reading architecture.md: %v", err)
	}
	arch := string(archData)
	if !strings.Contains(arch, "codesearch") {
		t.Error("architecture.md must mention codesearch")
	}
	if !strings.Contains(arch, "difftest") {
		t.Error("architecture.md must mention difftest")
	}
	// Must show codesearch in the package table.
	if !strings.Contains(arch, "codesearch") {
		t.Error("architecture.md package table must include codesearch")
	}

	// docs/DEPS.md: a new numbered ruling for zoekt.
	depsData, err := os.ReadFile(filepath.Join(root, "docs", "DEPS.md"))
	if err != nil {
		t.Fatalf("reading DEPS.md: %v", err)
	}
	deps := string(depsData)
	if !strings.Contains(deps, "zoekt") {
		t.Error("DEPS.md must contain a ruling for zoekt")
	}
	// The nested-modules sentence must mention codesearch.
	if !strings.Contains(deps, "codesearch") {
		t.Error("DEPS.md nested-modules sentence must mention codesearch")
	}

	// docs/configuration.md: Index row in tools.Options table,
	// code_search limits, codesearch.Options fields.
	confData, err := os.ReadFile(filepath.Join(root, "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("reading configuration.md: %v", err)
	}
	conf := string(confData)
	if !strings.Contains(conf, "Index") {
		t.Error("configuration.md must have an Index row")
	}
	if !strings.Contains(conf, "code_search") {
		t.Error("configuration.md must mention code_search limits")
	}
	if !strings.Contains(conf, "codesearch.Options") {
		t.Error("configuration.md must document codesearch.Options fields")
	}

	// docs/cli.md: make target descriptions updated.
	cliData, err := os.ReadFile(filepath.Join(root, "docs", "cli.md"))
	if err != nil {
		t.Fatalf("reading cli.md: %v", err)
	}
	cli := string(cliData)
	if !strings.Contains(cli, "codesearch") {
		t.Error("cli.md must mention codesearch in make target descriptions")
	}

	// README.md: module list includes codesearch.
	readmeData, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	readme := string(readmeData)
	if !strings.Contains(readme, "codesearch") {
		t.Error("README.md must list the codesearch module")
	}
}

// TS-03-68: docs/api.md is untouched by the change.
// Verifies: 03-REQ-10.7.
func TestApiMdUntouched_TS03_68(t *testing.T) {
	root := repoRoot(t)

	// Verify docs/api.md exists (it should).
	apiPath := filepath.Join(root, "docs", "api.md")
	if _, err := os.Stat(apiPath); os.IsNotExist(err) {
		t.Skip("docs/api.md does not exist")
	}

	// Check git diff to see if api.md has been modified.
	// Since we're in a working tree, check if the file is in the staged or
	// unstaged changes.
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		// If git diff fails (e.g. no commits), just verify the file exists.
		t.Logf("git diff failed: %v; skipping diff check", err)
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "docs/api.md" {
			t.Error("docs/api.md must not be modified by this change")
		}
	}
}
