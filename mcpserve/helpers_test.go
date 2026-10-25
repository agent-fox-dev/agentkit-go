package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

const (
	initFrame  = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"1"}}}`
	readyFrame = `{"jsonrpc":"2.0","method":"notifications/initialized"}`
)

// call is a tools/call frame with id and the literal params.
func call(id, params string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":` + params + `}`
}

// must is v; a setup step that fails panics, which fails the test.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// tool is a tool with an optional string "text" argument that runs run, or answers OK.
func tool(name string, run func(context.Context, json.RawMessage) core.ToolResult) core.Tool {
	if run == nil {
		run = func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }
	}
	return core.Tool{Name: name, Description: "the " + name + " tool", InputSchema: schema.Object(schema.Opt("text", schema.String())), Execute: run}
}

// echoTool answers with its arguments.
func echoTool() core.Tool {
	return tool("echo", func(_ context.Context, in json.RawMessage) core.ToolResult {
		return core.OKResult(map[string]any{"echo": string(in)})
	})
}

// blockingTool signals started, then answers aborted once its context ends,
// reporting the context's error on observed.
func blockingTool(started chan<- struct{}, observed chan<- error) core.Tool {
	return tool("block", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		started <- struct{}{}
		<-ctx.Done()
		observed <- ctx.Err()
		return core.ErrResult("aborted", "Operation aborted")
	})
}

func runnerOf(ts ...core.Tool) *agentkit.Runner {
	return must(agentkit.NewRunner(agentkit.Toolset{Tools: ts}))
}

// recorder wraps a toolRunner and keeps what each Call received and answered.
type recorder struct {
	toolRunner
	mu     sync.Mutex
	inputs []string
	res    []core.ToolResult
}

func (r *recorder) Call(ctx context.Context, name string, input json.RawMessage) core.ToolResult {
	res := r.toolRunner.Call(ctx, name, input)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs, r.res = append(r.inputs, string(input)), append(r.res, res)
	return res
}

func (r *recorder) results() []core.ToolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.res)
}

// serverOf is a Server over a recorded runner of ts, of echo when there are none.
func serverOf(t *testing.T, opts Options, ts ...core.Tool) (*Server, *recorder) {
	if len(ts) == 0 {
		ts = []core.Tool{echoTool()}
	}
	rec := &recorder{toolRunner: runnerOf(ts...)}
	return must(newServer(rec, opts)), rec
}

// capture is an slog.Handler that keeps each record as "LEVEL message key=value ...".
type capture struct {
	mu    sync.Mutex
	lines []string
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capture) WithGroup(string) slog.Handler            { return c }
func (c *capture) Handle(_ context.Context, r slog.Record) error {
	line := r.Level.String() + " " + r.Message
	r.Attrs(func(a slog.Attr) bool { line += fmt.Sprintf(" %s=%v", a.Key, a.Value); return true })
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
	return nil
}

func (c *capture) text() string { c.mu.Lock(); defer c.mu.Unlock(); return strings.Join(c.lines, "\n") }

// stdioClient is the far end of os.Stdin and os.Stdout while s.ServeStdio runs.
type stdioClient struct {
	in, out *os.File
	lines   *bufio.Scanner
	done    chan error
}

// serveStdio points os.Stdin and os.Stdout at pipes for the test and runs
// s.ServeStdio(ctx) on them.
func serveStdio(t *testing.T, ctx context.Context, s *Server) *stdioClient {
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = inR, outW
	t.Cleanup(func() {
		os.Stdin, os.Stdout = oldIn, oldOut
		for _, f := range []*os.File{inR, inW, outR, outW} {
			_ = f.Close()
		}
	})
	c := &stdioClient{in: inW, out: outR, lines: bufio.NewScanner(outR), done: make(chan error, 1)}
	c.lines.Buffer(nil, 1<<24)
	go func() { c.done <- s.ServeStdio(ctx) }()
	return c
}

// send writes frames, one a line, from a goroutine, since a server that ends
// the session stops reading.
func (c *stdioClient) send(frames ...string) {
	go func() {
		for _, f := range frames {
			if _, err := io.WriteString(c.in, f+"\n"); err != nil {
				return
			}
		}
	}()
}

// conn connects AgentKit's MCP client to the server behind c.
func (c *stdioClient) conn(t *testing.T) *agentmcp.ServerConnection {
	conn := must(agentmcp.Connect(context.Background(), agentmcp.ServerConfig{Name: "client", Timeout: 5 * time.Second},
		agentmcp.NewPipeTransport(c.out, c.in, wire.Defaults()), agentmcp.ConnectionOptions{}))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// serveLoop serves s on a loopback endpoint that closes with the test.
func serveLoop(t *testing.T, s *Server) *Endpoint {
	ep := must(s.ServeLoopback(context.Background()))
	t.Cleanup(func() { _ = ep.Close() })
	return ep
}

// dial connects AgentKit's MCP pool to ep and returns the tools it imports.
func dial(t *testing.T, ep *Endpoint) []core.Tool {
	pool := agentmcp.NewPool(agentmcp.ConnectionOptions{})
	t.Cleanup(func() { _ = pool.Close() })
	must(pool.Connect(context.Background(), agentmcp.ServerConfig{Name: "agentkit", URL: ep.URL, Timeout: 10 * time.Second,
		Headers: map[string]string{"Authorization": "Bearer " + ep.Token}}, nil, nil))
	return must(pool.Tools(context.Background(), nil))
}

var httpClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

// post sends body to url with the Authorization header auth ("" for none) and
// the extra headers given as name, value pairs; a failed exchange is a zero response.
func post(url, auth, body string, headers ...string) *http.Response {
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	headers = append(headers, "Authorization", auth, "Content-Type", "application/json", "Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] != "" {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return &http.Response{}
	}
	_ = res.Body.Close()
	return res
}

// refused reports whether nothing accepts connections at the port of url.
func refused(url string) bool {
	c, err := net.DialTimeout("tcp", strings.TrimSuffix(strings.TrimPrefix(url, "http://"), endpointPath), time.Second)
	if err == nil {
		_ = c.Close()
	}
	return err != nil
}
