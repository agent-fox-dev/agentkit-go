package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newServer is the SDK's server, the peer every client test talks to.
func newServer(name string) *sdk.Server {
	return sdk.NewServer(&sdk.Implementation{Name: name, Version: "1"}, &sdk.ServerOptions{
		Capabilities: &sdk.ServerCapabilities{Tools: &sdk.ToolCapabilities{ListChanged: true}},
	})
}

// handle registers a tool whose handler gets its arguments decoded with
// json.Number; a returned error becomes a tool error.
func handle(s *sdk.Server, def *Tool, h func(args map[string]any) (*CallToolResult, error)) {
	d := *def
	if d.InputSchema == nil {
		d.InputSchema = json.RawMessage(`{"type":"object"}`)
	}
	s.AddTool(&d, func(_ context.Context, req *sdk.CallToolRequest) (*CallToolResult, error) {
		var args map[string]any
		if raw := req.Params.Arguments; len(raw) > 0 {
			dec := json.NewDecoder(bytes.NewReader(raw))
			dec.UseNumber()
			_ = dec.Decode(&args)
		}
		res, err := h(args)
		if err != nil {
			return &CallToolResult{IsError: true, Content: []Content{&TextContent{Text: err.Error()}}}, nil
		}
		if res == nil {
			res = &CallToolResult{}
		}
		return res, nil
	})
}

func text(s string) *CallToolResult {
	return &CallToolResult{Content: []Content{&TextContent{Text: s}}}
}

// echoServer exposes `echo` (a required string message) and `boom` (always a
// tool error).
func echoServer() *sdk.Server {
	s := newServer("test-server")
	handle(s, &Tool{Name: "echo", Description: "echo the message back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
	}, func(args map[string]any) (*CallToolResult, error) {
		msg, _ := args["message"].(string)
		return text("echo: " + msg), nil
	})
	handle(s, &Tool{Name: "boom"}, func(map[string]any) (*CallToolResult, error) {
		return nil, fmt.Errorf("the tool refused")
	})
	return s
}

// pair connects cfg to srv over two in-memory pipes, strict NDJSON on both
// ends: no subprocess, no port.
func pair(t *testing.T, srv *sdk.Server, cfg ServerConfig, opts ConnectionOptions) (*ServerConnection, *sdk.ServerSession) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	ss, err := srv.Connect(context.Background(), NewPipeTransport(c2sR, s2cW, wire.Limits{}), nil)
	must(t, err)
	conn, err := Connect(context.Background(), cfg, NewPipeTransport(s2cR, c2sW, wire.Limits{}), opts)
	must(t, err)
	t.Cleanup(func() { _ = conn.Close(); _ = ss.Close() })
	return conn, ss
}

// poolOf is a pool holding one in-memory connection per config, each to its
// own echoServer unless srv is given.
func poolOf(t *testing.T, srv *sdk.Server, cfgs ...ServerConfig) *Pool {
	t.Helper()
	p := NewPool(ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	for _, cfg := range cfgs {
		s := srv
		if s == nil {
			s = echoServer()
		}
		conn, _ := pair(t, s, cfg, ConnectionOptions{})
		must(t, p.Add(conn))
	}
	return p
}

// toolsOf adapts the pool's tools and indexes them by qualified name.
func toolsOf(t *testing.T, p *Pool) map[string]core.Tool {
	t.Helper()
	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	out := map[string]core.Tool{}
	for _, tl := range tools {
		out[tl.Name] = tl
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func firstText(t *testing.T, res *CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("result has no content: %+v", res)
	}
	tc, ok := res.Content[0].(*TextContent)
	if !ok {
		t.Fatalf("first content block is %T, want text", res.Content[0])
	}
	return tc.Text
}

// remote serves srv over Streamable HTTP and records the headers of the first
// request. A request carrying key neither
// as a bearer token nor as X-API-Key is refused with 401.
func remote(t *testing.T, srv *sdk.Server, key string, got *http.Header) *httptest.Server {
	t.Helper()
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv },
		&sdk.StreamableHTTPOptions{Stateless: true})
	var once sync.Once
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			once.Do(func() { *got = r.Header.Clone() })
		}
		if r.Header.Get("Authorization") != "Bearer "+key && r.Header.Get("X-API-Key") != key {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// warnings collects what a pool reports through Warnf.
type warnings struct {
	mu   sync.Mutex
	list []string
}

func (w *warnings) warnf(f string, a ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.list = append(w.list, fmt.Sprintf(f, a...))
}

func (w *warnings) has(sub string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, s := range w.list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// spawn connects this test binary, re-executed as an MCP server in the given
// mode, through p. env is the child's whole environment beyond the mode.
func spawn(t *testing.T, p *Pool, mode string, cfg ServerConfig, env []string, secrets func(string) string) (*ServerConnection, error) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	cfg.Command = exe
	return p.Connect(context.Background(), cfg, append([]string{"AGENTKIT_MCP_CHILD=" + mode}, env...), secrets)
}

// TestMain is where this test binary becomes an MCP server when re-executed as
// a subprocess: "serve" answers hello, env, die (answer, then exit) and crash
// (exit without answering); "one-shot" answers the first request and exits.
func TestMain(m *testing.M) {
	switch os.Getenv("AGENTKIT_MCP_CHILD") {
	case "serve":
		serveChild()
		return
	case "one-shot":
		line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
		var req struct{ ID json.RawMessage }
		_ = json.Unmarshal(line, &req)
		_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"supportedVersions":["2026-07-28"],"capabilities":{}}}`+"\n", req.ID)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func serveChild() {
	out := &exitAfterWrite{w: os.Stdout}
	s := newServer("child")
	handle(s, &Tool{Name: "hello"}, func(map[string]any) (*CallToolResult, error) { return text("hi"), nil })
	handle(s, &Tool{Name: "env"}, func(map[string]any) (*CallToolResult, error) {
		return text(strings.Join(os.Environ(), "\n")), nil
	})
	handle(s, &Tool{Name: "die"}, func(map[string]any) (*CallToolResult, error) {
		out.armed.Store(true)
		return text("bye"), nil
	})
	handle(s, &Tool{Name: "crash"}, func(map[string]any) (*CallToolResult, error) {
		os.Exit(1)
		return nil, nil
	})
	_ = s.Run(context.Background(), NewPipeTransport(os.Stdin, out, wire.Limits{}))
}

// exitAfterWrite exits the process right after the first write that follows
// arming, so a response is fully written before the server is gone.
type exitAfterWrite struct {
	w     io.Writer
	armed atomic.Bool
}

func (e *exitAfterWrite) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if e.armed.Load() {
		os.Exit(0)
	}
	return n, err
}
