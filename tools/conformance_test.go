package tools_test

// Spec 06 conformance: every built-in tool returns its documented error codes,
// and its Data conforms to its declared OutputSchema.
//
// code_search lives in its own module and is covered in codesearch/tool_test.go.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/jsonx"
	"github.com/agentfox/agentkit-go/schema"
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
// set from tools.All.
func builtins(t *testing.T, root string) map[string]core.Tool {
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
	return out
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
	all := builtins(t, conformanceWorkspace(t))
	for _, name := range []string{"read_file", "write_file", "edit_file", "list_files", "find_files",
		"search_files", "file_outline", "find_symbol", "find_references",
		"execute", "run_command"} {
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
}

// TS-06-24: a path outside the workspace is path_not_allowed. 06-REQ-9.3.
func TestErrorCodesPathNotAllowed_TS06_24(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t))
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
	all := builtins(t, conformanceWorkspace(t))
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
	all := builtins(t, conformanceWorkspace(t))
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

// TS-06-28: reading past the end of a file. 06-REQ-9.7.
func TestErrorCodesOffsetSchemeAddress_TS06_28(t *testing.T) {
	all := builtins(t, conformanceWorkspace(t))
	expectError(t, context.Background(), all["read_file"], `{"path":"f.txt","offset":999}`, "offset_past_end")
}

// TS-06-29: the I/O failures each tool documents. 06-REQ-9.8.
//
// edit_failed is not induced: ApplyEdits returns only *EditError, which
// edit_file reports as edit_<phase>; see docs/errata/06_tool_output_schemas.md.
func TestErrorCodesFailures_TS06_29(t *testing.T) {
	root := conformanceWorkspace(t)
	all := builtins(t, root)
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

// validateToolData asserts that data conforms to tool.OutputSchema, through
// the same JSON a programmatic caller would decode.
//
// The schema is checked as the value of a property rather than as the root:
// schema.Validate walks a root schema's properties only, so a root oneOf
// would never be evaluated.
func validateToolData(t *testing.T, tool core.Tool, data map[string]any) {
	t.Helper()
	if tool.OutputSchema == nil {
		t.Fatalf("%s declares no OutputSchema", tool.Name)
	}
	if data == nil {
		t.Fatalf("%s returned no Data", tool.Name)
	}
	blob, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatalf("%s: Data does not marshal: %v", tool.Name, err)
	}
	ordered, err := jsonx.DecodeOrderedObject(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(schema.Object(schema.Prop("data", tool.OutputSchema)), ordered); err != nil {
		t.Errorf("%s: Data does not conform to OutputSchema: %v\nData: %s", tool.Name, err, blob)
	}
}

// TS-06-22: every built-in tool's successful Data conforms to its declared
// OutputSchema, across the result shapes each one has: empty results,
// truncation notes.
// 06-REQ-9.1.
func TestConformanceSuccessDataMatchesOutputSchema_TS06_22(t *testing.T) {
	root := conformanceWorkspace(t)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	all := builtins(t, root)

	type call struct{ tool, args string }
	calls := []call{
		{"read_file", `{"path":"main.go"}`},
		{"write_file", `{"path":"new.txt","content":"hello\n"}`},
		{"edit_file", `{"path":"new.txt","edits":[{"old_string":"hello","new_string":"bye"}]}`},
		{"list_files", `{}`},
		{"list_files", `{"path":"empty"}`},
		{"list_files", `{"limit":1}`},
		{"find_files", `{"pattern":"**/*.go"}`},
		{"find_files", `{"pattern":"**/*.none"}`},
		{"find_files", `{"pattern":"**/*","limit":1}`},
		{"search_files", `{"pattern":"Greet","context_lines":1}`},
		{"search_files", `{"pattern":"no_such_text_anywhere"}`},
		{"search_files", `{"pattern":"e","max_matches":1}`},
		{"file_outline", `{"path":"main.go","include_private":true}`},
		{"find_symbol", `{"name":"Greet"}`},
		{"find_symbol", `{"name":"NoSuchSymbol"}`},
		{"find_references", `{"name":"Greet"}`},
	}
	if runtime.GOOS != "windows" {
		calls = append(calls,
			call{"execute", `{"command":"echo hi"}`},
			call{"run_command", `{"argv":["echo","hi"]}`})
	}
	covered := map[string]bool{}
	for _, c := range calls {
		tl := all[c.tool]
		res := tl.Execute(context.Background(), json.RawMessage(c.args))
		if !res.OK {
			t.Errorf("%s %s: OK false, %s: %s", c.tool, c.args, res.Error, res.Detail)
			continue
		}
		covered[c.tool] = true
		validateToolData(t, tl, res.Data)
	}

	want := []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
		"file_outline", "find_symbol", "find_references"}
	if runtime.GOOS != "windows" {
		want = append(want, "execute", "run_command")
	}
	for _, name := range want {
		if !covered[name] {
			t.Errorf("%s produced no successful result to validate", name)
		}
	}
}

