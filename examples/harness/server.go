package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// toolServer holds the tools a harness may call — code_mode and the sandbox's
// proxy tools — and runs every call through the embedder's guard. It is the
// one piece of this example the library does not provide: AgentKit's driver
// runs a tool call through preparation, the guard, the handler and its
// events, but only for a call its own model made, and a harness is its own
// driver. So the server runs the same steps itself, and attaches itself as
// the core.NestedCaller a code_mode script's calls go through: outside an
// agent run, code mode has no other way to reach a tool.
//
// What the driver does that this does not: Config.After, execution events on
// a stream, and the terminate vote. A harness has its own loop to end.
type toolServer struct {
	tools   map[string]core.Tool
	order   []string
	guard   core.BeforeToolCall
	log     io.Writer
	seq     atomic.Int64
	calls   atomic.Int64
	blocked atomic.Int64
}

// newToolServer binds proxies into one code_mode tool and serves it beside
// them. guard sees every call, direct or from a script.
func newToolServer(proxies []core.Tool, guard core.BeforeToolCall, log io.Writer) (*toolServer, codemode.BuildInfo, error) {
	cm, info, err := codemode.New(proxies, codemode.Options{})
	if err != nil {
		return nil, info, err
	}
	s := &toolServer{tools: map[string]core.Tool{}, guard: guard, log: log}
	for _, t := range append([]core.Tool{cm}, proxies...) {
		s.tools[t.Name] = t
		s.order = append(s.order, t.Name)
	}
	return s, info, nil
}

// served returns the tools in registration order: code_mode first.
func (s *toolServer) served() []core.Tool {
	out := make([]core.Tool, 0, len(s.order))
	for _, n := range s.order {
		out = append(out, s.tools[n])
	}
	return out
}

// call runs one tool call as the driver would: the input is checked and
// prepared against the tool's schema, the guard decides, then the handler
// runs, with this server attached as the nested caller when the tool reaches
// other tools. parent names the code_mode call a nested call came from, or
// is empty. Every failure is a result, never a Go error.
func (s *toolServer) call(ctx context.Context, name string, input json.RawMessage, parentID, parentName string, reach map[string]core.Tool) (res core.ToolResult) {
	id := fmt.Sprintf("call_%d", s.seq.Add(1))
	s.calls.Add(1)
	indent := "  "
	if parentID != "" {
		indent = "    "
	}
	defer func() {
		if r := recover(); r != nil {
			res = core.ErrResult("panic", fmt.Sprintf("tool %q panicked: %v", name, r))
		}
		if !res.OK {
			detail := res.Detail
			if code, ok := res.Data["exit_code"]; ok && detail == "" {
				detail = fmt.Sprintf("exit %v", code)
			}
			fmt.Fprintf(s.log, "%s%-8s %s: %s\n", indent, "error", name, firstLine(res.Error+": "+detail))
		}
	}()
	if ctx.Err() != nil {
		return core.ErrResult("aborted", "Operation aborted")
	}
	tool, ok := reach[name]
	if !ok {
		return core.ErrResult("unknown_tool", fmt.Sprintf("%q is not a tool the harness may call", name))
	}
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	use, err := core.NewToolUse(id, name, input)
	if err != nil {
		return core.ErrResult("invalid_arguments", err.Error())
	}
	prepared, err := core.PrepareArguments(tool, use)
	if err != nil {
		return core.ErrResult("invalid_arguments", err.Error())
	}
	if s.guard != nil {
		dec := s.guard(ctx, core.BeforeToolCallContext{
			ToolName: name, ToolUseID: id, ParentToolUseID: parentID, ParentToolName: parentName,
			Tool: tool, Arguments: prepared.Args, RawInput: use.Input,
		})
		if dec.Block {
			s.blocked.Add(1)
			reason := dec.Reason
			if reason == "" {
				reason = "blocked by policy"
			}
			fmt.Fprintf(s.log, "%s%-8s %s: %s\n", indent, "blocked", name, firstLine(reason))
			return core.ErrResult(core.BlockErrorCode, reason)
		}
		if dec.Arguments != nil {
			if prepared, err = prepared.TryWithArgs(dec.Arguments); err != nil {
				return core.ErrResult("invalid_arguments", "the guard returned arguments that are not JSON: "+err.Error())
			}
		}
	}
	fmt.Fprintf(s.log, "%s%-8s %s %s\n", indent, "run", name, summarize(prepared.Args))
	hctx := ctx
	if len(tool.ReachableTools) > 0 {
		// A wrapper's scripts reach the tools it declares and no others:
		// code_mode cannot call code_mode.
		n := &nested{s: s, parentID: id, parentName: name, tools: map[string]core.Tool{}}
		for _, t := range tool.ReachableTools {
			n.tools[t.Name] = t
		}
		hctx = core.WithNestedCaller(ctx, n)
	}
	if tool.Execute != nil {
		return tool.Execute(hctx, prepared.Raw)
	}
	out, err := tool.Handler(hctx, prepared.Raw)
	if err != nil {
		return core.ErrResult("handler_error", err.Error())
	}
	var data map[string]any
	if len(out) > 0 {
		if err := json.Unmarshal(out, &data); err != nil {
			return core.ToolResult{OK: true, Data: map[string]any{"result": json.RawMessage(out)}}
		}
	}
	return core.OKResult(data)
}

// nested is the core.NestedCaller for one code_mode call: the script's tool
// functions arrive here through core.CallNested. Results come back in call
// order; a failed call is a result, as the driver's are. Handlers run
// concurrently unless one of the calls is to a Sequential tool, such as
// execute, in which case they run in order — the driver's rule.
type nested struct {
	s                    *toolServer
	parentID, parentName string
	tools                map[string]core.Tool
}

func (n *nested) Call(ctx context.Context, calls ...core.ToolUseBlock) ([]core.ToolResult, error) {
	results := make([]core.ToolResult, len(calls))
	sequential := len(calls) <= 1
	for _, c := range calls {
		if t, ok := n.tools[c.Name]; ok && t.ExecutionMode == core.Sequential {
			sequential = true
		}
	}
	if sequential {
		for i, c := range calls {
			results[i] = n.s.call(ctx, c.Name, c.Input, n.parentID, n.parentName, n.tools)
		}
		return results, nil
	}
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = n.s.call(ctx, c.Name, c.Input, n.parentID, n.parentName, n.tools)
		}()
	}
	wg.Wait()
	return results, nil
}

// direct runs a call the harness (or this program) makes by name.
func (s *toolServer) direct(ctx context.Context, name string, input json.RawMessage) core.ToolResult {
	return s.call(ctx, name, input, "", "", s.tools)
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
	for _, t := range s.served() {
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
				res := s.direct(ctx, name, req.Params.Arguments)
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
