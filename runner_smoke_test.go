package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// smokeWorkspace opens a temporary workspace holding the given files and
// builds the real tools over it.
func smokeWorkspace(t *testing.T, files map[string]string) []core.Tool {
	t.Helper()
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(ws.Root(), name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// TS-21-57 (smoke, 21-PATH-1): an embedder runs code mode through a runner
// with no agent. The workspace, the tools over it, code mode, the runner and
// the nested caller are all real; there is no provider.
func TestSmokeCodeModeThroughARunnerWithNoAgent_TS21_57(t *testing.T) {
	all := smokeWorkspace(t, map[string]string{"a.txt": "alpha content\n"})
	cm, _, err := codemode.New(all, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var events eventLog
	var guards guardLog
	r, err := NewRunner(Toolset{Tools: append(slices.Clone(all), cm), Guard: guards.guard, Events: events.add})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	script, _ := json.Marshal(map[string]string{"script": "res = read_file(path=\"a.txt\")\nprint(res[\"content\"].strip())\n"})
	res := r.Call(context.Background(), "code_mode", script)
	if !res.OK || !strings.Contains(res.LLMText(), "alpha content") {
		t.Fatalf("Call = %+v; want OK with the file's content", res)
	}

	var cmID string
	for _, g := range guards.seen() {
		if g.ToolName == "code_mode" {
			cmID = g.ToolUseID
		}
	}
	if cmID == "" {
		t.Fatal("Guard never saw the code_mode call")
	}
	var nestedStart, nestedEnd, cmEnd, cmResult int
	for _, e := range events.all() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.Name == "read_file" && ev.ParentToolUseID == cmID {
				nestedStart++
			}
		case core.ToolExecutionEndEvent:
			if ev.Name == "read_file" && ev.ParentToolUseID == cmID {
				nestedEnd++
			}
			if ev.ToolUseID == cmID {
				cmEnd++
			}
		case core.ToolResultEvent:
			if ev.Message.ToolUseID == cmID {
				cmResult++
			}
		}
	}
	if nestedStart != 1 || nestedEnd != 1 || cmEnd != 1 || cmResult != 1 {
		t.Errorf("nested read_file start/end = %d/%d, code_mode end/result = %d/%d; want 1/1 and 1/1",
			nestedStart, nestedEnd, cmEnd, cmResult)
	}
	// The guard saw the script's call as the nested call it is.
	var nestedGuarded bool
	for _, g := range guards.seen() {
		if g.ToolName == "read_file" && g.ParentToolUseID == cmID && g.ParentToolName == "code_mode" {
			nestedGuarded = true
		}
	}
	if !nestedGuarded {
		t.Error("Guard never saw read_file as a call nested under code_mode")
	}
}

// TS-21-58 (smoke, 21-PATH-2): an agent run executes a turn's two tool calls
// through its runner. The agent, the runner, the event stream and the tools
// are real; only the model is scripted.
func TestSmokeAgentRunExecutesATurnThroughItsRunner_TS21_58(t *testing.T) {
	all := smokeWorkspace(t, map[string]string{"a.txt": "alpha\n", "b.txt": "beta\n"})
	var readFile core.Tool
	for _, tl := range all {
		if tl.Name == "read_file" {
			readFile = tl
		}
	}
	fp := faux.New(
		callTurn(faux.FauxToolCall("c1", "read_file", `{"path":"a.txt"}`), faux.FauxToolCall("c2", "read_file", `{"path":"b.txt"}`)),
		finalTurn("both read"),
	)
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{Tools: []core.Tool{readFile}}})
	if err != nil {
		t.Fatal(err)
	}
	st, err := a.Stream(context.Background(), "read both files")
	if err != nil {
		t.Fatal(err)
	}
	starts, ends, results := map[string]int{}, map[string]int{}, map[string]string{}
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			starts[ev.ToolUseID]++
		case core.ToolExecutionEndEvent:
			ends[ev.ToolUseID]++
		case core.ToolResultEvent:
			results[ev.Message.ToolUseID] = ev.Message.Content.Text()
		}
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}

	reqs := fp.Requests()
	if len(reqs) != 2 || !slices.Equal(wireNames(reqs[0].Tools), toolNames(a.Runner().Tools())) {
		t.Fatalf("%d requests; first request tools %v, Runner().Tools() %v", len(reqs), wireNames(reqs[0].Tools), toolNames(a.Runner().Tools()))
	}
	for id, want := range map[string]string{"c1": "alpha", "c2": "beta"} {
		if starts[id] != 1 || ends[id] != 1 || !strings.Contains(results[id], want) {
			t.Errorf("%s: %d start, %d end events and result %q; want one of each and the file's %q", id, starts[id], ends[id], results[id], want)
		}
	}
	var toolResults int
	for _, m := range res.Messages {
		if _, ok := m.(core.ToolResultMessage); ok {
			toolResults++
		}
	}
	if toolResults != 2 || res.FinalText() != "both read" || res.StopReason != core.RunStopEndTurn {
		t.Errorf("%d tool results, final %q, stop %q; want 2, %q and end_turn", toolResults, res.FinalText(), res.StopReason, "both read")
	}
}

// TS-21-59 (smoke, 21-PATH-3): a misconfigured toolset is refused before any
// request. The real run_command tool, New, NewRunner and the shell check are
// in play; the provider only counts requests.
func TestSmokeUnguardedShellIsRefusedBeforeAnyRequest_TS21_59(t *testing.T) {
	all := smokeWorkspace(t, nil)
	var runCommand core.Tool
	for _, tl := range all {
		if tl.Name == "run_command" {
			runCommand = tl
		}
	}
	if !guard.IsShellTool(runCommand.Name) {
		t.Fatalf("run_command is not a shell tool to guard.IsShellTool")
	}
	fp := faux.New(finalTurn("never sent"))
	a, err := New(Config{Client: fp, Model: testModelID, Toolset: Toolset{Tools: []core.Tool{runCommand}}})
	if a != nil || !errors.Is(err, core.ErrUnguardedExecute) {
		t.Fatalf("New = %v, %v; want a nil agent and an error wrapping ErrUnguardedExecute", a, err)
	}
	if !strings.Contains(err.Error(), "Toolset.Guard is nil") || strings.Contains(err.Error(), "Config.Guard") {
		t.Errorf("the error %q should name Toolset.Guard", err)
	}
	if n := fp.Calls(); n != 0 {
		t.Fatalf("the provider received %d requests, want none", n)
	}
}