// TS-06-27: a failed subprocess still returns Data that conforms to the
// shared subprocess schema: a non-zero exit is command_exit, a binary that
// cannot start is exec_failed. 06-REQ-9.6.
func TestConformanceSubprocessFailureData_TS06_27(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	all := builtins(t, conformanceWorkspace(t))
	for name, args := range map[string]string{
		"execute":     `{"command":"echo partial; exit 1"}`,
		"run_command": `{"argv":["sh","-c","echo partial; exit 1"]}`,
	} {
		res := expectError(t, context.Background(), all[name], args, "command_exit")
		validateToolData(t, all[name], res.Data)
	}
	// A process that never started has no output, exit code or outcome:
	// exec_failed carries no Data, and there is nothing to validate. See
	// docs/errata/06_tool_output_schemas.md.
	res := expectError(t, context.Background(), all["run_command"], `{"argv":["/nonexistent_bin_xyz"]}`, "exec_failed")
	if res.Data != nil {
		t.Fatalf("exec_failed Data = %+v, want none", res.Data)
	}
}

// TS-06-30 (smoke, 06-PATH-1): a caller runs the real read_file and
// list_files from tools.All on a real workspace, marshals Data, decodes it
// ordered and validates it with schema.Validate.
func TestSmokeBuiltinDataValidates_TS06_30(t *testing.T) {
	root := conformanceWorkspace(t)
	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, tl := range all {
		var args string
		switch tl.Name {
		case "read_file":
			args = `{"path":"main.go"}`
		case "list_files":
			args = `{"path":"."}`
		default:
			continue
		}
		res := tl.Execute(context.Background(), json.RawMessage(args))
		if !res.OK || res.Data == nil {
			t.Fatalf("%s: OK %v Data %v (%s: %s)", tl.Name, res.OK, res.Data, res.Error, res.Detail)
		}
		validateToolData(t, tl, res.Data)
		ran++
	}
	if ran != 2 {
		t.Fatalf("ran %d of read_file and list_files", ran)
	}
}

// TS-06-33 (smoke, 06-PATH-4): the real execute and run_command, through
// tools.Run, exit 2; Data carries output, exit_code 2 and outcome exit, and
// conforms to the subprocess schema.
func TestSmokeSubprocessFailureData_TS06_33(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	all := builtins(t, conformanceWorkspace(t))
	for name, args := range map[string]string{
		"execute":     `{"command":"echo failing; exit 2"}`,
		"run_command": `{"argv":["sh","-c","echo failing; exit 2"]}`,
	} {
		res := expectError(t, context.Background(), all[name], args, "command_exit")
		if res.Data["exit_code"] != 2 || res.Data["outcome"] != "exit" ||
			!strings.Contains(fmt.Sprint(res.Data["output"]), "failing") {
			t.Fatalf("%s: Data = %+v, want output, exit_code 2, outcome exit", name, res.Data)
		}
		validateToolData(t, all[name], res.Data)
	}
}
