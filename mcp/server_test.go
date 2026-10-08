package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/mcp"
	"github.com/agentfox/agentkit-go/wire"
)

// ---- REQ-MCP-SERVER-07

func TestHTTPModeRequiresAnAPIKey(t *testing.T) {
	if _, err := echoServer(t).HTTPHandler(mcp.HTTPOptions{}); !errors.Is(err, mcp.ErrNoAPIKey) {
		t.Fatalf("err = %v; a server that starts unauthenticated because a config key was "+
			"missing is exactly what REQ-MCP-SERVER-07 exists to prevent", err)
	}
}

const listTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

func post(t *testing.T, url, body string, header ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func httpServer(t *testing.T, s *mcp.Server, opts mcp.HTTPOptions) *httptest.Server {
	t.Helper()
	h, err := s.HTTPHandler(opts)
	must(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestUnauthenticatedHTTPRequestsGet401(t *testing.T) {
	srv := httpServer(t, echoServer(t), mcp.HTTPOptions{APIKey: "sekret"})
	for _, tc := range []struct {
		name   string
		header []string
		ok     bool
	}{
		{"no credential", nil, false},
		{"wrong key", []string{"X-API-Key", "nope"}, false},
		{"bearer", []string{"Authorization", "Bearer sekret"}, true},
		{"raw api key header", []string{"X-API-Key", "sekret"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := post(t, srv.URL, listTools, tc.header...)
			if got := resp.StatusCode != http.StatusUnauthorized; got != tc.ok {
				t.Fatalf("status = %d; authenticated = %v, want %v", resp.StatusCode, got, tc.ok)
			}
		})
	}
}

// TestABrowserOriginIsRefused is the DNS-rebinding defence: a page on any
// origin can POST to a localhost server, and a native MCP client never sends
// an Origin at all.
func TestABrowserOriginIsRefused(t *testing.T) {
	srv := httpServer(t, echoServer(t), mcp.HTTPOptions{APIKey: "k"})
	resp, _ := post(t, srv.URL, listTools, "X-API-Key", "k", "Origin", "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an origin not on the allowlist", resp.StatusCode)
	}
}

// TestAuthenticationRunsBeforeTheMethodCheck: answering 405 to an
// unauthenticated caller tells them which verbs exist.
func TestAuthenticationRunsBeforeTheMethodCheck(t *testing.T) {
	srv := httpServer(t, echoServer(t), mcp.HTTPOptions{APIKey: "k"})
	resp, err := srv.Client().Get(srv.URL)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated GET returned %d; it must be 401", resp.StatusCode)
	}
}

func TestTheHTTPBodyIsBounded(t *testing.T) {
	srv := httpServer(t, echoServer(t), mcp.HTTPOptions{APIKey: "k", MaxBodyBytes: 256})
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` +
		strings.Repeat("x", 4096) + `"}}`
	if _, out := post(t, srv.URL, big, "X-API-Key", "k"); !strings.Contains(out, "error") {
		t.Fatalf("an oversized body must be refused: %s", out)
	}
}

// TestTheHTTPBodyCapHonoursTheSmallerWireLimit. A host with a 256-byte message
// bound must not read 16 MiB bodies into memory before refusing them: the cap
// is the smaller of the two, and an oversized body is refused at the reader
// (-32600), never parsed.
func TestTheHTTPBodyCapHonoursTheSmallerWireLimit(t *testing.T) {
	s := mcp.NewServer(mcp.ServerOptions{Limits: wire.Limits{MaxMessageBytes: 256}})
	srv := httpServer(t, s, mcp.HTTPOptions{APIKey: "k"})
	big := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` +
		strings.Repeat("x", 4096) + `"}}`
	_, out := post(t, srv.URL, big, "X-API-Key", "k")
	var m struct{ Error struct{ Code int } }
	must(t, json.Unmarshal([]byte(out), &m))
	if m.Error.Code != -32600 {
		t.Fatalf("response = %s; want the body refused by the reader (-32600)", out)
	}
}

// TestADuplicateKeyBodyIsRefused is REQ-SEC-11.3 on the inbound HTTP surface:
// the SDK alone would take the last of two `name`s, and the peer would choose
// which tool runs.
func TestADuplicateKeyBodyIsRefused(t *testing.T) {
	srv := httpServer(t, echoServer(t), mcp.HTTPOptions{APIKey: "k"})
	resp, out := post(t, srv.URL,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","name":"boom"}}`,
		"X-API-Key", "k")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(out, "-32700") {
		t.Fatalf("status %d, body %s; want a parse error before the SDK sees the body",
			resp.StatusCode, out)
	}
}

// ---- concurrency

// TestStdioServeBoundsConcurrentHandlers. Without a cap the client decides how
// many handlers this process runs at once.
func TestStdioServeBoundsConcurrentHandlers(t *testing.T) {
	s := mcp.NewServer(mcp.ServerOptions{})
	var running, started atomic.Int32
	release := make(chan struct{})
	must(t, s.RegisterTool(&mcp.Tool{Name: "block"},
		func(ctx context.Context, _ map[string]any) (*mcp.CallToolResult, error) {
			started.Add(1)
			running.Add(1)
			defer running.Add(-1)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil, nil
		}))
	conn := pair(t, s, mcp.ServerConfig{Name: "s"}, mcp.ConnectionOptions{})

	const n = mcp.MaxConcurrentHandlers + 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := conn.Call(context.Background(), "block", nil); err != nil {
				errs <- err
			}
		}()
	}

	waitFor(t, "the cap to fill", func() bool { return running.Load() >= mcp.MaxConcurrentHandlers })
	time.Sleep(50 * time.Millisecond) // long enough for a 65th to have started if it were going to
	if got := started.Load(); got != mcp.MaxConcurrentHandlers {
		t.Fatalf("%d handlers were started; the cap is %d", got, mcp.MaxConcurrentHandlers)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("request failed: %v", err)
	}
}

