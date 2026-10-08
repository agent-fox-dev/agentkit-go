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
