package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// childMarker selects, in the re-executed test binary, the program that serves
// a runner on its stdio. childExitFile names the file it leaves its outcome in.
const (
	childMarker   = "MCPSERVE_TEST_CHILD"
	childExitFile = "MCPSERVE_TEST_CHILD_EXIT"
)

func TestMain(m *testing.M) {
	if os.Getenv(childMarker) != "" {
		os.Exit(serveChild())
	}
	os.Exit(m.Run())
}

// serveChild is the child program of the process-level tests: a runner with one
// tool, served on stdio until the client leaves. It records how it ended, since
// a pool does not report a child's exit status.
func serveChild() int {
	echo := core.Tool{Name: "echo", Description: "answers with its text argument", InputSchema: schema.Object(schema.Prop("text", schema.String())),
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var a struct{ Text string }
			_ = json.Unmarshal(in, &a)
			return core.OKResult(map[string]any{"echo": a.Text})
		}}
	r, err := agentkit.NewRunner(agentkit.Toolset{Tools: []core.Tool{echo}})
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
