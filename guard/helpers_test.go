package guard

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

func execCall(cmd string) core.BeforeToolCallContext {
	return core.BeforeToolCallContext{ToolName: "execute", Arguments: map[string]any{"command": cmd}}
}

func argvCall(argv ...any) core.BeforeToolCallContext {
	return core.BeforeToolCallContext{ToolName: "run_command", Arguments: map[string]any{"argv": argv}}
}

func decide(g core.BeforeToolCall, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
	return g(context.Background(), in)
}

// argvs renders each command's Argv as one space-joined string, so a case
// can say which programs a line runs in one literal.
func argvs(t *testing.T, line string) []string {
	t.Helper()
	cmds, err := Commands(line)
	if err != nil {
		t.Fatalf("Commands(%q): %v", line, err)
	}
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = strings.Join(c.Argv, " ")
	}
	return out
}