// TestCacheScopeDefaultsToPrivate. A tool list can encode which tools THIS
// caller may see; `cacheScope: public` would tell shared intermediaries they
// may serve it to anybody.
func TestCacheScopeDefaultsToPrivate(t *testing.T) {
	conn := pair(t, echoServer(t), mcp.ServerConfig{Name: "s"}, mcp.ConnectionOptions{})
	sess, err := conn.Session(context.Background())
	must(t, err)
	res, err := sess.ListTools(context.Background(), nil)
	must(t, err)
	if res.CacheScope != "private" {
		t.Fatalf("cacheScope = %q, want private", res.CacheScope)
	}
}

// ---- Run (REQ-MCP-SERVER-01/02)

// TestRunIsANoOpWhenServerModeIsDisabled. REQ-MCP-SERVER-01 says off by
// default, so "not enabled" is a successful no-op.
func TestRunIsANoOpWhenServerModeIsDisabled(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- mcp.NewServer(mcp.ServerOptions{}).Run(context.Background(),
			mcp.ServerModeConfig{Enabled: false, Transport: "http", Port: 1}, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a disabled config must return nil without listening; got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a disabled config must not start a listener")
	}
}

// TestHTTPModeRefusesToStartWithoutAKey is REQ-MCP-SERVER-07 at the runner.
// The dangerous case is api_key_env NAMED but unset: the config looks
// authenticated, so nobody re-reads it.
func TestHTTPModeRefusesToStartWithoutAKey(t *testing.T) {
	s := mcp.NewServer(mcp.ServerOptions{})
	// Already cancelled, so a build that WRONGLY got past the guard shuts its
	// listener down instead of parking the test on an open port.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	port := freePort(t)
	for _, tc := range []struct {
		name string
		cfg  mcp.ServerModeConfig
		env  func(string) string
		want string
	}{
		{"no api_key_env at all",
			mcp.ServerModeConfig{Enabled: true, Transport: "http", Port: port}, nil, "api_key_env"},
		{"api_key_env names an unset variable",
			mcp.ServerModeConfig{Enabled: true, Transport: "http", Port: port, APIKeyEnv: "MCP_KEY"},
			func(string) string { return "" }, "MCP_KEY"},
	} {
		err := s.Run(ctx, tc.cfg, tc.env)
		if !errors.Is(err, mcp.ErrNoAPIKey) {
			t.Fatalf("%s: want ErrNoAPIKey, got %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: the refusal must name %q; got %q", tc.name, tc.want, err)
		}
	}
}

// TestRunRejectsAnUnknownTransport. Falling back to stdio would leave a host
// that meant to listen on a port silently speaking to a closed stdin.
func TestRunRejectsAnUnknownTransport(t *testing.T) {
	err := mcp.NewServer(mcp.ServerOptions{}).Run(context.Background(),
		mcp.ServerModeConfig{Enabled: true, Transport: "websocket"}, nil)
	if err == nil || !strings.Contains(err.Error(), "websocket") {
		t.Fatalf("an unknown transport must be refused by name; got %v", err)
	}
}

// TestHTTPModeBindsLoopback. A default that binds every interface turns a
// config file's `port = 8080` into an internet-facing service.
func TestHTTPModeBindsLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	port := freePort(t)
	done := make(chan error, 1)
	go func() {
		done <- echoServer(t).Run(ctx, mcp.ServerModeConfig{
			Enabled: true, Transport: "http", Port: port, APIKeyEnv: "MCP_KEY",
		}, func(string) string { return "sekrit" })
	}()

	waitForPort(t, fmt.Sprintf("127.0.0.1:%d", port))
	if dialable(fmt.Sprintf("%s:%d", nonLoopbackHost(t), port)) {
		t.Fatal("http mode must bind loopback only")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a shutdown we asked for is not a failure; got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling the context must stop the listener")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	must(t, l.Close())
	return port
}

func waitForPort(t *testing.T, addr string) {
	t.Helper()
	waitFor(t, "a listener on "+addr, func() bool { return dialable(addr) })
}

func dialable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// nonLoopbackHost is this machine's first routable address, or a skip.
func nonLoopbackHost(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	must(t, err)
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	t.Skip("no non-loopback IPv4 interface; nothing to bind wrongly")
	return ""
}
