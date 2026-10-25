package codemode

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func TestNewBuildsATypedToolThatReachesItsTools(t *testing.T) {
	tl, _, err := New([]core.Tool{leaf("dummy")}, Options{Name: "custom_mode"})
	must(t, err)
	if tl.Name != "custom_mode" || len(tl.ReachableTools) != 1 || tl.ReachableTools[0].Name != "dummy" || tl.Execute == nil {
		t.Fatalf("tool = %+v", tl)
	}
	if in := tl.InputSchema; !in.IsRequired("script") || in.Properties["script"].Type != schema.TypeString {
		t.Fatalf("InputSchema = %+v, want a required string script", in)
	}
	out := tl.OutputSchema
	if out.Properties["output"].Type != schema.TypeString || out.Properties["calls_completed"].Type != schema.TypeArray ||
		out.Properties["return_value"].Type != schema.TypeNone {
		t.Fatalf("OutputSchema = %+v", out)
	}
	def, _, err := New(nil, Options{})
	if err != nil || def.Name != DefaultName || len(def.PromptGuidelines) == 0 {
		t.Fatalf("defaults: name %q, guidelines %v, %v", def.Name, def.PromptGuidelines, err)
	}
	custom, _, err := New(nil, Options{Guidelines: []string{"custom guide"}})
	if err != nil || len(custom.PromptGuidelines) != 1 || custom.PromptGuidelines[0] != "custom guide" {
		t.Fatalf("custom guidelines = %v, %v", custom.PromptGuidelines, err)
	}
}

// Code mode cannot bind code mode: by name, a code-mode tool under any name,
// directly or behind a wrapper, even one hidden behind a same-named plain tool.
func TestNewRefusesToBindACodeModeTool(t *testing.T) {
	inner, _, err := New([]core.Tool{leaf("a")}, Options{Name: "read_file"})
	must(t, err)
	wrapper := leaf("wrapper")
	wrapper.ReachableTools = []core.Tool{inner}
	for _, tc := range []struct {
		name  string
		tools []core.Tool
	}{
		{"by name", []core.Tool{{Name: DefaultName}}},
		{"under another name", []core.Tool{inner}},
		{"behind a wrapper", []core.Tool{wrapper}},
		{"behind a same-named plain tool", []core.Tool{leaf("read_file"), wrapper}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := New(tc.tools, Options{})
			if err == nil || !strings.Contains(err.Error(), "codemode: cannot bind code_mode tool inside code_mode") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// A cycle of plain tools is walked once, not forever.
	a, b := leaf("a"), leaf("b")
	b.ReachableTools = []core.Tool{leaf("a")}
	a.ReachableTools = []core.Tool{b}
	if _, _, err := New([]core.Tool{a}, Options{}); err != nil {
		t.Fatalf("a name cycle: %v", err)
	}
}

// A name a script could not call is refused, as is a second tool of a name.
func TestNewRefusesNamesAScriptCannotCall(t *testing.T) {
	for _, name := range []string{"", "my-tool", "1st", "for", "print", "parallel", "call", "is_error", "main", "result"} {
		if _, _, err := New([]core.Tool{leaf(name)}, Options{}); err == nil || !strings.Contains(err.Error(), "codemode: tool name") {
			t.Fatalf("%q: err = %v", name, err)
		}
	}
	if _, _, err := New([]core.Tool{leaf("lookup"), leaf("lookup")}, Options{}); err == nil ||
		!strings.Contains(err.Error(), "codemode: duplicate tool name: lookup") {
		t.Fatalf("duplicate: err = %v", err)
	}
}

func TestBuildInfoMeasuresTheDescription(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tools []core.Tool
		opts  Options
	}{
		{"generated", []core.Tool{leaf("t1"), leaf("t2")}, Options{}},
		{"explicit, multibyte", nil, Options{Description: "üü—x"}},
	} {
		tl, info, err := New(tc.tools, tc.opts)
		must(t, err)
		if info.BoundToolsCount != len(tc.tools) || info.DescriptionBytes != len(tl.Description) ||
			info.DescriptionChars != utf8.RuneCountInString(tl.Description) {
			t.Fatalf("%s: info %+v for a %d-byte description and %d tools", tc.name, info, len(tl.Description), len(tc.tools))
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// builtins are the named tools of tools.For, over a workspace of two Go files
// and three text files.
func builtins(t *testing.T, names ...string) []core.Tool {
	t.Helper()
	dir := testutil.Tree(t, map[string]string{
		"main.go": "package main\n", "util.go": "package main\n", "notes.md": "notes\n",
		"a.txt": "content_a\n", "b.txt": "content_b\n", "c.txt": "content_c\n",
	})
	ws, err := workspace.Open(dir, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	must(t, err)
	all, err := tools.For(ws, tools.Options{})
	must(t, err)
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

// runAgent has a scripted model call cm once with script through a real agent,
// and returns the code-mode result, the nested calls the agent closed on its
// event stream, and the transcript.
func runAgent(t *testing.T, cm core.Tool, script string) (core.ToolResult, []core.ToolExecutionEndEvent, core.RunResult) {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"script": script})
	model := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("cm_call", cm.Name, string(args))}, StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, StopReason: core.StopReasonStop},
	)
	var out core.ToolResult
	agent, err := agentkit.New(agentkit.Config{Client: model, Model: faux.Model().ID, MaxTurns: 4,
		Toolset: agentkit.Toolset{Tools: []core.Tool{cm}, After: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == cm.Name {
				out = in.ToolResult
			}
			return core.AfterToolCallDecision{}
		}},
	})
	must(t, err)
	st, err := agent.Stream(context.Background(), "go")
	must(t, err)
	var nested []core.ToolExecutionEndEvent
	for e := range st.Events() {
		if end, ok := e.(core.ToolExecutionEndEvent); ok && end.ParentToolUseID != "" {
			nested = append(nested, end)
		}
	}
	res, err := st.RunResult()
	must(t, err)
	return out, nested, res
}

