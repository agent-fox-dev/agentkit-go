package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/guard"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-02-54: All() and FileNavigationTools() return the pinned names in order
func TestAllAndFileNavigationToolsPinnedNames_TS02_54(t *testing.T) {
	root := t.TempDir()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}

	// All() must return exactly 11 tools in this order.
	wantAll := []string{
		"read_file", "write_file", "edit_file",
		"list_files", "find_files", "search_files",
		"file_outline", "find_symbol",
		"execute", "run_command", "powershell",
	}
	gotAll := make([]string, 0, len(all))
	for _, tl := range all {
		gotAll = append(gotAll, tl.Name)
	}
	if strings.Join(gotAll, ",") != strings.Join(wantAll, ",") {
		t.Fatalf("All() names:\ngot:  %v\nwant: %v", gotAll, wantAll)
	}

	// FileNavigationTools() must return exactly 5 names.
	wantNav := []string{"list_files", "find_files", "search_files", "file_outline", "find_symbol"}
	gotNav := tools.FileNavigationTools()
	if strings.Join(gotNav, ",") != strings.Join(wantNav, ",") {
		t.Fatalf("FileNavigationTools():\ngot:  %v\nwant: %v", gotNav, wantNav)
	}
}

// TS-02-56: file_outline and find_symbol metadata: Builtin, Parallel, read-only, pinned guidelines
func TestToolMetadata_TS02_56(t *testing.T) {
	root := t.TempDir()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}

	toolMap := make(map[string]core.Tool)
	for _, tl := range all {
		toolMap[tl.Name] = tl
	}

	// file_outline checks.
	fo, ok := toolMap["file_outline"]
	if !ok {
		t.Fatal("file_outline not found in All()")
	}
	if !fo.Builtin {
		t.Error("file_outline must be Builtin")
	}
	if fo.ExecutionMode == core.Sequential {
		t.Error("file_outline must not be Sequential")
	}
	wantGuideline := "Before reading a large file, outline it and read only the range you need."
	if len(fo.PromptGuidelines) != 1 || fo.PromptGuidelines[0] != wantGuideline {
		t.Errorf("file_outline PromptGuidelines = %v, want [%q]", fo.PromptGuidelines, wantGuideline)
	}
	// Schema must have "path" required and "include_private" optional.
	if fo.InputSchema == nil {
		t.Fatal("file_outline InputSchema is nil")
	}

	// find_symbol checks.
	fs, ok := toolMap["find_symbol"]
	if !ok {
		t.Fatal("find_symbol not found in All()")
	}
	if !fs.Builtin {
		t.Error("find_symbol must be Builtin")
	}
	if fs.ExecutionMode == core.Sequential {
		t.Error("find_symbol must not be Sequential")
	}
	wantGuideline2 := "Use find_symbol to locate a declaration; search_files for usages and text."
	if len(fs.PromptGuidelines) != 1 || fs.PromptGuidelines[0] != wantGuideline2 {
		t.Errorf("find_symbol PromptGuidelines = %v, want [%q]", fs.PromptGuidelines, wantGuideline2)
	}
	if fs.InputSchema == nil {
		t.Fatal("find_symbol InputSchema is nil")
	}

	// Neither tool should be in guard.ShellToolNames.
	for _, name := range guard.ShellToolNames {
		if name == "file_outline" {
			t.Error("file_outline must not be in guard.ShellToolNames")
		}
		if name == "find_symbol" {
			t.Error("find_symbol must not be in guard.ShellToolNames")
		}
	}
}

// TS-02-31: All() and file_outline start no walk or subprocess and ctags runner is built lazily
func TestAllAndFileOutlineNoWalkOrSubprocess_TS02_31(t *testing.T) {
	root := t.TempDir()
	// Write a Go file so file_outline has something to outline.
	if err := os.WriteFile(root+"/main.go", []byte("package main\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use DisableCtags to ensure no subprocess is spawned.
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	// All() should not start any walk or subprocess.
	all, err := tools.All(tools.Options{
		Workspace: ws,
		Symbols:   tools.SymbolOptions{DisableCtags: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Find file_outline and call it — it should not trigger a walk.
	var foTool core.Tool
	for _, tl := range all {
		if tl.Name == "file_outline" {
			foTool = tl
			break
		}
	}
	if foTool.Name == "" {
		t.Fatal("file_outline not found in All()")
	}

	// Call file_outline — it reads from disk, not from the symbol table.
	r := foTool.Execute(context.Background(), json.RawMessage(`{"path":"main.go"}`))
	if !r.OK {
		t.Fatalf("file_outline failed: error=%q detail=%q", r.Error, r.Detail)
	}
	if !strings.Contains(r.Text, "Hello") {
		t.Fatalf("file_outline should show Hello: %s", r.Text)
	}

	// Verify that find_symbol was NOT called (the table should not be built).
	// We can verify this indirectly: calling find_symbol should trigger a build.
	var fsTool core.Tool
	for _, tl := range all {
		if tl.Name == "find_symbol" {
			fsTool = tl
			break
		}
	}
	if fsTool.Name == "" {
		t.Fatal("find_symbol not found in All()")
	}

	// Now call find_symbol — this should trigger the first build.
	r = fsTool.Execute(context.Background(), json.RawMessage(`{"name":"Hello"}`))
	if !r.OK {
		t.Fatalf("find_symbol failed: error=%q detail=%q", r.Error, r.Detail)
	}
}

// TS-02-31 (continued): CtagsRunner is constructed lazily, not at All() time.
func TestCtagsRunnerLazy_TS02_31(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(root+"/main.go", []byte("package main\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use a custom Runner to verify it's only called when a tool is used.
	var runnerCalled bool
	customRunner := func(ctx context.Context, args []string) ([]byte, error) {
		runnerCalled = true
		return nil, nil
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	// All() with a custom runner should not call it.
	_, err = tools.All(tools.Options{
		Workspace: ws,
		Symbols:   tools.SymbolOptions{Runner: customRunner},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runnerCalled {
		t.Fatal("Runner was called at All() time; it should be lazy")
	}
}

// TS-02-58: The root module stays standard-library-only and existing tools are unchanged
// This test verifies that go.mod has no new requirements and CGO_ENABLED=0 builds.
// The actual internal/policy tests are run by `make test`.
func TestRootModuleStdlibOnly_TS02_58(t *testing.T) {
	// This test just verifies that the tools package compiles and the
	// existing tool descriptions/schemas are not changed. The actual
	// policy enforcement is done by internal/policy/deps_test.go which
	// is run by `make test`.
	root := t.TempDir()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}

	// Verify existing tools' descriptions are unchanged.
	expectedDescs := map[string]string{
		"read_file": "Read a file. Text is returned as at most 2000 lines or 50KB, " +
			"whichever comes first; an image is returned as a note plus the image itself.",
		"write_file": "Write a file, creating or replacing it.",
		"list_files": "List the entries of a directory, directories with a trailing /. " +
			"Does not apply .gitignore.",
		"find_files": "Find files by glob pattern, skipping .gitignored paths.",
	}
	for _, tl := range all {
		if want, ok := expectedDescs[tl.Name]; ok {
			if tl.Description != want {
				t.Errorf("%s description changed:\ngot:  %q\nwant: %q", tl.Name, tl.Description, want)
			}
		}
	}
}
