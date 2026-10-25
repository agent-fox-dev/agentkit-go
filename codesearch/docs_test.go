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

// TS-03-6: the module's design record holds the go/no-go figures and the
// pinned-zoekt answers. They lived in codesearch/README.md until the README was
// rewritten for users; they are now in docs/codesearch.md.
// Verifies: 03-REQ-1.2, 03-REQ-9.1, 03-REQ-10.5.
func TestReadmeContent_TS03_6(t *testing.T) {
	path := filepath.Join(repoRoot(t), "docs", "codesearch.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading docs/codesearch.md: %v", err)
	}
	content := string(data)

	// The go/no-go gate is a section of its own, with the figures, the
	// repositories and a date.
	var gate string
	for _, section := range strings.Split(content, "\n## ")[1:] {
		if strings.Contains(strings.SplitN(section, "\n", 2)[0], "Go/No-Go") {
			gate = section
			break
		}
	}
	if gate == "" {
		t.Fatal("docs/codesearch.md must have a ## section whose heading names the Go/No-Go gate")
	}
	for _, required := range []string{"Decision", "Date"} {
		if !strings.Contains(gate, required) {
			t.Errorf("the Go/No-Go section must contain %q", required)
		}
	}
	// Must mention the repositories.
	for _, repo := range []string{"sourcegraph", "kubernetes"} {
		if !strings.Contains(gate, repo) {
			t.Errorf("the Go/No-Go section must mention repository %q", repo)
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
			t.Errorf("docs/codesearch.md must contain %q", topic)
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
