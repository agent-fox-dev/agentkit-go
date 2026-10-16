package mcpserve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// selfPath is this module's path, the one buildinfo.go looks for.
const selfPath = "github.com/agent-fox-dev/agentkit-go/mcpserve"

// toolRunner is the part of an *agentkit.Runner a Server uses. It is an
// interface so that a test can count the calls to Tools and record what Call
// receives; New is the only constructor, and it takes the concrete type.
type toolRunner interface {
	Tools() []core.Tool
	Call(ctx context.Context, name string, input json.RawMessage) core.ToolResult
}

// A Server serves one runner's tools over MCP. It is immutable after New and
// safe for concurrent use: it may serve any number of loopback endpoints and
// one stdio session at the same time, all over the same tool set.
type Server struct {
	srv *sdk.Server
	run toolRunner
	log *slog.Logger
}

// New builds a Server over the tools of r. It reads r.Tools() once, and serves
// that set for the Server's whole lifetime.
//
// Each tool is served under its own name and description, with its input
// schema as json.Marshal renders it, or {"type":"object"} when it has none.
// New refuses a runner that holds a tool whose schema cannot be served (it does
// not marshal, or its top level is not an object with "type":"object"), naming
// the tool, so the SDK is never handed a tool it would reject.
func New(r *agentkit.Runner, opts Options) (*Server, error) {
	if r == nil {
		return nil, fmt.Errorf("mcpserve: Runner is nil")
	}
	return newServer(r, opts)
}

func newServer(r toolRunner, opts Options) (*Server, error) {
	ts := r.Tools()
	defs := make([]*sdk.Tool, 0, len(ts))
	for _, t := range ts {
		in, err := inputSchema(t)
		if err != nil {
			return nil, err
		}
		defs = append(defs, &sdk.Tool{Name: t.Name, Description: t.Description, InputSchema: in})
	}
	name, version, log := opts.Name, opts.Version, opts.Logger
	if name == "" {
		name = "agentkit"
	}
	if version == "" {
		version = moduleVersion()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &Server{run: r, log: log}
	s.srv = sdk.NewServer(&sdk.Implementation{Name: name, Version: version}, &sdk.ServerOptions{Instructions: opts.Instructions})
	for _, d := range defs {
		s.srv.AddTool(d, s.handler(d.Name))
	}
	return s, nil
}

// inputSchema is the schema t is served with: its JSON, or {"type":"object"}
// when it has none. A schema that does not marshal, or whose top level is not
// an object with "type":"object", is an error, because the SDK panics on it.
func inputSchema(t core.Tool) (json.RawMessage, error) {
	if t.InputSchema == nil {
		return json.RawMessage(`{"type":"object"}`), nil
	}
	b, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("mcpserve: tool %q: input schema: %w", t.Name, err)
	}
	var top map[string]any
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, fmt.Errorf("mcpserve: tool %q: input schema is not a JSON object", t.Name)
	}
	if top["type"] != "object" {
		return nil, fmt.Errorf(`mcpserve: tool %q: input schema must have "type":"object" at the top level`, t.Name)
	}
	return b, nil
}

// handler serves the tool called name.
func (s *Server) handler(name string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		res := s.run.Call(ctx, name, req.Params.Arguments)
		return &sdk.CallToolResult{
			Content: []sdk.Content{&sdk.TextContent{Text: res.LLMText()}},
			IsError: !res.OK,
		}, nil
	}
}
