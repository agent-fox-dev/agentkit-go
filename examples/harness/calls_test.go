package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// Direct calls and a script's nested calls alike pass the guard; a refused
// command, an unknown tool and a bad argument are results, and the hooks log
// each call, nested ones indented under code_mode, and count calls and blocks.
func TestEveryCallPassesTheGuardAndTheHooksLogAndCountIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the calls run POSIX commands")
	}
	ws, err := workspace.Open(testutil.Tree(t, map[string]string{"a.txt": "hello\n"}), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	runner, hk, info, err := buildRunner(all, guard.Allow("cat"), &log)
	if err != nil || info.BoundToolsCount != len(all) {
		t.Fatalf("buildRunner: %v; code_mode binds %d of %d tools", err, info.BoundToolsCount, len(all))
	}
	ctx := context.Background()
	for _, c := range []struct{ tool, input, code string }{
		{"execute", `{"command":"cat a.txt"}`, ""},
		{"execute", `{"command":"cat a.txt | wc -l"}`, core.BlockErrorCode},
		{"execute", `{"command":"rm a.txt"}`, core.BlockErrorCode},
		{"execute", `{"command":"cat missing.txt"}`, "command_exit"},
		{"no_such_tool", `{}`, "unknown_tool"},
		{"read_file", `{}`, "invalid_arguments"},
		{"read_file", `not json`, "invalid_arguments"},
	} {
		if res := runner.Call(ctx, c.tool, json.RawMessage(c.input)); res.Error != c.code || res.OK != (c.code == "") {
			t.Errorf("%s %s: %+v, want error %q", c.tool, c.input, res, c.code)
		}
	}
	script, _ := json.Marshal(map[string]string{"script": "a = read_file(path=\"a.txt\")\nprint(\"a:\", a[\"content\"].strip())\n" +
		"rm = execute(command=\"rm a.txt\")\nprint(\"rm:\", rm.error if is_error(rm) else \"ran\")\n"})
	if res := runner.Call(ctx, "code_mode", script); !res.OK || !strings.Contains(res.LLMText(), "a: hello") || !strings.Contains(res.LLMText(), "rm: blocked_by_policy") {
		t.Errorf("code_mode: %+v", res)
	}
	// A script reaches the tools code_mode binds and no others: not code_mode itself.
	self, _ := json.Marshal(map[string]string{"script": `code_mode(script="print(1)")`})
	if res := runner.Call(ctx, "code_mode", self); res.OK || !strings.Contains(res.Detail, "undefined: code_mode") {
		t.Errorf("code_mode calling itself: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "a.txt")); err != nil {
		t.Errorf("a.txt is gone: the guard did not hold: %v", err)
	}
	if hk.blocked.Load() != 3 || hk.calls.Load() < 11 {
		t.Errorf("counted %d calls and %d blocks, want every call and 3 blocks", hk.calls.Load(), hk.blocked.Load())
	}
	for _, want := range []string{"  run      execute command=cat a.txt", "  blocked  execute: guard.Restricted",
		"  error    execute: command_exit: exit 1", "    run      read_file path=a.txt", "    blocked  execute: guard.Restricted"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
}

func TestABlockWithoutAReasonIsLoggedAsBlockedByPolicy(t *testing.T) {
	var log strings.Builder
	h := &hooks{w: &log}
	refuse := func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{Block: true}
	}
	h.guard(refuse)(context.Background(), core.BeforeToolCallContext{ToolName: "execute", ParentToolUseID: "p"})
	if got := log.String(); got != "    blocked  execute: blocked by policy\n" || h.blocked.Load() != 1 {
		t.Errorf("logged %q after %d blocks", got, h.blocked.Load())
	}
}

func TestSummariesAreOneShortLineWithSortedKeys(t *testing.T) {
	long := strings.Repeat("x", 70)
	for got, want := range map[string]string{
		summarize(map[string]any{"path": "a.txt", "command": "two\nlines", "n": 3}): "command=two … n=3 path=a.txt",
		summarize(map[string]any{"content": long}):                                  "content=" + long[:60] + "…",
		summarize(nil): "",
		firstLine("  " + strings.Repeat("y", 130)): strings.Repeat("y", 120) + "…",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}
