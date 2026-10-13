// Package toolconformance holds the spec 06 conformance case table: every
// built-in tool returns its documented error codes, and its Data conforms to
// its declared OutputSchema. The tools package runs it against tools.All and
// the sandbox package against proxy tools, so the two cannot drift. Only
// tests import it.
//
// code_search lives in its own module and is covered in
// codesearch/tool_test.go.
package toolconformance

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// Entry is one file or directory of a workspace tree.
type Entry struct {
	Path string // slash-separated, relative
	Body string
	Mode fs.FileMode
	Dir  bool
}

// Tree is a workspace's contents, in order.
type Tree []Entry

// Write creates tree under dir.
func (tr Tree) Write(t testing.TB, dir string) {
	t.Helper()
	for _, e := range tr {
		p := filepath.Join(dir, filepath.FromSlash(e.Path))
		if e.Dir {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(e.Body), e.Mode); err != nil {
			t.Fatal(err)
		}
	}
}

// Tar returns tree as a tar stream.
func (tr Tree) Tar(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range tr {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: e.Path, Mode: int64(e.Mode), Size: int64(len(e.Body))}
		if e.Dir {
			h = &tar.Header{Typeflag: tar.TypeDir, Name: e.Path + "/", Mode: 0o755}
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.Body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// BaseTree is the small tree every conformance case runs against: a Go
// file, a 10-byte text file and a directory.
func BaseTree() Tree {
	return Tree{
		{Path: "f.txt", Body: "0123456789", Mode: 0o644},
		{Path: "main.go", Body: "package main\n\n// Greet says hello.\nfunc Greet() string { return \"hi\" }\n\nfunc main() { _ = Greet() }\n", Mode: 0o644},
		{Path: "dir/inner.txt", Body: "inner\n", Mode: 0o644},
	}
}

// Builder returns the built-in tools over a workspace holding tree.
type Builder func(t *testing.T, tree Tree) []core.Tool

// group is one conformance case group: its tree and its checks.
type group struct {
	tree func() Tree
	run  func(t *testing.T, all map[string]core.Tool)
}

// RunGroup runs the case group id (such as "TS-06-22") against the tools
// build returns.
func RunGroup(t *testing.T, id string, build Builder) {
	t.Helper()
	g, ok := groups[id]
	if !ok {
		t.Fatalf("no conformance case group %s", id)
	}
	tree := BaseTree()
	if g.tree != nil {
		tree = g.tree()
	}
	all := map[string]core.Tool{}
	for _, tl := range build(t, tree) {
		all[tl.Name] = tl
	}
	g.run(t, all)
}

// CaseNames lists the case groups, sorted.
func CaseNames() []string {
	names := make([]string, 0, len(groups))
	for id := range groups {
		names = append(names, id)
	}
	slices.Sort(names)
	return names
}

// unreadable reports whether a mode-0 file can be made unreadable here: not
// on Windows and not as root.
func unreadable() bool { return runtime.GOOS != "windows" && os.Geteuid() != 0 }

// posix reports whether POSIX shell commands run here.
func posix() bool { return runtime.GOOS != "windows" }

var groups = map[string]group{
	// TS-06-22: every built-in tool's successful Data conforms to its
	// declared OutputSchema, across the result shapes each one has: empty
	// results, truncation notes. 06-REQ-9.1.
	"TS-06-22": {
		tree: func() Tree { return append(BaseTree(), Entry{Path: "empty", Dir: true}) },
		run: func(t *testing.T, all map[string]core.Tool) {
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
			if posix() {
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
			if posix() {
				want = append(want, "execute", "run_command")
			}
			for _, name := range want {
				if !covered[name] {
					t.Errorf("%s produced no successful result to validate", name)
				}
			}
		},
	},

	// TS-06-23: malformed JSON is invalid_arguments for every built-in
	// tool. 06-REQ-9.2.
	"TS-06-23": {run: func(t *testing.T, all map[string]core.Tool) {
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
	}},

	// TS-06-24: a path outside the workspace is path_not_allowed.
	// 06-REQ-9.3.
	"TS-06-24": {run: func(t *testing.T, all map[string]core.Tool) {
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
	}},

	// TS-06-25: a directory is not_a_file for the tools that read one file.
	// 06-REQ-9.4.
	"TS-06-25": {run: func(t *testing.T, all map[string]core.Tool) {
		for name, args := range map[string]string{
			"read_file":    `{"path":"dir"}`,
			"edit_file":    `{"path":"dir","edits":[{"old_string":"a","new_string":"b"}]}`,
			"file_outline": `{"path":"dir"}`,
		} {
			expectError(t, context.Background(), all[name], args, "not_a_file")
		}
	}},

	// TS-06-26: a cancelled context is aborted for the tools that walk or
	// index the workspace. 06-REQ-9.5.
	"TS-06-26": {run: func(t *testing.T, all map[string]core.Tool) {
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
	}},

	// TS-06-27: a failed subprocess still returns Data that conforms to the
	// shared subprocess schema: a non-zero exit is command_exit, a binary
	// that cannot start is exec_failed. 06-REQ-9.6.
	"TS-06-27": {run: func(t *testing.T, all map[string]core.Tool) {
		if !posix() {
			t.Skip("POSIX shell commands")
		}
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
	}},

	// TS-06-28: reading past the end of a file. 06-REQ-9.7.
	"TS-06-28": {run: func(t *testing.T, all map[string]core.Tool) {
		expectError(t, context.Background(), all["read_file"], `{"path":"f.txt","offset":999}`, "offset_past_end")
	}},

	// TS-06-29: the I/O failures each tool documents. 06-REQ-9.8.
	//
	// edit_failed is not induced: ApplyEdits returns only *EditError, which
	// edit_file reports as edit_<phase>; see
	// docs/errata/06_tool_output_schemas.md.
	"TS-06-29": {
		tree: func() Tree {
			tree := BaseTree()
			if unreadable() {
				// The stat succeeds and the read does not: outline_failed.
				tree = append(tree, Entry{Path: "locked.go", Body: "package x\n", Mode: 0})
			}
			return tree
		},
		run: func(t *testing.T, all map[string]core.Tool) {
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
			if !unreadable() {
				t.Log("outline_failed needs an unreadable file; skipped on Windows and as root")
				return
			}
			expectError(t, bg, all["file_outline"], `{"path":"locked.go"}`, "outline_failed")
		},
	},

	// TS-06-30 (smoke, 06-PATH-1): a caller runs the real read_file and
	// list_files on a real workspace, marshals Data, decodes it ordered and
	// validates it with schema.Validate.
	"TS-06-30": {run: func(t *testing.T, all map[string]core.Tool) {
		ran := 0
		for name, args := range map[string]string{"read_file": `{"path":"main.go"}`, "list_files": `{"path":"."}`} {
			tl, ok := all[name]
			if !ok {
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
	}},

	// TS-06-33 (smoke, 06-PATH-4): the real execute and run_command exit 2;
	// Data carries output, exit_code 2 and outcome exit, and conforms to the
	// subprocess schema.
	"TS-06-33": {run: func(t *testing.T, all map[string]core.Tool) {
		if !posix() {
			t.Skip("POSIX shell commands")
		}
		for name, args := range map[string]string{
			"execute":     `{"command":"echo failing; exit 2"}`,
			"run_command": `{"argv":["sh","-c","echo failing; exit 2"]}`,
		} {
			res := expectError(t, context.Background(), all[name], args, "command_exit")
			// The exit code is compared by value: a local tool returns an int,
			// a proxy a json.Number decoded from the agent's JSON.
			if fmt.Sprint(res.Data["exit_code"]) != "2" || res.Data["outcome"] != "exit" ||
				!strings.Contains(fmt.Sprint(res.Data["output"]), "failing") {
				t.Fatalf("%s: Data = %+v, want output, exit_code 2, outcome exit", name, res.Data)
			}
			validateToolData(t, all[name], res.Data)
		}
	}},
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
	d := json.NewDecoder(bytes.NewReader(blob))
	d.UseNumber()
	var ordered map[string]any
	if err := d.Decode(&ordered); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(schema.Object(schema.Prop("data", tool.OutputSchema)), ordered); err != nil {
		t.Errorf("%s: Data does not conform to OutputSchema: %v\nData: %s", tool.Name, err, blob)
	}
}
