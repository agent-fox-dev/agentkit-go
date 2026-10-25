package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAConnectionDiscoversAndCallsTheServersTools(t *testing.T) {
	conn, _ := pair(t, echoServer(), ServerConfig{Name: "test"}, ConnectionOptions{})
	ctx := context.Background()

	sess, err := conn.Session(ctx)
	must(t, err)
	if info := sess.InitializeResult().ServerInfo; info == nil || info.Name != "test-server" {
		t.Fatalf("server identity = %+v", info)
	}
	if conn.Name() != "test" || !conn.Alive() || conn.Dead() || conn.Reconnects() != 0 {
		t.Fatalf("name %q alive %v dead %v reconnects %d", conn.Name(), conn.Alive(), conn.Dead(), conn.Reconnects())
	}
	tools, err := conn.ListTools(ctx)
	must(t, err)
	if len(tools) != 2 {
		t.Fatalf("%d tools, want 2", len(tools))
	}
	res, err := conn.Call(ctx, "echo", map[string]any{"message": "hello"})
	must(t, err)
	if got := firstText(t, res); got != "echo: hello" {
		t.Fatalf("result = %q", got)
	}
}

// A tool that failed is a result the model sees; a call that never happened
// (an unknown tool, arguments that cannot be encoded) is an error.
func TestAFailingToolIsAnErrorResultAndAnImpossibleCallIsAnError(t *testing.T) {
	conn, _ := pair(t, echoServer(), ServerConfig{Name: "test"}, ConnectionOptions{})
	ctx := context.Background()

	res, err := conn.Call(ctx, "boom", nil)
	if err != nil || !res.IsError || !strings.Contains(firstText(t, res), "refused") {
		t.Fatalf("res = %+v, err = %v; want an is_error result with the handler's message", res, err)
	}
	if _, err := conn.Call(ctx, "nonexistent", nil); err == nil {
		t.Fatal("calling a tool the server does not have must be an error")
	}
	if _, err := conn.Call(ctx, "echo", map[string]any{"message": make(chan int)}); err == nil ||
		!strings.Contains(err.Error(), "arguments") {
		t.Fatalf("err = %v, want an arguments error", err)
	}
}

func TestThePerSessionCallLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		calls int
		fails bool // whether the last call is refused
	}{
		{"a limit refuses the call past it", 3, 4, true},
		{"zero means the default", 0, 5, false},
		{"negative means unlimited", -1, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := pair(t, echoServer(), ServerConfig{Name: "s", PerSessionCallLimit: tc.limit}, ConnectionOptions{})
			var err error
			for i := 0; i < tc.calls; i++ {
				if err != nil {
					t.Fatalf("call %d of %d failed: %v", i, tc.calls, err)
				}
				_, err = conn.Call(context.Background(), "echo", map[string]any{"message": "x"})
			}
			if tc.fails != errors.Is(err, ErrCallLimit) {
				t.Fatalf("last call err = %v, want ErrCallLimit %v", err, tc.fails)
			}
		})
	}
}

