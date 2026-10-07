package docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-04-56: docs/configuration.md documents the subprocess runner and the
// AfterToolCall ToolResult and Metadata.
func TestConfigurationMD_SubprocessRunnerAndAfterToolCall_TS_04_56(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "configuration.md"))
	if err != nil {
		t.Fatalf("reading configuration.md: %v", err)
	}
	content := string(data)

	// It has a heading whose text is Subprocess runner (`tools.Run`, `tools.RunArgv`).
	const heading = "Subprocess runner (`tools.Run`, `tools.RunArgv`)"
	if !strings.Contains(content, heading) {
		t.Fatalf("configuration.md does not contain heading %q", heading)
	}

	// Extract the section.
	sec := extractSection(content, "## "+heading, "##")
	if sec == "" {
		t.Fatal("could not extract the subprocess runner section")
	}

	// That section's ExecOptions table has rows for all required fields.
	for _, field := range []string{
		"Dir", "Timeout", "MaxBytes", "KeepHead", "SpillDir",
		"LogPath", "Stdin", "Env", "DrainIdle", "DrainCeiling",
	} {
		if !strings.Contains(sec, field) {
			t.Errorf("subprocess runner section missing ExecOptions field %q", field)
		}
	}

	// That section names the ExecResult fields.
	for _, field := range []string{
		"Output", "Outcome", "ExitCode", "Truncated", "TotalBytes",
		"SpillPath", "Duration", "IOErr",
	} {
		if !strings.Contains(sec, field) {
			t.Errorf("subprocess runner section missing ExecResult field %q", field)
		}
	}

	// The outcome words.
	for _, word := range []string{"ok", "exit", "signal", "timeout", "abort"} {
		if !strings.Contains(sec, word) {
			t.Errorf("subprocess runner section missing outcome word %q", word)
		}
	}

	// The table row naming BeforeToolCall and AfterToolCall mentions ToolResult
	// and ToolResultMessage.Metadata.
	lines := strings.Split(content, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "AfterToolCall") && strings.Contains(line, "|") {
			found = true
			if !strings.Contains(line, "ToolResult") {
				t.Error("AfterToolCall row does not mention ToolResult")
			}
			if !strings.Contains(line, "ToolResultMessage.Metadata") {
				t.Error("AfterToolCall row does not mention ToolResultMessage.Metadata")
			}
			break
		}
	}
	if !found {
		t.Error("no table row containing AfterToolCall found in configuration.md")
	}
}

// TS-04-57: architecture.md mentions metadata at finalize and RunArgv in the
// tools row, and a 04_ erratum records the departures.
func TestArchitectureMD_MetadataAndErratum_TS_04_57(t *testing.T) {
	root := repoRoot(t)

	// --- architecture.md ---
	archData, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatalf("reading architecture.md: %v", err)
	}
	arch := string(archData)

	// The data-flow finalize step mentions metadata.
	dataFlow := extractSection(arch, "## Data flow of one run", "##")
	if dataFlow == "" {
		t.Fatal("architecture.md has no 'Data flow of one run' section")
	}
	// Find the finalize line(s).
	finalizeMentionsMetadata := false
	for _, line := range strings.Split(dataFlow, "\n") {
		if strings.Contains(strings.ToLower(line), "finalize") && strings.Contains(strings.ToLower(line), "metadata") {
			finalizeMentionsMetadata = true
			break
		}
	}
	if !finalizeMentionsMetadata {
		t.Error("the data-flow finalize step does not mention metadata")
	}

	// The tools package row names RunArgv.
	packagesSection := extractSection(arch, "## Packages", "##")
	if packagesSection == "" {
		t.Fatal("architecture.md has no 'Packages' section")
	}
	// Find the tools row.
	toolsRowHasRunArgv := false
	for _, line := range strings.Split(packagesSection, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "|") && strings.Contains(trimmed, "`tools`") && !strings.Contains(trimmed, "`codesearch`") {
			if strings.Contains(trimmed, "RunArgv") {
				toolsRowHasRunArgv = true
			}
			break
		}
	}
	if !toolsRowHasRunArgv {
		t.Error("the tools package row in architecture.md does not name RunArgv")
	}

	// --- erratum ---
	entries, err := os.ReadDir(filepath.Join(root, "docs", "errata"))
	if err != nil {
		t.Fatalf("reading docs/errata: %v", err)
	}
	var erratumContent strings.Builder
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "04_") && strings.HasSuffix(e.Name(), ".md") {
			count++
			data, err := os.ReadFile(filepath.Join(root, "docs", "errata", e.Name()))
			if err != nil {
				t.Fatalf("reading erratum %s: %v", e.Name(), err)
			}
			erratumContent.Write(data)
		}
	}
	if count == 0 {
		t.Fatal("no docs/errata/04_*.md files found")
	}

	errText := erratumContent.String()
	for _, s := range []string{
		"RunArgv", "Exec", "Outcome", "TimedOut", "Aborted",
		"ExitCode -1", "KeepHead", "KeepTail", "WaitDelay",
	} {
		if !strings.Contains(errText, s) {
			t.Errorf("erratum does not mention %q", s)
		}
	}

	// The erratum states that §7 and §5's usage and abort items were already
	// implemented.
	if !strings.Contains(errText, "§7") && !strings.Contains(errText, "§7") {
		t.Error("erratum does not mention §7")
	}
	if !strings.Contains(errText, "§5") && !strings.Contains(errText, "§5") {
		t.Error("erratum does not mention §5")
	}
	if !strings.Contains(strings.ToLower(errText), "already implemented") &&
		!strings.Contains(strings.ToLower(errText), "already shipped") {
		t.Error("erratum does not state that §7 and §5 items were already implemented")
	}
}
