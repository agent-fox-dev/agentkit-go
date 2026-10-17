package mcpserve

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/wire"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// dial connects AgentKit's own MCP client to an endpoint, as a harness's
// embedder would, and closes the connection with the test.
func dial(t *testing.T, ep *Endpoint) *agentmcp.Pool {
	t.Helper()
	pool := agentmcp.NewPool(agentmcp.ConnectionOptions{})
	cfg := agentmcp.ServerConfig{Name: "agentkit", URL: ep.URL, Timeout: 10 * time.Second,
		Headers: map[string]string{"Authorization": "Bearer " + ep.Token}}
	if _, err := pool.Connect(context.Background(), cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

func toolNames(ts []core.Tool) []string {
	var out []string
	for _, tl := range ts {
		out = append(out, tl.Name)
	}
	slices.Sort(out)
	return out
}

// refused reports whether nothing accepts connections at the endpoint's port.
func refused(ep *Endpoint) bool {
	host := strings.TrimSuffix(strings.TrimPrefix(ep.URL, "http://"), "/mcp")
	c, err := net.DialTimeout("tcp", host, time.Second)
	if err == nil {
		_ = c.Close()
	}
	return err != nil
}

// TS-23-12 (integration): one Server serves two loopback endpoints and a stdio
// session at once, over the same tools. Run with -race. 23-REQ-2.8.
func TestOneServerManySessions_TS23_12(t *testing.T) {
	toServer, fromServer := stdio(t)
	s, _ := echoServer(t, Options{})
	a, b := serveLoop(t, s), serveLoop(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serving(ctx, s)
	pa, pb := dial(t, a), dial(t, b)
	conn, err := agentmcp.Connect(ctx, agentmcp.ServerConfig{Name: "agentkit", Timeout: 5 * time.Second},
		agentmcp.NewPipeTransport(fromServer, toServer, wire.Defaults()), agentmcp.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Each client lists the tools and calls the first; all three run at once.
	over := func(p *agentmcp.Pool) func() ([]string, bool) {
		return func() ([]string, bool) {
			imported, err := p.Tools(ctx, nil)
			if err != nil || len(imported) == 0 {
				return nil, false
			}
			var names []string
			for _, n := range toolNames(imported) {
				names = append(names, strings.TrimPrefix(n, "agentkit__"))
			}
			return names, imported[0].Execute(ctx, json.RawMessage(`{"text":"hi"}`)).OK
		}
	}
	overStdio := func() ([]string, bool) {
		defs, err := conn.ListTools(ctx)
		if err != nil {
			return nil, false
		}
		var names []string
		for _, d := range defs {
			names = append(names, d.Name)
		}
		out, err := conn.CallRaw(ctx, "echo", json.RawMessage(`{"text":"hi"}`))
		return names, err == nil && !out.IsError
	}
	clients := []func() ([]string, bool){over(pa), over(pb), overStdio}
	names, oks := make([][]string, len(clients)), make([]bool, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			names[i], oks[i] = c()
		}()
	}
	wg.Wait()
	for i := range clients {
		if !oks[i] || !slices.Equal(names[i], []string{"echo"}) {
			t.Errorf("client %d listed %v and its call ok was %v; want [echo] and true", i, names[i], oks[i])
		}
	}
	_ = conn.Close()
	if err := within(t, 5*time.Second, done, "ServeStdio after the client left"); err != nil {
		t.Errorf("ServeStdio returned %v", err)
	}
}

// TS-23-19 (integration): AgentKit's mcp.Pool, over loopback HTTP with the
// bearer token, runs code_mode, and the script's nested read_file call also
// passes the runner's guard. 23-REQ-3.7.
func TestPoolRunsCodeModeThroughTheGuard_TS23_19(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	const content = "the file's known content"
	if err := os.WriteFile(filepath.Join(ws.Root(), "known.txt"), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cm, _, err := codemode.New(all, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []string
	r, err := agentkit.NewRunner(agentkit.Toolset{Tools: append([]core.Tool{cm}, all...),
		Guard: func(ctx context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			mu.Lock()
			seen = append(seen, in.ToolName)
			mu.Unlock()
			return guard.AllowAll(ctx, in)
		}})
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ep := serveLoop(t, s)
	imported, err := dial(t, ep).Tools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var code core.Tool
	for _, tl := range imported {
		if tl.Name == "agentkit__code_mode" {
			code = tl
		}
	}
	if code.Execute == nil {
		t.Fatalf("code_mode is not among the imported tools %v", toolNames(imported))
	}
	script, _ := json.Marshal(map[string]string{"script": "f = read_file(path=\"known.txt\")\nprint(f[\"content\"].strip())\n"})
	res := code.Execute(context.Background(), script)
	if !res.OK || !strings.Contains(res.LLMText(), content) {
		t.Errorf("code_mode = %+v, want OK and the file's content", res)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(seen, "code_mode") || !slices.Contains(seen, "read_file") {
		t.Errorf("the guard saw %v, want both the code_mode call and the nested read_file call", seen)
	}
}

// openSSE starts a session on ep and a standalone event stream that stays open,
// as a client does. It returns once the stream's headers are in.
func openSSE(t *testing.T, ep *Endpoint) {
	t.Helper()
	auth := "Bearer " + ep.Token
	r := send(t, http.MethodPost, ep.URL, auth, strings.NewReader(initFrame))
	session := r.header.Get("Mcp-Session-Id")
	if session == "" {
		t.Fatalf("initialize gave no session: %d %s", r.status, r.body)
	}
	req, _ := http.NewRequest(http.MethodPost, ep.URL, strings.NewReader(readyFrame))
	for k, v := range map[string]string{"Authorization": auth, "Content-Type": "application/json", "Mcp-Session-Id": session,
		"Accept": "application/json, text/event-stream"} {
		req.Header.Set(k, v)
	}
	if res, err := httpClient.Do(req); err == nil {
		_ = res.Body.Close()
	}
	get, _ := http.NewRequest(http.MethodGet, ep.URL, nil)
	for k, v := range map[string]string{"Authorization": auth, "Mcp-Session-Id": session, "Accept": "text/event-stream"} {
		get.Header.Set(k, v)
	}
	res, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(get)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("the standalone stream: %v, %v", res, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
}

// TS-23-35 (integration): Close and the end of ctx each stop the endpoint
// within 3 s even with a standalone event stream open. 23-REQ-6.1.
func TestShutdownIsBounded_TS23_35(t *testing.T) {
	s, _ := echoServer(t, Options{})

	closed, err := s.ServeLoopback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	openSSE(t, closed)
	start := time.Now()
	if err := closed.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Close took %v with a stream open, want under 3s", took)
	}
	if !refused(closed) {
		t.Error("the port still accepts connections after Close")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended, err := s.ServeLoopback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	openSSE(t, ended)
	cancel()
	deadline := time.Now().Add(3 * time.Second)
	for !refused(ended) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !refused(ended) {
		t.Error("the endpoint still accepts connections 3s after its ctx ended")
	}
}

// TS-23-36 (integration): force-closing the endpoint cancels a call in flight.
// 23-REQ-6.2.
func TestForceCloseCancelsInFlightCalls_TS23_36(t *testing.T) {
	started, observed := make(chan struct{}, 1), make(chan error, 1)
	rec := &recorder{toolRunner: runnerOf(t, blockingTool(started, observed))}
	s, err := newServer(rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ep, err := s.ServeLoopback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	imported, err := dial(t, ep).Tools(context.Background(), nil)
	if err != nil || len(imported) != 1 {
		t.Fatalf("Tools = %v, %v", imported, err)
	}
	go imported[0].Execute(context.Background(), json.RawMessage(`{}`))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never started")
	}

	start := time.Now()
	_ = ep.Close()
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Close took %v with a call in flight, want under 3s", took)
	}
	select {
	case err := <-observed:
		if err == nil {
			t.Error("the tool saw no context error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call in flight was never cancelled by the force-close")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(rec.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls := rec.seen(); len(calls) != 1 || calls[0].res.Error != "aborted" {
		t.Errorf("the runner's result is %+v, want aborted", calls)
	}
}

// TS-23-37: Close is idempotent and safe from many goroutines. Run with -race.
// 23-REQ-6.3.
func TestCloseIsIdempotentAndConcurrent_TS23_37(t *testing.T) {
	s, _ := echoServer(t, Options{})
	ep, err := s.ServeLoopback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 16)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = ep.Close()
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Close %d returned %v", i, err)
		}
	}
	if err := ep.Close(); err != nil {
		t.Errorf("a later Close returned %v", err)
	}
	if !refused(ep) {
		t.Error("the port still accepts connections")
	}
}

// TS-23-38: Close on an Endpoint that ServeLoopback did not return does nothing
// and returns nil. 23-REQ-6.4.
func TestCloseOnAForeignEndpoint_TS23_38(t *testing.T) {
	var zero Endpoint
	var nilEndpoint *Endpoint
	for name, ep := range map[string]*Endpoint{"zero": &zero, "literal": {URL: "x", Token: "y"}, "nil": nilEndpoint} {
		if err := ep.Close(); err != nil {
			t.Errorf("%s: Close returned %v", name, err)
		}
	}
}

// TS-23-39 (integration): two ServeLoopback calls on one Server give
// independent endpoints. 23-REQ-6.5.
func TestEndpointsAreIndependent_TS23_39(t *testing.T) {
	s, _ := echoServer(t, Options{})
	a, b := serveLoop(t, s), serveLoop(t, s)
	if a.URL == b.URL || a.Token == b.Token {
		t.Fatalf("the endpoints share a port or a token: %s %s", a.URL, b.URL)
	}
	if r := send(t, http.MethodPost, b.URL, "Bearer "+a.Token, strings.NewReader(initFrame)); r.status != http.StatusUnauthorized {
		t.Errorf("A's token at B: %d, want 401", r.status)
	}
	_ = b.Close()
	if r := send(t, http.MethodPost, a.URL, "Bearer "+a.Token, strings.NewReader(initFrame)); r.status != http.StatusOK {
		t.Errorf("A after B closed: %d, want 200", r.status)
	}
}
