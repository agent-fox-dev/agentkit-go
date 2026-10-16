package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-22-35 (smoke, 22-PATH-1): an embedder builds a code-mode agent from named
// workspace tools and a scripted model. A real workspace and tools.For feed
// core.Named, which returns list_files, find_files and read_file in that
// order; the example then runs its faux.ToolCall and faux.Text script through
// codemode and the agent against its own workspace, with its MCP inventory
// tool, and prints what the scripts did. Nothing in the path is replaced.
func TestSmokeCodemodeNamedWorkspaceTools_TS22_35(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir, workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	picked, err := core.Named(all, "list_files", "find_files", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range picked {
		got = append(got, p.Name)
	}
	if want := []string{"list_files", "find_files", "read_file"}; !slices.Equal(got, want) {
		t.Fatalf("core.Named over tools.For returned %v, want %v", got, want)
	}

	var b strings.Builder
	if err := run(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"code_mode binds 4 tools",
		"Go files: main.go, util.go",
		"CHANGELOG.md: read_failed",
		`Return value: {"apples": 12, "pears": 0}`,
		"called list_files", "called find_files", "called read_file", "called inventory__stock",
		"model: The workspace has two Go files, no CHANGELOG.md, and 12 apples and 0 pears in stock.",
		"Code mode example completed successfully",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
}