// A server with allow_sampling unset is never told the client samples, and a
// request it sends anyway fails the call; with it set, a request with no
// handler to answer it is refused.
func TestSamplingIsRefusedUnlessEnabled(t *testing.T) {
	sampler := func(context.Context, *CreateMessageParams) (*CreateMessageResult, error) {
		return &CreateMessageResult{Role: "assistant", Model: "test", Content: &TextContent{Text: "sampled"}}, nil
	}
	for _, tc := range []struct {
		name    string
		allow   bool
		handler SamplingHandler
		want    error // nil: the sampled answer comes back
	}{
		{"disabled by default", false, sampler, errAny},
		{"enabled with a handler", true, sampler, nil},
		{"enabled with no handler", true, nil, ErrSamplingNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := pair(t, samplingServer(t), ServerConfig{Name: "s", AllowSampling: tc.allow},
				ConnectionOptions{Sampling: tc.handler})
			res, err := conn.Call(context.Background(), "ask", nil)
			switch {
			case tc.want == nil && err != nil:
				t.Fatal(err)
			case tc.want == nil && firstText(t, res) != "sampled":
				t.Fatalf("res = %+v, want the sampled answer", res)
			case tc.want == errAny && err == nil:
				t.Fatalf("sampling must be refused; got %+v", res)
			case tc.want != nil && tc.want != errAny && !errors.Is(err, tc.want):
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

var errAny = errors.New("any error")

// samplingServer answers `ask` through a multi-round-trip input request: the
// first call asks the client to sample, and the client's retry carries the
// answer and the opaque state back.
func samplingServer(t *testing.T) *sdk.Server {
	s := newServer("sampler")
	s.AddTool(&Tool{Name: "ask", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, req *sdk.CallToolRequest) (*CallToolResult, error) {
			if r, ok := req.Params.InputResponses["s1"].(*sdk.CreateMessageWithToolsResult); ok { //nolint:staticcheck // the deprecated sampling round trip
				if req.Params.RequestState != "asked-once" {
					t.Errorf("requestState must come back verbatim; got %q", req.Params.RequestState)
				}
				return &CallToolResult{Content: r.Content}, nil
			}
			return &CallToolResult{RequestState: "asked-once", InputRequests: sdk.InputRequestMap{
				"s1": &CreateMessageParams{MaxTokens: 16, Messages: []*sdk.SamplingMessage{ //nolint:staticcheck // as above
					{Role: "user", Content: &TextContent{Text: "hi"}}}},
			}}, nil
		})
	return s
}

// timeout_s bounds returning, not only the answer: a peer that never reads its
// pipe, or an HTTP server that accepts the request and never answers, does not
// hold the caller past it; nor does a caller's cancelled context.
func TestConnectReturnsAtItsDeadline(t *testing.T) {
	wedged := func(ctx context.Context) error {
		c2sR, c2sW := io.Pipe() // nobody reads c2sR
		s2cR, s2cW := io.Pipe()
		t.Cleanup(func() { _ = c2sR.Close(); _ = s2cW.Close() })
		_, err := Connect(ctx, ServerConfig{Name: "stall", Timeout: 100 * time.Millisecond},
			NewPipeTransport(s2cR, c2sW, wire.Limits{}), ConnectionOptions{})
		return err
	}
	stalling := func(ctx context.Context) error {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		t.Cleanup(srv.Close)
		p := NewPool(ConnectionOptions{})
		t.Cleanup(func() { _ = p.Close() })
		_, err := p.Connect(ctx, ServerConfig{Name: "remote", URL: srv.URL, Timeout: 200 * time.Millisecond}, nil, nil)
		return err
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name    string
		connect func(context.Context) error
		ctx     context.Context
		want    error
	}{
		{"a wedged pipe", wedged, context.Background(), context.DeadlineExceeded},
		{"a stalling http server", stalling, context.Background(), context.DeadlineExceeded},
		{"a cancelled context", wedged, cancelled, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() { done <- tc.connect(tc.ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, tc.want) {
					t.Fatalf("err = %v, want %v", err, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Connect hung past its deadline")
			}
		})
	}
}

// A server whose answer carries a duplicate key does not get to choose which
// of the two values the client sees.
func TestConnectRejectsAnAnswerWithADuplicateKey(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	t.Cleanup(func() { _ = c2sR.Close(); _ = s2cW.Close() })
	go func() {
		line, _ := bufio.NewReader(c2sR).ReadBytes('\n')
		var req struct{ ID json.RawMessage }
		_ = json.Unmarshal(line, &req)
		_, _ = fmt.Fprintf(s2cW, `{"jsonrpc":"2.0","id":%s,"result":{"supportedVersions":["2026-07-28"],`+
			`"capabilities":{},"capabilities":{"tools":{}}}}`+"\n", req.ID)
	}()
	_, err := Connect(context.Background(), ServerConfig{Name: "s", Timeout: 3 * time.Second},
		NewPipeTransport(s2cR, c2sW, wire.Limits{}), ConnectionOptions{})
	if err == nil {
		t.Fatal("a frame with a duplicate key must be rejected")
	}
}

// A connection opened over a caller's transport is never redialled: once its
// session ends, it stays ended.
func TestADirectConnectionWhoseSessionEndedIsNotRedialled(t *testing.T) {
	conn, ss := pair(t, echoServer(), ServerConfig{Name: "direct"}, ConnectionOptions{})
	must(t, ss.Close())
	testutil.Eventually(t, "the client to notice the server leaving", func() bool { return !conn.Alive() })
	if _, err := conn.ListTools(context.Background()); err == nil || !strings.Contains(err.Error(), "session ended") {
		t.Fatalf("err = %v, want the session-ended error", err)
	}
	if conn.Reconnects() != 0 {
		t.Fatalf("reconnects = %d, want 0", conn.Reconnects())
	}
}

func TestACloseIsFinalAndIdempotent(t *testing.T) {
	conn, _ := pair(t, echoServer(), ServerConfig{Name: "c"}, ConnectionOptions{})
	must(t, conn.Close())
	must(t, conn.Close())
	if _, err := conn.Call(context.Background(), "echo", nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	if conn.Alive() {
		t.Fatal("a closed connection reports alive")
	}
}
