package sandbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-13-70 (smoke, 13-PATH-2): a faux-driven code-mode script over proxy
// tools has one guard-blocked call that errors while the others complete,
// with nested events parented on code_mode. 13-REQ-10.2.
func TestSmokeCodeModeOverProxies_TS13_70(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell commands")
	}
	isolate(t)
	sb := newLocal(t)
	ctx := context.Background()
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, file("notes.txt", "a needle in here\n")))}); err != nil {
		t.Fatal(err)
	}
	cm, _, err := codemode.New([]core.Tool{tool(t, sb, "read_file"), tool(t, sb, "search_files"), tool(t, sb, "execute")}, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	script := `r = read_file(path="notes.txt")
print("read: " + r["content"].strip())
s = search_files(pattern="needle")
print("search: " + s["matches"][0]["file"])
ok = execute(command="echo ok")
print("ok: " + ok["output"].strip())
bad = execute(command="echo forbidden")
if is_error(bad):
    print("blocked: " + bad.error)
`
	args, _ := json.Marshal(map[string]string{"script": script})
	model := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("cm_call", cm.Name, string(args))}, StopReason: core.StopReasonToolUse},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, StopReason: core.StopReasonStop},
	)
	guard := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if cmd, _ := in.Arguments["command"].(string); in.ToolName == "execute" && strings.Contains(cmd, "forbidden") {
			return core.BeforeToolCallDecision{Block: true, Reason: "forbidden command"}
		}
		return core.BeforeToolCallDecision{}
	}
	var out core.ToolResult
	agent, err := agentkit.New(agentkit.Config{
		Provider: model, Model: faux.Model().ID, MaxTurns: 4, Toolset: agentkit.Toolset{Tools: []core.Tool{cm}, Guard: guard, After: func(_ context.Context, in core.AfterToolCallContext) core.AfterToolCallDecision {
			if in.ToolName == cm.Name {
				out = in.ToolResult
			}
			return core.AfterToolCallDecision{}
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := agent.Stream(ctx, "go")
	if err != nil {
		t.Fatal(err)
	}
	nested := 0
	for e := range st.Events() {
		var name, parent string
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			name, parent = ev.Name, ev.ParentToolUseID
		case core.ToolExecutionEndEvent:
			name, parent = ev.Name, ev.ParentToolUseID
		default:
			continue
		}
		if name == cm.Name {
			continue
		}
		nested++
		if parent != "cm_call" {
			t.Errorf("nested %s event has parent %q, want cm_call", name, parent)
		}
	}
	run, err := st.RunResult()
	if err != nil || run.FinalText() != "done" {
		t.Fatalf("run: %q, %v", run.FinalText(), err)
	}
	if nested == 0 {
		t.Fatal("no nested execution events")
	}
	for _, want := range []string{"read: a needle in here", "search: notes.txt", "ok: ok", "blocked: " + core.BlockErrorCode} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("script output lacks %q:\n%s", want, out.Text)
		}
	}
}
