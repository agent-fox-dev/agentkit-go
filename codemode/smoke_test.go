package codemode_test

// Spec 08 smoke tests: each execution path through a real agent, real
// built-in tools, the real nested dispatcher and a real Starlark thread. Only
// the model is scripted (provider/faux).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/tools"
)

// smokeWorkspace has two Go files and three text files.
func smokeWorkspace(t *testing.T) *tools.Workspace {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"main.go": "package main\n", "util.go": "package main\n", "notes.md": "notes\n",
		"a.txt": "content_a\n", "b.txt": "content_b\n", "c.txt": "content_c\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := tools.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// builtin is the named tool from tools.All.
func builtin(t *testing.T, ws *tools.Workspace, names ...string) []core.Tool {
	t.Helper()
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	var out []core.Tool
	for _, n := range names {
		for _, tl := range all {
			if tl.Name == n {
				out = append(out, tl)
			}
		}
	}
	if len(out) != len(names) {
		t.Fatalf("found %d of %v", len(out), names)
	}
	return out
}

// runAgent has the model call cm once with script, through a real agent,
// and returns the code-mode result as the handler returned it, the nested
// calls the agent closed on its event stream, and the transcript.
func runAgent(t *testing.T, cm core.Tool, script string) (core.ToolResult, []core.ToolExecutionEndEvent, core.RunResult) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"script": script})
	model := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("cm_call", cm.Name, string(args))}, StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, StopReason: core.StopReasonStop},
	)
	var out core.ToolResult
	agent, err := agentkit.NewAgent(core.AgentConfig{
		Model:         faux.Model(),
		Providers:     core.ProviderRegistry{faux.API: model.APIProvider()},
		StopPolicy:    func(sc core.StopContext) bool { return sc.TurnCount >= 4 },
		ParallelTools: true,
		ToolPolicy:    core.ToolPolicy{CustomTools: []core.Tool{cm}},
		AfterToolCall: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == cm.Name {
				out = in.ToolResult
			}
			return core.AfterToolCallDecision{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := agent.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var nested []core.ToolExecutionEndEvent
	for e := range st.Events() {
		if end, ok := e.(core.ToolExecutionEndEvent); ok && end.ParentToolUseID != "" {
			nested = append(nested, end)
		}
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}
	return out, nested, res
}

// TS-08-45 (smoke, 08-PATH-1): a script calls the real list_files and
// filters its entries; the agent reports one nested call.
func TestSmokeListAndFilter_TS08_45(t *testing.T) {
	ws := smokeWorkspace(t)
	cm, _, err := codemode.New(builtin(t, ws, "list_files"), codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, nested, run := runAgent(t, cm, `entries = list_files(path=".")
for e in entries["entries"]:
    if e.endswith(".go"):
        print(e)
`)
	if !res.OK || res.Text != "main.go\nutil.go" {
		t.Fatalf("result = %+v", res)
	}
	if calls := res.Data["calls_completed"].([]any); len(calls) != 1 || calls[0].(map[string]any)["tool"] != "list_files" {
		t.Fatalf("calls = %+v", calls)
	}
	if len(nested) != 1 || nested[0].Name != "list_files" || nested[0].ParentToolUseID != "cm_call" {
		t.Fatalf("nested calls = %+v", nested)
	}
	for _, m := range run.Messages {
		if r, ok := m.(core.ToolResultMessage); ok && r.ToolName != cm.Name {
			t.Fatalf("a nested call reached the transcript: %s", r.ToolName)
		}
	}
}

// TS-08-46 (smoke, 08-PATH-2): parallel reads through the real read_file,
// in order, each reported as a nested call.
func TestSmokeParallelReads_TS08_46(t *testing.T) {
	ws := smokeWorkspace(t)
	cm, _, err := codemode.New(builtin(t, ws, "read_file"), codemode.Options{MaxConcurrentCalls: 2})
	if err != nil {
		t.Fatal(err)
	}
	res, nested, _ := runAgent(t, cm, `paths = ["a.txt", "b.txt", "c.txt"]
results = parallel([call(read_file, path=p) for p in paths])
for r in results:
    print(r["content"].strip())
`)
	if !res.OK || res.Text != "content_a\ncontent_b\ncontent_c" {
		t.Fatalf("result = %+v", res)
	}
	if calls := res.Data["calls_completed"].([]any); len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if len(nested) != 3 {
		t.Fatalf("%d nested calls, want 3", len(nested))
	}
}

// TS-08-47 (smoke, 08-PATH-3): output past MaxOutputBytes stops the script,
// is truncated, and is spilled to SpillDir.
func TestSmokeOutputLimitSpills_TS08_47(t *testing.T) {
	ws := smokeWorkspace(t)
	spill := t.TempDir()
	cm, _, err := codemode.New(builtin(t, ws, "read_file"), codemode.Options{MaxOutputBytes: 256, SpillDir: spill})
	if err != nil {
		t.Fatal(err)
	}
	res, _, run := runAgent(t, cm, `for i in range(100):
    print("line number " + str(i) + " with extensive logging payload")
`)
	m := res.Metadata
	if res.OK || res.Error != "output_limit_exceeded" || m == nil || !m.Truncated || m.TruncatedBy != "bytes" || m.SpillPath == "" {
		t.Fatalf("result = %+v / %+v", res, m)
	}
	if _, err := os.Stat(m.SpillPath); err != nil || !strings.HasPrefix(m.SpillPath, spill) {
		t.Fatalf("spill file %q: %v", m.SpillPath, err)
	}
	if !strings.Contains(res.Text, "bytes elided") || !strings.Contains(res.Text, "line number 0 ") {
		t.Fatalf("text = %q", res.Text)
	}
	// The transcript carries the same result, with its metadata.
	for _, msg := range run.Messages {
		if r, ok := msg.(core.ToolResultMessage); ok && r.ToolName == cm.Name {
			if !r.IsError || r.Metadata == nil || r.Metadata.SpillPath != m.SpillPath {
				t.Fatalf("transcript result = %+v", r)
			}
		}
	}
}

// TS-08-48 (smoke, 08-PATH-4): the real read_file fails on a missing file,
// the script gets a ToolError, falls back, and finishes OK.
func TestSmokeToolErrorFallback_TS08_48(t *testing.T) {
	ws := smokeWorkspace(t)
	cm, _, err := codemode.New(builtin(t, ws, "read_file"), codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	res, nested, _ := runAgent(t, cm, `res = read_file(path="missing.txt")
if is_error(res):
    print("File not found, using fallback")
else:
    print(res["content"])
`)
	if !res.OK || res.Text != "File not found, using fallback" {
		t.Fatalf("result = %+v", res)
	}
	calls := res.Data["calls_completed"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["ok"] != false || calls[0].(map[string]any)["error"] != "read_failed" {
		t.Fatalf("calls = %+v", calls)
	}
	if len(nested) != 1 || !nested[0].IsError {
		t.Fatalf("nested calls = %+v", nested)
	}
}
