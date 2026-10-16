package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	agentkit "github.com/agent-fox-dev/agentkit-go"
)

// toolServer serves a runner's tools to a harness over MCP. Every call a
// harness makes arrives at the runner, which runs it as AgentKit's driver runs
// a call its own model made: preparation, the guard, the handler with a nested
// caller attached when the tool reaches other tools — which is what lets a
// code_mode script call the tools it binds — and the toolset's hooks. The
// server adds only the protocol.
type toolServer struct {
	runner *agentkit.Runner
}

// mcpServer is the MCP server: one MCP tool per served tool, with the tool's
// own name, description and input schema, so the harness's model reads the
// same definition AgentKit's driver would send. A result's text is what the
// model would read in AgentKit (core.ToolResult.LLMText), and a failed call
// is an MCP error result, not a protocol error.
func (s *toolServer) mcpServer() (*sdk.Server, error) {
	srv := sdk.NewServer(&sdk.Implementation{Name: "agentkit", Version: "example"}, &sdk.ServerOptions{
		Instructions: "The tools run in a sandbox that holds the workspace. code_mode runs a Starlark script over the other tools in one call.",
	})
	for _, t := range s.runner.Tools() {
		in := json.RawMessage(`{"type":"object"}`)
		if t.InputSchema != nil {
			b, err := json.Marshal(t.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("%s: input schema: %w", t.Name, err)
			}
			in = b
		}
		name := t.Name
		srv.AddTool(&sdk.Tool{Name: name, Description: t.Description, InputSchema: in},
			func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				res := s.runner.Call(ctx, name, req.Params.Arguments)
				return &sdk.CallToolResult{
					Content: []sdk.Content{&sdk.TextContent{Text: res.LLMText()}},
					IsError: !res.OK,
				}, nil
			})
	}
	return srv, nil
}

// servedMCP is a running MCP server on a loopback port.
type servedMCP struct {
	URL   string // the Streamable HTTP endpoint
	Token string // the bearer token every request must carry
	stop  func()
}

// serve starts the MCP server over Streamable HTTP on a free loopback port,
// behind a random bearer token: a harness on this machine is the only
// intended client, and the token keeps any other local process from using
// the sandbox's tools under this program's policy. The returned stop closes
// the listener; a harness still connected gets a closed connection.
func (s *toolServer) serve(ctx context.Context) (*servedMCP, error) {
	srv, err := s.mcpServer()
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(raw)
	want := []byte("Bearer " + token)
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, nil)
	auth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	hs := &http.Server{Handler: auth, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = hs.Serve(l) }()
	stop := func() {
		// A client's standalone SSE stream stays open until the client
		// leaves, so a graceful shutdown is bounded and then forced.
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := hs.Shutdown(sctx); err != nil {
			_ = hs.Close()
		}
	}
	return &servedMCP{URL: "http://" + l.Addr().String() + "/mcp", Token: token, stop: stop}, nil
}

// mcpConfig is the harness-side configuration of this server, in the JSON
// shape Claude Code and Pi read: one server named agentkit, over Streamable
// HTTP, with the bearer token in a header. pi, when set, adds Pi's exposure
// field so the tools are declared to the model directly rather than only
// through Pi's own code mode.
func (m *servedMCP) mcpConfig(pi bool) string {
	server := map[string]any{
		"type":    "http",
		"url":     m.URL,
		"headers": map[string]string{"Authorization": "Bearer " + m.Token},
	}
	if pi {
		server["exposure"] = "direct"
	}
	b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"agentkit": server}})
	return string(b)
}

// errShellBlocked is what a check whose command the guard refused reports.
var errShellBlocked = errors.New("the guard refused the command")

// summarize renders tool arguments as one short line, keys sorted.
func summarize(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncate(firstLine(fmt.Sprint(args[k])), 60)))
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return truncate(s, 120)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
