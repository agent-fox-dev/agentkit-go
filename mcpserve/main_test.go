package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// childMarker selects, in the re-executed test binary, the program that serves
// a runner on its stdio. childExitFile names the file it leaves its outcome in.
const (
	childMarker    = "MCPSERVE_TEST_CHILD"
	childExitFile  = "MCPSERVE_TEST_CHILD_EXIT"
	childWorkspace = "MCPSERVE_TEST_CHILD_WORKSPACE"
)

func TestMain(m *testing.M) {
	if os.Getenv(childMarker) != "" {
		os.Exit(serveChild())
	}
	os.Exit(m.Run())
}

// serveChild is the child program of the process-level tests: a runner served
// on stdio until the client leaves. It has one tool, echo, or, when the
// environment names a workspace directory, the built-in tools of that
// workspace and a code_mode tool over them. It records how it ended, since a
// pool does not report a child's exit status.
func serveChild() int {
	echo := core.Tool{Name: "echo", Description: "answers with its text argument", InputSchema: schema.Object(schema.Prop("text", schema.String())),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var a struct{ Text string }
			_ = json.Unmarshal(in, &a)
			return core.OKResult(map[string]any{"echo": a.Text})
		}}
	var r *agentkit.Runner
	var err error
	if dir := os.Getenv(childWorkspace); dir != "" {
		r, err = workspaceRunner(dir)
	} else {
		r, err = agentkit.NewRunner(agentkit.Toolset{Tools: []core.Tool{echo}})
	}
	if err == nil {
		var s *Server
		if s, err = New(r, Options{}); err == nil {
			err = s.ServeStdio(context.Background())
		}
	}
	outcome, code := "exit 0", 0
	if err != nil {
		outcome, code = fmt.Sprintf("error: %v", err), 1
	}
	if f := os.Getenv(childExitFile); f != "" {
		_ = os.WriteFile(f, []byte(outcome), 0o600)
	}
	return code
}

// workspaceRunner is a runner over the built-in tools of the workspace at dir
// and a code_mode tool that binds them, under a guard that allows everything.
func workspaceRunner(dir string) (*agentkit.Runner, error) {
	ws, err := workspace.Open(dir, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		return nil, err
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		return nil, err
	}
	cm, _, err := codemode.New(all, codemode.Options{})
	if err != nil {
		return nil, err
	}
	return agentkit.NewRunner(agentkit.Toolset{Tools: append([]core.Tool{cm}, all...), Guard: guard.AllowAll})
}
