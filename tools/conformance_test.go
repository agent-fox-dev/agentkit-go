package tools_test

// Spec 06 conformance: every built-in tool returns its documented error codes,
// and its Data conforms to its declared OutputSchema.
//
// code_search lives in its own module and is covered in codesearch/tool_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/internal/testkit"
	"github.com/agentfox/agentkit-go/stop"
	"github.com/agentfox/agentkit-go/subagent"
	"github.com/agentfox/agentkit-go/tools"
)

// conformanceWorkspace is a small tree every conformance test runs against:
// a Go file, a 10-byte text file and a directory.
func conformanceWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("f.txt", "0123456789")
	write("main.go", "package main\n\n// Greet says hello.\nfunc Greet() string { return \"hi\" }\n\nfunc main() { _ = Greet() }\n")
	write("dir/inner.txt", "inner\n")
	return root
}

// builtins is every built-in tool in the root module, by name: the default
// set from tools.All, plus fetch_url and a subagent delegation tool, which
// an embedder adds explicitly.
func builtins(t *testing.T, root string, child func(context.Context) (*agentkit.Agent, error)) map[string]core.Tool {
	t.Helper()
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]core.Tool{}
	for _, tl := range all {
		out[tl.Name] = tl
	}
	out["fetch_url"] = tools.FetchTool(tools.FetchOptions{})
	if child == nil {
		child = func(context.Context) (*agentkit.Agent, error) { return newAgent(t, nil), nil }
	}
	out["subagent"] = subagent.Tool(newAgent(t, nil), child, subagent.Options{Name: "subagent"})
	return out
}

// newAgent builds an agent on the scripted provider, which answers "done".
func newAgent(t *testing.T, ts []core.Tool) *agentkit.Agent {
	t.Helper()
	a, err := agentkit.NewAgent(core.AgentConfig{
		Model:      testkit.TestModel(),
		StopPolicy: stop.AfterTurns(3),
		Providers:  core.ProviderRegistry{testkit.TestAPI: (&testkit.Scripted{}).Provider()},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range ts {
		if err := a.RegisterTool(tl); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// expectError runs tl and asserts the contract: OK false and exactly code.
func expectError(t *testing.T, ctx context.Context, tl core.Tool, args, code string) core.ToolResult {
	t.Helper()
	if tl.Execute == nil {
		t.Fatalf("%s has no Execute", tl.Name)
	}
	res := tl.Execute(ctx, json.RawMessage(args))
	if res.OK || res.Error != code {
		t.Errorf("%s %s: OK %v error %q (%s), want error %q", tl.Name, args, res.OK, res.Error, res.Detail, code)
	}
	return res
}

// TS-06-23: malformed JSON is invalid_arguments for every built-in tool.
// 06-REQ-9.2.
func TestErrorCodesInvalidArguments_TS06_23(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t), nil)
	for _, name := range []string{"read_file", "write_file", "edit_file", "list_files", "find_files",
		"search_files", "file_outline", "find_symbol", "find_references", "fetch_url",
		"execute", "run_command", "powershell", "subagent"} {
		tl, ok := all[name]
		if !ok {
			t.Errorf("%s is not a built-in tool", name)
			continue
		}
		expectError(t, context.Background(), tl, `{malformed`, "invalid_arguments")
	}
	// An invalid value, not only invalid JSON.
	expectError(t, context.Background(), all["find_files"], `{"pattern":"*","file_type":"socket"}`, "invalid_arguments")
	expectError(t, context.Background(), all["search_files"], `{"pattern":""}`, "invalid_arguments")
	expectError(t, context.Background(), all["subagent"], `{"prompt":""}`, "invalid_arguments")
}

// TS-06-24: a path outside the workspace is path_not_allowed. 06-REQ-9.3.
func TestErrorCodesPathNotAllowed_TS06_24(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t), nil)
	for name, args := range map[string]string{
		"read_file":    `{"path":"../outside/secret.txt"}`,
		"write_file":   `{"path":"../outside/secret.txt","content":"x"}`,
		"edit_file":    `{"path":"../outside/secret.txt","edits":[{"old_string":"a","new_string":"b"}]}`,
		"list_files":   `{"path":"../outside"}`,
		"find_files":   `{"pattern":"*","path":"../outside"}`,
		"search_files": `{"pattern":"x","path":"../outside"}`,
		"file_outline": `{"path":"../outside/secret.go"}`,
		"find_symbol":  `{"name":"Greet","path":"../outside"}`,
	} {
		expectError(t, context.Background(), all[name], args, "path_not_allowed")
	}
	expectError(t, context.Background(), all["read_file"], `{"path":"/etc/hosts"}`, "path_not_allowed")
}

