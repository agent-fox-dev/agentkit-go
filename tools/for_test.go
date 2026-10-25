package tools

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/internal/toolconformance"
)

var builtinNames = []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
	"file_outline", "find_symbol", "find_references", "execute", "run_command"}

func names(ts []core.Tool) []string {
	var out []string
	for _, tl := range ts {
		out = append(out, tl.Name)
	}
	return out
}

// TestForReturnsTheBuiltinsAsTheModelSeesThem pins the order and the
// model-visible definitions: they head every cached prompt prefix.
func TestForReturnsTheBuiltinsAsTheModelSeesThem(t *testing.T) {
	ts, err := For(newWS(t, nil), Options{})
	if err != nil || !slices.Equal(names(ts), builtinNames) {
		t.Fatalf("For = %v, %v; want %v", names(ts), err, builtinNames)
	}
	type definition struct {
		Name             string          `json:"name"`
		Description      string          `json:"description"`
		InputSchema      json.RawMessage `json:"input_schema"`
		OutputSchema     json.RawMessage `json:"output_schema"`
		PromptGuidelines []string        `json:"prompt_guidelines"`
		Builtin          bool            `json:"builtin"`
		ExecutionMode    int             `json:"execution_mode"`
	}
	var defs []definition
	for _, tl := range ts {
		if (tl.Handler == nil) == (tl.Execute == nil) || !tl.Builtin {
			t.Errorf("%s: a built-in sets exactly one of Handler and Execute, and Builtin", tl.Name)
		}
		defs = append(defs, definition{tl.Name, tl.Description, json.RawMessage(testutil.MustJSON(t, tl.InputSchema)),
			json.RawMessage(testutil.MustJSON(t, tl.OutputSchema)), tl.PromptGuidelines, tl.Builtin, int(tl.ExecutionMode)})
	}
	b, err := json.MarshalIndent(defs, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, filepath.Join("testdata", "definitions.json"), append(b, '\n'))
	if nav := FileNavigationTools(); len(nav) != 6 || !slices.Contains(nav, "find_references") {
		t.Errorf("FileNavigationTools = %v", nav)
	}
}

func TestForRefusesANilWorkspace(t *testing.T) {
	if ts, err := For(nil, Options{}); ts != nil || err == nil || !strings.Contains(err.Error(), "workspace.Open") {
		t.Fatalf("For(nil) = %v, %v", ts, err)
	}
}

func TestForAppendsTheIndexsToolsAfterTheBuiltins(t *testing.T) {
	ws := newWS(t, nil)
	ts, err := For(ws, Options{Index: &fakeIndex{tools: []core.Tool{{Name: "code_search"}}}})
	if err != nil || !slices.Equal(names(ts), append(slices.Clone(builtinNames), "code_search")) {
		t.Fatalf("For with an index = %v, %v", names(ts), err)
	}
	ts, err = For(ws, Options{Index: &fakeIndex{tools: []core.Tool{{Name: "read_file"}}}})
	if ts != nil || err == nil || err.Error() != `tools: index tool "read_file" duplicates a built-in tool name` {
		t.Fatalf("a duplicate name: %v, %v", names(ts), err)
	}
}

// TestToolsConformToTheSharedCaseTable runs the conformance cases the
// sandbox's proxy tools also run, so the two cannot drift.
func TestToolsConformToTheSharedCaseTable(t *testing.T) {
	build := func(t *testing.T, tree toolconformance.Tree) []core.Tool {
		ws := newWS(t, nil)
		tree.Write(t, ws.Root())
		ts, err := For(ws, Options{})
		if err != nil {
			t.Fatal(err)
		}
		return ts
	}
	for _, name := range toolconformance.CaseNames() {
		t.Run(name, func(t *testing.T) { toolconformance.RunGroup(t, name, build) })
	}
}

// TestChangesInvalidateWhatTheyMayHaveTouched checks what each mutating tool
// marks, whether it succeeds or fails: the path in the symbol table, the
// reference cache and the index for write_file and edit_file, everything for
// a shell command.
func TestChangesInvalidateWhatTheyMayHaveTouched(t *testing.T) {
	ws := newWS(t, map[string]string{"sub/a.go": "package sub\n"})
	idx := &fakeIndex{}
	ts := toolSet(t, ws, Options{Index: idx})
	ft := newFT(ws, Options{Index: idx})
	ft.getTable()
	rc := ft.getRefCache()
	for _, c := range []struct {
		tool, args string
		ok         bool
		marked     string // the path marked, or "" for everything
	}{
		{"write_file", `{"path":"sub/a.go","content":"package sub\n\nfunc A() {}\n"}`, true, "sub/a.go"},
		{"write_file", `{"path":"sub","content":"x"}`, false, "sub"},
		{"edit_file", `{"path":"sub/a.go","edits":[{"old_string":"A()","new_string":"B()"}]}`, true, "sub/a.go"},
		{"edit_file", `{"path":"sub/a.go","edits":[{"old_string":"nowhere","new_string":"x"}]}`, false, "sub/a.go"},
	} {
		tl := map[string]core.Tool{"write_file": ft.writeFile(), "edit_file": ft.editFile()}[c.tool]
		if r := call(tl, c.args); r.OK != c.ok {
			t.Fatalf("%s %s: %+v", c.tool, c.args, r)
		}
		dirty, _ := marks(ft.table)
		if !dirty[c.marked] || !rc.isPathDirty(c.marked) || !slices.Equal(idx.take(), []string{c.marked}) {
			t.Errorf("%s %s: %s is not marked everywhere (table %v)", c.tool, c.args, c.marked, dirty)
		}
		ft.table.snapshotMarks()
		_ = rc.refresh(t.Context())
	}
	// An edit of a path that is not there changed nothing and marks nothing.
	if r := call(ft.editFile(), `{"path":"nope.go","edits":[{"old_string":"a","new_string":"b"}]}`); r.Error != "read_failed" {
		t.Fatalf("%+v", r)
	}
	if dirty, all := marks(ft.table); len(dirty) != 0 || all || len(idx.take()) != 0 {
		t.Errorf("a missing path marked %v (all %v)", dirty, all)
	}

	// For's shell tools mark everything, failed or not, and pass the result through.
	needPOSIX(t)
	for _, args := range []string{`{"command":"echo out-42"}`, `{"command":"false"}`} {
		r := call(ts["execute"], args)
		if (args == `{"command":"false"}`) == r.OK || !slices.Equal(idx.take(), []string{""}) {
			t.Errorf("execute %s: %+v", args, r)
		}
	}
	if r := call(ts["run_command"], `{"argv":["echo","rc-42"]}`); r.Text != "rc-42\n" || !slices.Equal(idx.take(), []string{""}) {
		t.Errorf("run_command: %q", r.Text)
	}
}
