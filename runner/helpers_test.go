package runner

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testkit"
)

func mustRunner(t *testing.T, ts Toolset) *Runner {
	t.Helper()
	r, err := New(ts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func use(t *testing.T, id, name, args string) core.ToolUseBlock {
	return testkit.ToolUse(t, id, name, args)
}

// The tool-side doubles are internal/testkit's, shared with the root
// package's tests.
var (
	executions = testkit.Executions
	echo       = testkit.EchoTool
	tool       = testkit.Tool
	returning  = testkit.Returning
	voter      = testkit.Voter
	wrapper    = testkit.Wrapper
	meet       = testkit.Meet
	attending  = testkit.Attending
	allow      = testkit.Allow
	blockIf    = testkit.BlockIf
	nested     = testkit.Uses
	child      = testkit.Child
	noopAfter  = testkit.NoopAfter
)

type (
	eventLog = testkit.EventLog
	guardLog = testkit.GuardLog
)