// TS-06-25: a directory is not_a_file for the tools that read one file.
// 06-REQ-9.4.
func TestErrorCodesNotAFile_TS06_25(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t), nil)
	for name, args := range map[string]string{
		"read_file":    `{"path":"dir"}`,
		"edit_file":    `{"path":"dir","edits":[{"old_string":"a","new_string":"b"}]}`,
		"file_outline": `{"path":"dir"}`,
	} {
		expectError(t, context.Background(), all[name], args, "not_a_file")
	}
}

// TS-06-26: a cancelled context is aborted for the tools that walk or index
// the workspace. 06-REQ-9.5.
func TestErrorCodesAborted_TS06_26(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, args := range map[string]string{
		"find_files":      `{"pattern":"**/*"}`,
		"search_files":    `{"pattern":"Greet"}`,
		"file_outline":    `{"path":"main.go"}`,
		"find_symbol":     `{"name":"Greet"}`,
		"find_references": `{"name":"Greet"}`,
	} {
		expectError(t, ctx, all[name], args, "aborted")
	}
}

// TS-06-28: reading past the end of a file, and fetching a forbidden scheme
// or address. 06-REQ-9.7.
func TestErrorCodesOffsetSchemeAddress_TS06_28(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t), nil)
	expectError(t, context.Background(), all["read_file"], `{"path":"f.txt","offset":999}`, "offset_past_end")
	expectError(t, context.Background(), all["fetch_url"], `{"url":"ftp://example.com"}`, "scheme_not_allowed")
	expectError(t, context.Background(), all["fetch_url"], `{"url":"http://example.com"}`, "scheme_not_allowed")
	expectError(t, context.Background(), all["fetch_url"], `{"url":"https://127.0.0.1/"}`, "address_not_allowed")
	expectError(t, context.Background(), all["fetch_url"], `{"url":"https://169.254.169.254/latest/"}`, "address_not_allowed")
}

// TS-06-29: the I/O and delegation failures each tool documents. 06-REQ-9.8.
//
// edit_failed is not induced: ApplyEdits returns only *EditError, which
// edit_file reports as edit_<phase>; see docs/errata/06_tool_output_schemas.md.
func TestErrorCodesFailures_TS06_29(t *testing.T) {
	root := conformanceWorkspace(t)
	shellChild := func(context.Context) (*agentkit.Agent, error) {
		// A child holding a shell tool with no BeforeToolCall cannot run:
		// Run returns ErrUnguardedExecute, a failed child.
		return newAgent(t, []core.Tool{{Name: "execute", Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.OKResult(nil)
		}}}), nil
	}
	all := builtins(t, root, shellChild)
	bg := context.Background()

	expectError(t, bg, all["read_file"], `{"path":"missing.txt"}`, "read_failed")
	expectError(t, bg, all["edit_file"], `{"path":"missing.txt","edits":[{"old_string":"a","new_string":"b"}]}`, "read_failed")
	expectError(t, bg, all["file_outline"], `{"path":"missing.go"}`, "read_failed")
	// dir is a directory: the atomic rename cannot replace it with a file.
	expectError(t, bg, all["write_file"], `{"path":"dir","content":"x"}`, "write_failed")
	expectError(t, bg, all["list_files"], `{"path":"missing_dir"}`, "list_failed")
	// The spec's edit_precondition is spelled edit_<phase>: a missing
	// old_string is edit_not_found.
	expectError(t, bg, all["edit_file"], `{"path":"f.txt","edits":[{"old_string":"not_there","new_string":"x"}]}`, "edit_not_found")
	expectError(t, bg, all["subagent"], `{"prompt":"fail"}`, "subagent_failed")

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Log("outline_failed needs an unreadable file; skipped on Windows and as root")
		return
	}
	locked := filepath.Join(root, "locked.go")
	if err := os.WriteFile(locked, []byte("package x\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
	// The stat succeeds and the read does not: outline_failed.
	expectError(t, bg, all["file_outline"], `{"path":"locked.go"}`, "outline_failed")
}
