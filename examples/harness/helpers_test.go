package main

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// defaults are the flags' defaults, as main sets them, with the export under the test's directory.
func defaults(t *testing.T) options {
	return options{Harness: "scripted", Network: "none", Allow: "go,gofmt,git,specr", MaxTurns: 8, SpecTokens: 10000, Out: filepath.Join(t.TempDir(), "out")}
}

// hasSeq reports whether want occurs in args as a run of consecutive elements.
func hasSeq(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// fakeSandbox is a runner, built as the program builds it, over stand-ins for
// the sandbox's execute and read_file: execute answers a command by the entry
// of answers with the longest key that prefixes it (OK with no output when none does),
// and read_file answers every path with content. seen collects the commands
// in the order the guard saw them.
func fakeSandbox(t *testing.T, answers map[string]core.ToolResult, content string) (r *agentkit.Runner, h *hooks, seen *[]string) {
	t.Helper()
	execute := core.Tool{Name: "execute", InputSchema: schema.Object(schema.Prop("command", schema.String()), schema.Opt("timeout_s", schema.Int())),
		PromptGuidelines: []string{"Run one program per call."},
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var a struct{ Command string }
			_ = json.Unmarshal(in, &a)
			best, res := -1, ran("ok", 0, "")
			for prefix, r := range answers {
				if strings.HasPrefix(a.Command, prefix) && len(prefix) > best {
					best, res = len(prefix), r
				}
			}
			return res
		}}
	readFile := core.Tool{Name: "read_file", InputSchema: schema.Object(schema.Prop("path", schema.String())),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.OKResult(map[string]any{"content": content})
		}}
	seen = new([]string)
	record := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		cmd, _ := in.Arguments["command"].(string)
		*seen = append(*seen, strings.TrimSpace(in.ToolName+" "+cmd))
		return core.BeforeToolCallDecision{Block: strings.Contains(cmd, "|"), Reason: "no pipes"}
	}
	r, h, _, err := buildRunner([]core.Tool{execute, readFile}, record, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return r, h, seen
}

// ran is an execute result for a command that ran, with its outcome, exit code and output.
func ran(outcome string, exit int, output string) core.ToolResult {
	res := core.OKResult(map[string]any{"output": output, "outcome": outcome, "exit_code": exit})
	if outcome != "ok" {
		res.OK, res.Error = false, "command_exit"
	}
	return res
}
