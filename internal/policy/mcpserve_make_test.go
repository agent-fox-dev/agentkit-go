package policy

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TS-23-49: the Makefile builds, tests, vets, lints and tidies mcpserve through
// MODULES, documents it in the make doc loop, and its generated documentation
// is committed. 23-REQ-9.1.
func TestMakefileListsMcpserve_TS23_49(t *testing.T) {
	root := repoRoot(t)
	mf, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	var modules, docLoop bool
	for _, line := range strings.Split(string(mf), "\n") {
		words := strings.Fields(strings.ReplaceAll(line, ";", " "))
		if strings.HasPrefix(line, "MODULES :=") && slices.Contains(words, "mcpserve") {
			modules = true
		}
		if strings.Contains(line, "@for m in") && slices.Contains(words, "mcpserve") {
			docLoop = true
		}
	}
	if !modules || !docLoop {
		t.Errorf("MODULES lists mcpserve: %v; the doc loop does: %v", modules, docLoop)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "generated", "mcpserve", "mcpserve.txt")); err != nil {
		t.Errorf("the generated documentation is missing: %v", err)
	}
}
