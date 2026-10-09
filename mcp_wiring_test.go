package agentkit

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpTools spins up an in-process MCP server, connects a pool to it, and
// returns the adapted core.Tools.
//
// The client is the SHIPPED implementation talking to the SDK's server over a
// pipe, so what this exercises is the real qualified-name path rather than a
// hand-built tool that merely has a name with two underscores in it.
func mcpTools(t *testing.T, serverName string) []core.Tool {
	t.Helper()
	srv := sdk.NewServer(&sdk.Implementation{Name: "github", Version: "1"}, nil)
	srv.AddTool(&mcp.Tool{Name: "create_issue", Description: "open an issue",
		InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, req *sdk.CallToolRequest) (*mcp.CallToolResult, error) {
			var args struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &args)
			return &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.TextContent{Text: "created: " + args.Title}}}, nil
		})

	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	ss, err := srv.Connect(context.Background(), mcp.NewPipeTransport(c2sR, s2cW, wire.Limits{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := mcp.Connect(context.Background(), mcp.ServerConfig{Name: serverName},
		mcp.NewPipeTransport(s2cR, c2sW, wire.Limits{}), mcp.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pool := mcp.NewPool(mcp.ConnectionOptions{})
	// REQ-MCP-CLIENT-06: the host declares the names no server may shadow,
	// BEFORE connecting. This wiring is the pattern an embedder copies, so it
	// sets the field even though this test attaches an already-open
	// connection with Add — leaving it empty here would demonstrate a pool
	// whose connect-time collision check is disarmed.
	pool.NativeTools = builtinToolNames(t)
	if err := pool.Add(conn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = pool.Close()
		_ = ss.Close()
	})

	tools, err := pool.Tools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

// TestMCPToolsAreGatedByQualifiedNameEverywhere is REQ-MCP-CLIENT-11.
//
// The allowlist and the permission callback both have to see the SAME name — the qualified one. A gate that matches on the unqualified
// name is a gate that does not apply, and it fails open: the tool runs and the
// policy that was meant to stop it never fired.
func TestMCPToolsAreGatedByQualifiedNameEverywhere(t *testing.T) {
	tools := mcpTools(t, "github")
	if len(tools) != 1 || tools[0].Name != "github__create_issue" {
		t.Fatalf("tools = %+v, want one github__create_issue", tools)
	}

	t.Run("the allowlist matches the qualified name", func(t *testing.T) {
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "c1", "github__create_issue", `{"title":"bug"}`)),
			{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
		}}
		a := newTestAgent(t, s, func(c *Config) {
			c.Tools = tools
			// The UNQUALIFIED name is deliberately not in the list.
			c.Policy.ToolNames = []string{"create_issue"}
		})
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		msg := findToolResult(t, res.Messages, "c1")
		if !msg.IsError {
			t.Fatal("an allowlist naming the UNQUALIFIED tool must not admit the " +
				"qualified one; matching loosely here fails open")
		}
	})

	t.Run("the permission callback sees the qualified name", func(t *testing.T) {
		var seen []string
		s := &scripted{turns: []core.AssistantMessage{
			assistantWithTools(core.StopReasonToolUse,
				toolUse(t, "c1", "github__create_issue", `{"title":"bug"}`)),
			{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
		}}
		a := newTestAgent(t, s, func(c *Config) {
			c.Tools = tools
			c.Guard = func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
				seen = append(seen, in.ToolName)
				return core.BeforeToolCallDecision{}
			}
		})
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if strings.Join(seen, ",") != "github__create_issue" {
			t.Fatalf("interceptor saw %v, want the qualified name", seen)
		}
	})

}

// builtinToolNames is the SDK's own tool set by name — what an embedder hands
// Pool.NativeTools so a server cannot stand in front of `read_file`
// (REQ-MCP-CLIENT-06).
func builtinToolNames(t *testing.T) []string {
	t.Helper()
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(all))
	for _, tl := range all {
		names = append(names, tl.Name)
	}
	return names
}