// A bound tool's call goes through the agent's nested dispatcher: the agent
// reports it as a nested call, and only the code-mode result reaches the
// transcript.
func TestAScriptsCallsGoThroughTheAgentsNestedDispatcher(t *testing.T) {
	cm, _, err := New(builtins(t, "list_files"), Options{})
	must(t, err)
	res, nested, run := runAgent(t, cm, "entries = list_files(path=\".\")\nfor e in entries[\"entries\"]:\n"+
		"    if e.endswith(\".go\"):\n        print(e)\n")
	if !res.OK || res.Text != "main.go\nutil.go" || !strings.Contains(ledger(t, res), `"tool":"list_files"`) {
		t.Fatalf("result = %+v", res)
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

func TestParallelReadsThroughARealAgentComeBackInOrder(t *testing.T) {
	cm, _, err := New(builtins(t, "read_file"), Options{MaxConcurrentCalls: 2})
	must(t, err)
	res, nested, _ := runAgent(t, cm, "results = parallel([call(read_file, path=p) for p in [\"a.txt\", \"b.txt\", \"c.txt\"]])\n"+
		"for r in results:\n    print(r[\"content\"].strip())\n")
	if !res.OK || res.Text != "content_a\ncontent_b\ncontent_c" || len(nested) != 3 {
		t.Fatalf("result = %+v, %d nested calls", res, len(nested))
	}
}

// A real tool's failure is a ToolError the script falls back on, and a failed
// nested call for the agent.
func TestARealToolsFailureIsAValueTheScriptHandles(t *testing.T) {
	cm, _, err := New(builtins(t, "read_file"), Options{})
	must(t, err)
	res, nested, _ := runAgent(t, cm, "res = read_file(path=\"missing.txt\")\nif is_error(res):\n"+
		"    print(\"fallback\")\nelse:\n    print(res[\"content\"])\n")
	if !res.OK || res.Text != "fallback" || !strings.Contains(ledger(t, res), `"error":"read_failed","ok":false`) {
		t.Fatalf("result = %+v", res)
	}
	if len(nested) != 1 || !nested[0].IsError {
		t.Fatalf("nested calls = %+v", nested)
	}
}

// Truncated, spilled output reaches the transcript with its metadata.
func TestASpilledResultReachesTheTranscriptWithItsMetadata(t *testing.T) {
	spill := t.TempDir()
	cm, _, err := New(builtins(t, "read_file"), Options{MaxOutputBytes: 256, SpillDir: spill})
	must(t, err)
	res, _, run := runAgent(t, cm, "for i in range(100):\n    print(\"line number \" + str(i) + \" with a logging payload\")\n")
	m := res.Metadata
	if res.Error != "output_limit_exceeded" || m == nil || !strings.HasPrefix(m.SpillPath, spill) {
		t.Fatalf("result = %+v / %+v", res, m)
	}
	if _, err := os.Stat(m.SpillPath); err != nil {
		t.Fatal(err)
	}
	for _, msg := range run.Messages {
		if r, ok := msg.(core.ToolResultMessage); ok && r.ToolName == cm.Name {
			if !r.IsError || r.Metadata == nil || r.Metadata.SpillPath != m.SpillPath {
				t.Fatalf("transcript result = %+v", r)
			}
		}
	}
}
