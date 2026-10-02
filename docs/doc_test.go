package docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the repository root by walking up from this file's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	// This file is in docs/, so the repo root is one level up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting working directory: %v", err)
	}
	return filepath.Dir(wd)
}

// TS-01-69: architecture.md lists outline in the package graph and packages
// table and notes Walk and CtagsRunner.
func TestArchitectureMD_OutlineAndWalkAndCtagsRunner_TS_01_69(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatalf("reading architecture.md: %v", err)
	}
	content := string(data)

	// The package graph must contain an outline row with "nothing first-party".
	if !strings.Contains(content, "outline") {
		t.Error("architecture.md does not mention 'outline'")
	}

	// Check the package graph table has outline with "nothing first-party".
	graphSection := extractSection(content, "## Package graph", "##")
	if graphSection == "" {
		t.Fatal("architecture.md has no '## Package graph' section")
	}
	if !strings.Contains(graphSection, "outline") {
		t.Error("package graph section does not contain 'outline'")
	}
	if !strings.Contains(graphSection, "nothing first-party") {
		t.Error("package graph section does not mention 'nothing first-party' for outline")
	}

	// Check the packages table has an outline row.
	packagesSection := extractSection(content, "## Packages", "##")
	if packagesSection == "" {
		t.Fatal("architecture.md has no '## Packages' section")
	}
	if !strings.Contains(packagesSection, "outline") {
		t.Error("packages table does not contain 'outline'")
	}

	// Check that tools section mentions tools.Walk and tools.CtagsRunner.
	// The tools row is in the packages table or the package graph.
	if !strings.Contains(content, "tools.Walk") && !strings.Contains(content, "`Walk`") {
		// Check for Walk mentioned in the tools context
		if !strings.Contains(content, "Walk") {
			t.Error("architecture.md does not mention Walk")
		}
	}
	if !strings.Contains(content, "tools.CtagsRunner") && !strings.Contains(content, "`CtagsRunner`") {
		if !strings.Contains(content, "CtagsRunner") {
			t.Error("architecture.md does not mention CtagsRunner")
		}
	}
}

// TS-01-70: README.md and GAPS.md record the outline entry, unbuilt
// references/callers and the walk consolidation.
func TestREADME_And_GAPS_TS_01_70(t *testing.T) {
	root := repoRoot(t)

	// Check README.md
	readmeData, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	readme := string(readmeData)

	// README's packages table should have an outline entry.
	if !strings.Contains(readme, "outline") {
		t.Error("README.md does not mention 'outline'")
	}

	// Check the packages table specifically has outline.
	packagesTable := extractTableAfter(readme, "| Package | What it owns |")
	if packagesTable == "" {
		t.Fatal("README.md has no packages table")
	}
	if !strings.Contains(packagesTable, "outline") {
		t.Error("README.md packages table does not contain 'outline'")
	}

	// The 'What is not built' list should still mention references and callers.
	notBuiltSection := extractSection(readme, "## What is not built", "##")
	if notBuiltSection == "" {
		t.Fatal("README.md has no 'What is not built' section")
	}
	if !strings.Contains(strings.ToLower(notBuiltSection), "reference") {
		t.Error("'What is not built' section does not mention references")
	}
	if !strings.Contains(strings.ToLower(notBuiltSection), "caller") {
		t.Error("'What is not built' section does not mention callers")
	}

	// Check docs/GAPS.md
	gapsData, err := os.ReadFile(filepath.Join(root, "docs", "GAPS.md"))
	if err != nil {
		t.Fatalf("reading GAPS.md: %v", err)
	}
	gaps := string(gapsData)

	// GAPS.md should record the walk consolidation.
	if !strings.Contains(strings.ToLower(gaps), "walk") {
		t.Error("GAPS.md does not mention 'walk'")
	}
	// Should mention the consolidation or outline.
	if !strings.Contains(gaps, "outline") && !strings.Contains(gaps, "Walk") {
		t.Error("GAPS.md does not mention 'outline' or 'Walk'")
	}
}

// TS-01-71: docs/api.md is unchanged and make check passes.
// The make check part is verified by the CI gate; here we verify api.md content.
func TestAPImd_Unchanged_TS_01_71(t *testing.T) {
	root := repoRoot(t)

	data, err := os.ReadFile(filepath.Join(root, "docs", "api.md"))
	if err != nil {
		t.Fatalf("reading api.md: %v", err)
	}
	content := string(data)

	// api.md should still be the MCP network API document.
	if !strings.Contains(content, "# Network API") {
		t.Error("api.md does not start with '# Network API'")
	}
	if !strings.Contains(content, "MCP server") {
		t.Error("api.md does not mention 'MCP server'")
	}

	// api.md should NOT mention outline (it's the network API doc, not the outline doc).
	if strings.Contains(content, "outline") {
		t.Error("api.md should not mention 'outline' — it is the network API document")
	}
}

// extractSection extracts text from a markdown section header to the next
// section of the same or higher level. If endMarker is "##", it stops at
// the next "## " line.
func extractSection(content, header, endMarker string) string {
	idx := strings.Index(content, header)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(header):]
	// Find the next section header at the same level.
	endIdx := strings.Index(rest, "\n"+endMarker+" ")
	if endIdx < 0 {
		return rest
	}
	return rest[:endIdx]
}

// extractTableAfter extracts a markdown table starting from the given header row.
func extractTableAfter(content, headerRow string) string {
	idx := strings.Index(content, headerRow)
	if idx < 0 {
		return ""
	}
	rest := content[idx:]
	// Find the end of the table (first non-table line after the header).
	lines := strings.Split(rest, "\n")
	var table []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if len(table) > 0 && trimmed != "" && !strings.HasPrefix(trimmed, "|") {
			break
		}
		table = append(table, line)
	}
	return strings.Join(table, "\n")
}
