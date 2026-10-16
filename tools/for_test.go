package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// openForDir opens a workspace over dir with no global excludes.
func openForDir(t *testing.T, dir string) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Open(dir, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// builtinNames is the order tools.For returns the built-ins in.
var builtinNames = []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
	"file_outline", "find_symbol", "find_references", "execute", "run_command"}

// definition is what a tool declares to a model and to a sandbox host: the
// fields sandbox/internal/tooldigest hashes.
type definition struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	InputSchema      json.RawMessage `json:"input_schema"`
	OutputSchema     json.RawMessage `json:"output_schema"`
	PromptGuidelines []string        `json:"prompt_guidelines"`
	Builtin          bool            `json:"builtin"`
	ExecutionMode    int             `json:"execution_mode"`
}

func definitionsOf(t *testing.T, ts []core.Tool) []byte {
	t.Helper()
	var defs []definition
	for _, tl := range ts {
		in, err := json.Marshal(tl.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(tl.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		defs = append(defs, definition{tl.Name, tl.Description, in, out, tl.PromptGuidelines, tl.Builtin, int(tl.ExecutionMode)})
	}
	b, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func namesOf(ts []core.Tool) []string {
	var out []string
	for _, tl := range ts {
		out = append(out, tl.Name)
	}
	return out
}

func toolIn(t *testing.T, ts []core.Tool, name string) core.Tool {
	t.Helper()
	for _, tl := range ts {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("%s is not in the tool set %v", name, namesOf(ts))
	return core.Tool{}
}

// TS-20-14: The file tools fail with result code path_not_allowed for a refused
// path and leave the file untouched.
func TestFileToolsRefuseAPathTheWorkspaceRefuses_TS20_14(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "o.txt")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(outside)
	if err != nil {
		t.Fatal(err)
	}
	ws := openForDir(t, root)
	if err := os.Symlink(outsideDir, filepath.Join(ws.Root(), "out")); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{outside, "out/o.txt"} {
		quoted, _ := json.Marshal(path)
		calls := map[string]string{
			"read_file":  `{"path":` + string(quoted) + `}`,
			"write_file": `{"path":` + string(quoted) + `,"content":"x"}`,
			"edit_file":  `{"path":` + string(quoted) + `,"edits":[{"old_string":"original","new_string":"x"}]}`,
		}
		for name, args := range calls {
			res := toolIn(t, ts, name).Execute(context.Background(), json.RawMessage(args))
			if res.OK || res.Error != "path_not_allowed" {
				t.Errorf("%s %s: OK=%v Error=%q, want an error result with code path_not_allowed", name, path, res.OK, res.Error)
			}
			if !strings.HasPrefix(res.Detail, "workspace: path_not_allowed") {
				t.Errorf("%s %s: detail %q does not start with the workspace error text", name, path, res.Detail)
			}
		}
	}

	// A new file through the outward link must not appear either.
	res := toolIn(t, ts, "write_file").Execute(context.Background(), json.RawMessage(`{"path":"out/new.txt","content":"x"}`))
	if res.OK || res.Error != "path_not_allowed" {
		t.Errorf("write_file out/new.txt: OK=%v Error=%q", res.OK, res.Error)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "new.txt")); err == nil {
		t.Error("a file was created outside the workspace")
	}
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Errorf("the outside file now reads %q", got)
	}
	if after, err := os.Stat(outside); err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the outside file's mtime changed: %v %v", before.ModTime(), after)
	}
}

// TS-20-33: tools.For returns the eleven built-ins in order with definitions
// byte-identical to what tools.All returned before this change (recorded in
// testdata/definitions.json).
func TestForReturnsTheBuiltinsUnchanged_TS20_33(t *testing.T) {
	ws := openForDir(t, t.TempDir())
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	if len(ts) != 11 {
		t.Fatalf("For returned %d tools, want 11: %v", len(ts), namesOf(ts))
	}
	if got := namesOf(ts); !slices.Equal(got, builtinNames) {
		t.Fatalf("tool order = %v, want %v", got, builtinNames)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "definitions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := definitionsOf(t, ts); string(got) != string(want) {
		t.Errorf("the definitions differ from the ones tools.All returned before the change:\n got  %s\n want %s", got, want)
	}
}

// stubIndex is a tools.Index providing one tool.
type stubIndex struct{ tool string }

func (stubIndex) Symbols(context.Context, tools.SymbolQuery) (tools.SymbolAnswer, bool, error) {
	return tools.SymbolAnswer{}, false, nil
}
func (i stubIndex) Tools() []core.Tool { return []core.Tool{{Name: i.tool}} }
func (stubIndex) Invalidate(string)    {}
func (stubIndex) Close() error         { return nil }

// TS-20-35: Index tools are appended after the built-ins and a duplicate name
// fails.
func TestForAppendsIndexTools_TS20_35(t *testing.T) {
	ws := openForDir(t, t.TempDir())

	ts, err := tools.For(ws, tools.Options{Index: stubIndex{"idx_search"}})
	if err != nil {
		t.Fatalf("For with an index: %v", err)
	}
	if len(ts) != 12 || ts[11].Name != "idx_search" {
		t.Errorf("For with an index returned %v, want the built-ins then idx_search", namesOf(ts))
	}

	ts, err = tools.For(ws, tools.Options{Index: stubIndex{"read_file"}})
	if ts != nil {
		t.Errorf("a duplicate name returned %v tools, want none", namesOf(ts))
	}
	// The text recorded from tools.All before the change.
	const want = `tools: index tool "read_file" duplicates a built-in tool name`
	if err == nil || err.Error() != want {
		t.Errorf("a duplicate name: err = %v, want %q", err, want)
	}
}

// TS-20-36: tools.For with a nil workspace returns no tools and an error naming
// the workspace.
func TestForRefusesANilWorkspace_TS20_36(t *testing.T) {
	ts, err := tools.For(nil, tools.Options{})
	if ts != nil {
		t.Errorf("For(nil) returned %d tools, want none", len(ts))
	}
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("For(nil) err = %v, want an error naming the workspace", err)
	}
}

// TS-20-38: execute and run_command run in the workspace root.
func TestShellToolsRunInTheWorkspaceRoot_TS20_38(t *testing.T) {
	ws := openForDir(t, t.TempDir())
	if cwd, err := os.Getwd(); err != nil || cwd == ws.Root() {
		t.Fatalf("test setup: the process working directory %q must differ from the workspace root", cwd)
	}
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]string{
		"execute":     `{"command":"pwd"}`,
		"run_command": `{"argv":["pwd"]}`,
	} {
		res := toolIn(t, ts, name).Execute(context.Background(), json.RawMessage(args))
		if !res.OK {
			t.Errorf("%s: %s %s", name, res.Error, res.Detail)
			continue
		}
		if !strings.Contains(res.Text, ws.Root()) {
			t.Errorf("%s printed %q, want the workspace root %q", name, res.Text, ws.Root())
		}
	}
}
