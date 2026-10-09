package mcp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/internal/diag"
	"github.com/agentfox/agentkit-go/mcp"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// pair wires a client connection and a server together over two in-memory
// pipes, through the strict NDJSON transport on both ends.
//
// No subprocess, no port, no timing. The client and server are the SHIPPED
// implementations talking to each other.
func pair(t *testing.T, srv *mcp.Server, cfg mcp.ServerConfig, opts mcp.ConnectionOptions) *mcp.ServerConnection {
	t.Helper()
	c2sR, c2sW := io.Pipe() // client -> server
	s2cR, s2cW := io.Pipe() // server -> client

	ss, err := srv.Connect(context.Background(), mcp.NewPipeTransport(c2sR, s2cW, wire.Limits{}), nil)
	must(t, err)
	conn, err := mcp.Connect(context.Background(), cfg, mcp.NewPipeTransport(s2cR, c2sW, wire.Limits{}), opts)
	must(t, err)
	t.Cleanup(func() {
		_ = conn.Close()
		_ = ss.Close()
	})
	return conn
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func echoServer(t *testing.T) *mcp.Server {
	t.Helper()
	s := mcp.NewServer(mcp.ServerOptions{Info: mcp.Implementation{Name: "test-server", Version: "1"}})
	must(t, s.RegisterTool(&mcp.Tool{
		Name:        "echo",
		Description: "echo the message back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","description":"what to echo"}},"required":["message"]}`),
	}, func(_ context.Context, args map[string]any) (*mcp.CallToolResult, error) {
		msg, _ := args["message"].(string)
		return text("echo: " + msg), nil
	}))
	must(t, s.RegisterTool(&mcp.Tool{Name: "boom", Description: "always fails"},
		func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			return nil, errors.New("the tool refused")
		}))
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatalf("result has no content: %+v", res)
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("first content block is %T, want text", res.Content[0])
	}
	return tc.Text
}

// TestTheClientAndServerDiscoverAndCall is the end-to-end shape.
func TestTheClientAndServerDiscoverAndCall(t *testing.T) {
	conn := pair(t, echoServer(t), mcp.ServerConfig{Name: "test"}, mcp.ConnectionOptions{})
	ctx := context.Background()

	sess, err := conn.Session(ctx)
	must(t, err)
	if info := sess.InitializeResult().ServerInfo; info == nil || info.Name != "test-server" {
		t.Fatalf("server identity = %+v", info)
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

// TestAFailingHandlerIsAToolErrorNotAProtocolError is the distinction that
// decides whether the model ever hears about it.
func TestAFailingHandlerIsAToolErrorNotAProtocolError(t *testing.T) {
	conn := pair(t, echoServer(t), mcp.ServerConfig{Name: "test"}, mcp.ConnectionOptions{})
	ctx := context.Background()

	res, err := conn.Call(ctx, "boom", nil)
	if err != nil {
		t.Fatalf("a failing TOOL must not surface as a call error: %v", err)
	}
	if !res.IsError {
		t.Fatal("the result must be marked as an error so the model can react")
	}
	if !strings.Contains(firstText(t, res), "refused") {
		t.Fatalf("content = %+v, want the handler's own message", res.Content)
	}

	// An unknown tool IS a protocol error: that call genuinely never happened.
	if _, err := conn.Call(ctx, "nonexistent", nil); err == nil {
		t.Fatal("calling a tool the server does not have must be an error")
	}
}

// TestAPanickingHandlerDoesNotKillTheConnection: one broken tool must not be a
// dead connection for every other one.
func TestAPanickingHandlerDoesNotKillTheConnection(t *testing.T) {
	s := echoServer(t)
	must(t, s.RegisterTool(&mcp.Tool{Name: "panicky"},
		func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			panic("handler bug")
		}))
	conn := pair(t, s, mcp.ServerConfig{Name: "test"}, mcp.ConnectionOptions{})
	ctx := context.Background()

	res, err := conn.Call(ctx, "panicky", nil)
	if err != nil {
		t.Fatalf("a panicking handler must come back as a result: %v", err)
	}
	if !res.IsError {
		t.Fatal("a panic is an error result")
	}
	if _, err := conn.Call(ctx, "echo", map[string]any{"message": "still here"}); err != nil {
		t.Fatalf("the connection died with the handler: %v", err)
	}
}

// ---- REQ-MCP-CLIENT-09

func textOf(items []mcp.Content) (texts []string) {
	for _, it := range items {
		if tc, ok := it.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	return texts
}

// TestResultsAreCappedAcrossTheWholeResult is REQ-MCP-CLIENT-09.
//
// The budget is spent ACROSS the content items, not per item. A server
// returning two hundred blocks of 49K each passes a per-item cap and delivers
// ten megabytes into the model's context.
func TestResultsAreCappedAcrossTheWholeResult(t *testing.T) {
	var items []mcp.Content
	for i := 0; i < 200; i++ {
		items = append(items, &mcp.TextContent{Text: strings.Repeat("x", 49_000)})
	}
	out := mcp.CapContent(items)

	total := 0
	var note string
	for _, s := range textOf(out) {
		total += len([]rune(s))
		if strings.Contains(s, "truncated") {
			note = s
		}
	}
	if total > mcp.ResultCharCap+len([]rune(note)) {
		t.Fatalf("content totals %d characters, past the %d cap", total, mcp.ResultCharCap)
	}
	if note == "" {
		t.Fatal("truncation must carry a note to the model, or it reasons over a fragment " +
			"it believes is whole")
	}
	if !strings.Contains(note, "Narrow the request") {
		t.Fatalf("the note should tell the model what to DO: %q", note)
	}
}

// TestTheCapCountsRunesNotBytes: "characters" in a requirement about model
// context means what the model sees.
func TestTheCapCountsRunesNotBytes(t *testing.T) {
	// Each of these is 3 bytes and 1 rune.
	s := strings.Repeat("漢", mcp.ResultCharCap-10)
	out := mcp.CapContent([]mcp.Content{&mcp.TextContent{Text: s}})
	if len(out) != 1 {
		t.Fatalf("%d items, want the whole thing kept: it is under the cap in RUNES", len(out))
	}
	if got := len([]rune(textOf(out)[0])); got != mcp.ResultCharCap-10 {
		t.Fatalf("kept %d runes, want %d", got, mcp.ResultCharCap-10)
	}
}

// image is a block whose base64 encoding is n characters.
func image(n int) *sdk.ImageContent {
	return &sdk.ImageContent{Data: make([]byte, n/4*3), MIMEType: "image/png"}
}

// TestNonTextContentIsNotTruncated: an image is never SLICED. One that fits
// the cap is kept whole; one that does not is dropped whole, because half a
// base64 image is a corrupt image, not a shorter one.
func TestNonTextContentIsNotTruncated(t *testing.T) {
	img := image(40_000)
	if out := mcp.CapContent([]mcp.Content{img}); len(out) != 1 || out[0] != mcp.Content(img) {
		t.Fatal("an image within the cap must be kept intact")
	}
	out := mcp.CapContent([]mcp.Content{image(100_000)})
	if len(out) != 1 || !strings.Contains(textOf(out)[0], "truncated") {
		t.Fatalf("an image over the cap must be dropped whole, leaving the note: %+v", out)
	}
}

// TestNonTextContentIsChargedAgainstTheCap. A server could otherwise deliver
// ten megabytes past the cap as one image block.
func TestNonTextContentIsChargedAgainstTheCap(t *testing.T) {
	out := mcp.CapContent([]mcp.Content{
		&mcp.TextContent{Text: strings.Repeat("t", 20_000)},
		image(40_000),
		&mcp.TextContent{Text: "after"},
	})
	for _, it := range out {
		if _, ok := it.(*sdk.ImageContent); ok {
			t.Fatal("a 40K image after 20K of text does not fit in a 50K cap; it must be dropped")
		}
	}
	if texts := textOf(out); !strings.Contains(texts[len(texts)-1], "truncated") {
		t.Fatalf("the drop must carry the note: %+v", out)
	}

	// An image that fits is kept whole and still spends the budget.
	out = mcp.CapContent([]mcp.Content{image(40_000), &mcp.TextContent{Text: strings.Repeat("t", 20_000)}})
	if _, ok := out[0].(*sdk.ImageContent); !ok {
		t.Fatalf("an image within the cap must be kept intact: %+v", out[0])
	}
	if n := len([]rune(textOf(out)[0])); n != 10_000 {
		t.Fatalf("the text after a 40K image gets the remaining 10K, got %d", n)
	}
}

// ---- REQ-MCP-CLIENT-07 / -08

func TestThePerSessionCallLimitIsEnforced(t *testing.T) {
	cfg := mcp.ServerConfig{Name: "test", PerSessionCallLimit: 3}
	conn := pair(t, echoServer(t), cfg, mcp.ConnectionOptions{})
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := conn.Call(ctx, "echo", map[string]any{"message": "x"}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	_, err := conn.Call(ctx, "echo", map[string]any{"message": "x"})
	if !errors.Is(err, mcp.ErrCallLimit) {
		t.Fatalf("err = %v, want ErrCallLimit after the cap", err)
	}
}

func TestTheDefaultCallLimitIsAThousandAndNegativeMeansUnlimited(t *testing.T) {
	conn := pair(t, echoServer(t), mcp.ServerConfig{Name: "test"}, mcp.ConnectionOptions{})
	ctx := context.Background()
	if _, err := conn.Call(ctx, "echo", map[string]any{"message": "x"}); err != nil {
		t.Fatal(err)
	}

	conn2 := pair(t, echoServer(t), mcp.ServerConfig{Name: "u", PerSessionCallLimit: -1}, mcp.ConnectionOptions{})
	for i := 0; i < 5; i++ {
		if _, err := conn2.Call(ctx, "echo", map[string]any{"message": "x"}); err != nil {
			t.Fatalf("unlimited must not cap: %v", err)
		}
	}
}

// TestEveryToolCallIsAudited is REQ-MCP-CLIENT-03 and REQ-OBS-05.
func TestEveryToolCallIsAudited(t *testing.T) {
	var mu sync.Mutex
	var events []core.AuditEvent
	conn := pair(t, echoServer(t), mcp.ServerConfig{Name: "gh"}, mcp.ConnectionOptions{
		Audit: func(e core.AuditEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
	})
	ctx := context.Background()
	if _, err := conn.Call(ctx, "echo", map[string]any{"message": "secret-value"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Call(ctx, "boom", nil); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("%d audit events, want one per call", len(events))
	}
	for _, e := range events {
		if e.ServerName != "gh" {
			t.Fatalf("server_name = %q, want gh (REQ-OBS-05)", e.ServerName)
		}
		blob, _ := json.Marshal(e)
		if strings.Contains(string(blob), "secret-value") {
			t.Fatalf("the audit event carries the argument VALUE: %s", blob)
		}
	}
	if events[0].ArgumentsHash == "" {
		t.Fatal("REQ-OBS-05 requires an arguments hash")
	}
	if !events[1].IsError {
		t.Fatal("a failed tool call must be audited as an error")
	}
}

// TestSamplingIsRefusedUnlessEnabledAndAlwaysAudited is REQ-MCP-CLIENT-08.
//
// A server with allow_sampling unset is never told the client samples, and a
// request it sends anyway fails the call. One with it set is told, and every
// request is audited, refusals included: a refusal that leaves no trace is
// indistinguishable from a server that never asked.
func TestSamplingIsRefusedUnlessEnabledAndAlwaysAudited(t *testing.T) {
	for _, tc := range []struct {
		name      string
		allow     bool
		handler   mcp.SamplingHandler
		wantErr   bool
		wantAudit bool
	}{
		{"disabled by default", false, okSampler, true, false},
		{"enabled with a handler", true, okSampler, false, true},
		{"enabled with no handler", true, nil, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var events []core.AuditEvent
			cfg := mcp.ServerConfig{Name: "s", AllowSampling: tc.allow}
			conn := pair(t, samplingServer(t), cfg, mcp.ConnectionOptions{
				Sampling: tc.handler,
				Audit:    func(e core.AuditEvent) { mu.Lock(); events = append(events, e); mu.Unlock() },
			})

			res, err := conn.Call(context.Background(), "ask", nil)
			switch {
			case tc.wantErr && err == nil:
				t.Fatalf("sampling must be refused; got %+v", res)
			case tc.wantErr && tc.allow && !errors.Is(err, mcp.ErrSamplingNotAllowed):
				t.Fatalf("want ErrSamplingNotAllowed, got %v", err)
			case !tc.wantErr && err != nil:
				t.Fatal(err)
			case !tc.wantErr && firstText(t, res) != "sampled":
				t.Fatalf("sampling should have succeeded; got %+v", res)
			}

			mu.Lock()
			defer mu.Unlock()
			var sampled bool
			for _, e := range events {
				sampled = sampled || e.ToolName == "sampling/createMessage"
			}
			if sampled != tc.wantAudit {
				t.Fatalf("sampling audited = %v, want %v; events = %+v", sampled, tc.wantAudit, events)
			}
		})
	}
}

func okSampler(context.Context, *mcp.CreateMessageParams) (*mcp.CreateMessageResult, error) {
	return &mcp.CreateMessageResult{Role: "assistant", Model: "test",
		Content: &mcp.TextContent{Text: "sampled"}}, nil
}

// samplingServer answers `ask` through a multi-round-trip input request: the
// first call asks the client to sample, and the client's RETRY carries the
// answer and the opaque state back.
func samplingServer(t *testing.T) *mcp.Server {
	t.Helper()
	s := mcp.NewServer(mcp.ServerOptions{})
	s.AddTool(&mcp.Tool{Name: "ask", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(_ context.Context, req *sdk.CallToolRequest) (*mcp.CallToolResult, error) {
			if r, ok := req.Params.InputResponses["s1"].(*sdk.CreateMessageWithToolsResult); ok {
				if req.Params.RequestState != "asked-once" {
					t.Errorf("requestState must come back verbatim; got %q", req.Params.RequestState)
				}
				return &mcp.CallToolResult{Content: r.Content}, nil
			}
			return &mcp.CallToolResult{RequestState: "asked-once", InputRequests: sdk.InputRequestMap{
				"s1": &mcp.CreateMessageParams{MaxTokens: 16, Messages: []*sdk.SamplingMessage{
					{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}},
			}}, nil
		})
	return s
}

// ---- REQ-SEC-11 on the stdio surface

// TestAMalformedFrameTearsTheConnectionDown: a duplicate key is legal to
// encoding/json and to the SDK, and rejected by REQ-SEC-11.3. The framing is
// then untrustworthy, so the session ends rather than resynchronizing.
func TestAMalformedFrameTearsTheConnectionDown(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, s2cR) }()
	ss, err := echoServer(t).Connect(context.Background(), mcp.NewPipeTransport(c2sR, s2cW, wire.Limits{}), nil)
	must(t, err)
	t.Cleanup(func() { _ = ss.Close(); _ = c2sW.Close() })

	_, _ = c2sW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"ping"}` + "\n"))

	done := make(chan struct{})
	go func() { _ = ss.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the server kept reading after a malformed frame (REQ-SEC-11.4)")
	}
}

// TestTheClientRejectsADuplicateKeyFrame is the same rule on the client's
// side of the pipe: a server whose answer carries a duplicate key does not get
// to choose which of the two values the client sees.
func TestTheClientRejectsADuplicateKeyFrame(t *testing.T) {
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
	_, err := mcp.Connect(context.Background(), mcp.ServerConfig{Name: "s", Timeout: 3 * time.Second},
		mcp.NewPipeTransport(s2cR, c2sW, wire.Limits{}), mcp.ConnectionOptions{})
	if err == nil {
		t.Fatal("a frame with a duplicate key must be rejected")
	}
}

// TestConnectReturnsAtTheTimeoutOnAWedgedPipe is REQ-MCP-CLIENT-07's
// timeout_s where the transport itself is stuck: a peer that never reads its
// pipe must not hold the caller inside a write past the deadline.
func TestConnectReturnsAtTheTimeoutOnAWedgedPipe(t *testing.T) {
	c2sR, c2sW := io.Pipe() // nobody ever reads c2sR
	s2cR, s2cW := io.Pipe()
	t.Cleanup(func() { _ = c2sR.Close(); _ = s2cW.Close() })

	done := make(chan error, 1)
	go func() {
		_, err := mcp.Connect(context.Background(), mcp.ServerConfig{Name: "stall", Timeout: 100 * time.Millisecond},
			mcp.NewPipeTransport(s2cR, c2sW, wire.Limits{}), mcp.ConnectionOptions{})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v; want the connection's own deadline", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Connect hung past timeout_s on a pipe nobody reads")
	}
}

// ---- interpolation

// TestAnUnresolvedVariableIsAConfigurationError is NFR-SEC-03: "unexpanded
// variable references are a configuration error, not silently passed to the
// subprocess".
func TestAnUnresolvedVariableIsAConfigurationError(t *testing.T) {
	cfg := mcp.ServerConfig{
		Name: "gh", Command: "true",
		Env: map[string]string{"TOKEN": "${GH_TOKEN}", "MODE": "${MISSING}-suffix", "OTHER": "$ALSO_MISSING"},
	}
	p := mcp.NewPool(mcp.ConnectionOptions{})
	defer p.Close()
	_, err := p.Connect(context.Background(), cfg, []string{"PATH=/usr/bin"},
		func(name string) string {
			if name == "GH_TOKEN" {
				return "ghp_secret"
			}
			return ""
		})

	var unresolved *mcp.UnresolvedVariableError
	if !errors.As(err, &unresolved) {
		t.Fatalf("err = %v; an unset ${VAR} must be a typed configuration error", err)
	}
	if got := strings.Join(unresolved.Variables, ","); got != "MISSING,ALSO_MISSING" {
		t.Fatalf("variables = %v; the error must name every unresolved reference so they "+
			"are fixed in one pass", unresolved.Variables)
	}
	if !strings.Contains(err.Error(), "${MISSING}") || strings.Contains(err.Error(), "ghp_secret") {
		t.Fatalf("the message must name the variable and never the resolved secret: %v", err)
	}
	if len(p.Names()) != 0 {
		t.Fatal("nothing may be spawned on a configuration error")
	}
}

// TestAnExplicitlyEmptyVariableIsNotUnresolved. `FOO=` in the environment is a
// value the operator chose; only an ABSENT variable is unresolved.
func TestAnExplicitlyEmptyVariableIsNotUnresolved(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	p := mcp.NewPool(mcp.ConnectionOptions{})
	defer p.Close()

	cfg := mcp.ServerConfig{Name: "child", Command: exe,
		Env: map[string]string{"SUPPLIED": "[${EMPTY}]"}}
	conn, err := p.Connect(context.Background(), cfg,
		[]string{"AGENTKIT_MCP_CHILD=env", "EMPTY=", "PATH=" + os.Getenv("PATH")}, nil)
	if err != nil {
		t.Fatalf("connect: %v; a variable set to the empty string is set", err)
	}
	res, err := conn.Call(context.Background(), "env", nil)
	must(t, err)
	if !strings.Contains(firstText(t, res), "SUPPLIED=[]") {
		t.Fatalf("child env = %q; the empty value must be substituted", firstText(t, res))
	}
}

// ---- remote servers: headers, redirects, deadlines

// remote serves srv over HTTP behind key, recording the headers of the first
// request that reaches it.
func remote(t *testing.T, srv *mcp.Server, key string, got *http.Header) *httptest.Server {
	t.Helper()
	h, err := srv.HTTPHandler(mcp.HTTPOptions{APIKey: key})
	must(t, err)
	var once sync.Once
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			once.Do(func() { *got = r.Header.Clone() })
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// TestARemoteServersHeadersAreInterpolatedFromSecrets. A bearer token for a
// remote server has the same reason not to sit in a config file as a
// subprocess's credential does, so headers resolve through the same ${VAR}
// path.
func TestARemoteServersHeadersAreInterpolatedFromSecrets(t *testing.T) {
	var got http.Header
	hs := remote(t, echoServer(t), "ghp_secret", &got)

	p := mcp.NewPool(mcp.ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := p.Connect(context.Background(), mcp.ServerConfig{
		Name: "remote", URL: hs.URL,
		Headers: map[string]string{"Authorization": "Bearer ${GH_TOKEN}"},
	}, nil, func(name string) string {
		if name == "GH_TOKEN" {
			return "ghp_secret"
		}
		return ""
	})
	must(t, err)
	if h := got.Get("Authorization"); h != "Bearer ghp_secret" {
		t.Fatalf("Authorization was %q; the ${VAR} must resolve from the secrets store", h)
	}
	res, err := conn.Call(context.Background(), "echo", map[string]any{"message": "over http"})
	must(t, err)
	if firstText(t, res) != "echo: over http" {
		t.Fatalf("result = %+v", res)
	}
}

// TestAnUnresolvedHeaderVariableIsAConfigurationError is NFR-SEC-03 on the
// header path: nothing is sent at all and the error names the variable.
func TestAnUnresolvedHeaderVariableIsAConfigurationError(t *testing.T) {
	var got http.Header
	hs := remote(t, echoServer(t), "k", &got)

	p := mcp.NewPool(mcp.ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	_, err := p.Connect(context.Background(), mcp.ServerConfig{
		Name: "remote", URL: hs.URL,
		Headers: map[string]string{"Authorization": "Bearer ${MISSING}"},
	}, nil, func(string) string { return "" })

	var unresolved *mcp.UnresolvedVariableError
	if !errors.As(err, &unresolved) || len(unresolved.Variables) != 1 || unresolved.Variables[0] != "MISSING" {
		t.Fatalf("err = %v; want an UnresolvedVariableError naming MISSING", err)
	}
	if got != nil {
		t.Fatal("no request may be made on a configuration error")
	}
}

// TestAHeaderCarryingAControlByteIsRefused: the value can arrive from a
// config file or from an interpolated secret.
func TestAHeaderCarryingAControlByteIsRefused(t *testing.T) {
	var got http.Header
	hs := remote(t, echoServer(t), "k", &got)

	p := mcp.NewPool(mcp.ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	_, err := p.Connect(context.Background(), mcp.ServerConfig{
		Name: "remote", URL: hs.URL,
		Headers: map[string]string{"X-Token": "abc${INJECT}", "X-API-Key": "k"},
	}, nil, func(string) string { return "def\r\nX-Smuggled: yes" })
	must(t, err)

	if _, present := got["X-Token"]; present {
		t.Fatal("a header value carrying CRLF must be dropped, not sent")
	}
	if got.Get("X-Smuggled") != "" {
		t.Fatal("a smuggled header reached the server")
	}
}

// TestOnlyHTTPSchemesAreTransports. A file:// or custom-scheme endpoint in a
// config file is either a mistake or an attempt to make the client read
// something local.
func TestOnlyHTTPSchemesAreTransports(t *testing.T) {
	p := mcp.NewPool(mcp.ConnectionOptions{})
	for _, bad := range []string{"file:///etc/passwd", "ftp://h/x", "ws://h/x", "::"} {
		if _, err := p.Connect(context.Background(), mcp.ServerConfig{Name: "x", URL: bad}, nil, nil); err == nil {
			t.Fatalf("url %q must be refused", bad)
		}
	}
}

// TestTheDefaultHTTPClientDoesNotFollowRedirects. The configured headers carry
// the server's bearer token, and net/http forwards custom headers to wherever
// a redirect points.
func TestTheDefaultHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var elsewhereHits atomic.Int32
	elsewhere := remote(t, echoServer(t), "the-secret", nil)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhereHits.Add(1)
		elsewhere.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(origin.Close)

	p := mcp.NewPool(mcp.ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	_, err := p.Connect(context.Background(), mcp.ServerConfig{Name: "r", URL: origin.URL,
		Timeout: 3 * time.Second, Headers: map[string]string{"X-Api-Key": "the-secret"}}, nil, nil)
	if err == nil {
		t.Fatal("a redirected connect must fail, not succeed against a server we never configured")
	}
	if n := elsewhereHits.Load(); n != 0 {
		t.Fatalf("the redirect target received %d request(s) carrying our headers", n)
	}
}

// TestTimeoutSAppliesToAStallingHTTPServer: a server that accepts the POST and
// then never answers must not hold the caller past timeout_s.
func TestTimeoutSAppliesToAStallingHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	p := mcp.NewPool(mcp.ConnectionOptions{})
	t.Cleanup(func() { _ = p.Close() })
	done := make(chan error, 1)
	go func() {
		_, err := p.Connect(context.Background(), mcp.ServerConfig{Name: "remote", URL: srv.URL,
			Timeout: 200 * time.Millisecond}, nil, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v; want the operation's own deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connect hung on a stalling server; timeout_s never fired")
	}
}

// ---- pool: REQ-MCP-CLIENT-04, -05, -06

func poolWith(t *testing.T, cfgs ...mcp.ServerConfig) *mcp.Pool {
	t.Helper()
	p := mcp.NewPool(mcp.ConnectionOptions{})
	for _, cfg := range cfgs {
		must(t, p.Add(pair(t, echoServer(t), cfg, mcp.ConnectionOptions{})))
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestToolNamesAreQualifiedByServer is REQ-MCP-CLIENT-05.
func TestToolNamesAreQualifiedByServer(t *testing.T) {
	p := poolWith(t, mcp.ServerConfig{Name: "github"})
	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
		if tl.MCPServer != "github" {
			t.Fatalf("%s carries MCPServer %q; the audit trail must not have to guess "+
				"the server from a name whose prefix is configurable", tl.Name, tl.MCPServer)
		}
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "github__boom,github__echo" {
		t.Fatalf("names = %v, want the server_name__tool_name convention", names)
	}
}

func TestAConfiguredPrefixOverridesTheDefault(t *testing.T) {
	p := poolWith(t, mcp.ServerConfig{Name: "github", ToolPrefix: "gh."})
	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	for _, tl := range tools {
		if !strings.HasPrefix(tl.Name, "gh.") {
			t.Fatalf("%s does not use the configured prefix", tl.Name)
		}
	}

	// An empty tool_prefix in a config file means "the default"; DisablePrefix
	// is how a caller asks for none, because "" cannot mean both.
	p2 := poolWith(t, mcp.ServerConfig{Name: "raw", DisablePrefix: true})
	tools, err = p2.Tools(context.Background(), nil)
	must(t, err)
	for _, tl := range tools {
		if strings.Contains(tl.Name, "__") {
			t.Fatalf("%s is prefixed despite DisablePrefix", tl.Name)
		}
	}
}

// TestAShadowedNativeToolIsRefusedAtConnect is REQ-MCP-CLIENT-06 where the
// requirement puts it: at CONNECTION time, before the wrong tool can run.
func TestAShadowedNativeToolIsRefusedAtConnect(t *testing.T) {
	hs := remote(t, echoServer(t), "k", nil)
	p := mcp.NewPool(mcp.ConnectionOptions{})
	p.NativeTools = []string{"echo"} // the host's own tool of the same name
	t.Cleanup(func() { _ = p.Close() })

	conn, err := p.Connect(context.Background(), mcp.ServerConfig{Name: "remote", URL: hs.URL,
		DisablePrefix: true, Headers: map[string]string{"X-API-Key": "k"}}, nil, nil)
	if !errors.Is(err, mcp.ErrNameCollision) {
		t.Fatalf("err = %v, want ErrNameCollision at Connect", err)
	}
	if conn != nil {
		t.Fatal("a refused connection must not be returned")
	}
	if len(p.Names()) != 0 {
		t.Fatalf("names = %v; the connection must be torn down, not pooled", p.Names())
	}
}

// TestAPrefixedToolDoesNotCollideAtConnect: the default `server__tool`
// qualification is what keeps an MCP `echo` and a native `echo` apart.
func TestAPrefixedToolDoesNotCollideAtConnect(t *testing.T) {
	hs := remote(t, echoServer(t), "k", nil)
	p := mcp.NewPool(mcp.ConnectionOptions{})
	p.NativeTools = []string{"echo"}
	t.Cleanup(func() { _ = p.Close() })

	if _, err := p.Connect(context.Background(), mcp.ServerConfig{Name: "remote", URL: hs.URL,
		Headers: map[string]string{"X-API-Key": "k"}}, nil, nil); err != nil {
		t.Fatalf("connect: %v; `remote__echo` shadows nothing", err)
	}
}

// TestANativeToolRegisteredAfterConnectIsCaughtByTheBackstop keeps the check in
// Tools honest.
func TestANativeToolRegisteredAfterConnectIsCaughtByTheBackstop(t *testing.T) {
	p := poolWith(t, mcp.ServerConfig{Name: "srv", DisablePrefix: true})
	_, err := p.Tools(context.Background(), []core.Tool{{Name: "echo", Description: "the native one"}})
	if !errors.Is(err, mcp.ErrNameCollision) {
		t.Fatalf("err = %v, want ErrNameCollision", err)
	}
	if !strings.Contains(err.Error(), "native") {
		t.Fatalf("the error must say what it collided with: %v", err)
	}
}

func TestTwoServersExposingTheSameNameCollide(t *testing.T) {
	p := poolWith(t,
		mcp.ServerConfig{Name: "a", DisablePrefix: true},
		mcp.ServerConfig{Name: "b", DisablePrefix: true})
	if _, err := p.Tools(context.Background(), nil); !errors.Is(err, mcp.ErrNameCollision) {
		t.Fatalf("err = %v; two unprefixed servers both exposing `echo` collide", err)
	}
}

// TestAnAdaptedToolRunsThroughTheRealConnection closes the loop: the core.Tool
// the pool produces actually calls the MCP server.
func TestAnAdaptedToolRunsThroughTheRealConnection(t *testing.T) {
	p := poolWith(t, mcp.ServerConfig{Name: "srv"})
	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	var echo core.Tool
	for _, tl := range tools {
		if tl.Name == "srv__echo" {
			echo = tl
		}
	}
	if echo.Execute == nil {
		t.Fatal("the adapted tool has no Execute")
	}
	res := echo.Execute(context.Background(), json.RawMessage(`{"message":"through"}`))
	if !res.OK {
		t.Fatalf("call failed: %s %s", res.Error, res.Detail)
	}
	blob, _ := json.Marshal(res.Data)
	if !strings.Contains(string(blob), "echo: through") {
		t.Fatalf("data = %s", blob)
	}
}

// TestTheAdaptedSchemaCarriesRequiredProperties: a tool whose schema is
// flattened to an open object loses the validation REQ-TOOL-11 does before the
// call.
func TestTheAdaptedSchemaCarriesRequiredProperties(t *testing.T) {
	p := poolWith(t, mcp.ServerConfig{Name: "srv"})
	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	for _, tl := range tools {
		if tl.Name != "srv__echo" {
			continue
		}
		if !tl.InputSchema.IsRequired("message") {
			t.Fatalf("message is not required; the server declared it so: %+v", tl.InputSchema)
		}
		if props := tl.InputSchema.PropertyList(); len(props) != 1 || props[0] != "message" {
			t.Fatalf("properties = %v, want [message]", props)
		}
		return
	}
	t.Fatal("srv__echo not found")
}

// TestToolArgumentsPassThroughWithoutFloat64Laundering. Go's default for a
// JSON number is float64: 9007199254740993 would come out the other side as
// 9007199254740992 and 1.10 as 1.1. The bytes go through as the model wrote
// them, and RegisterTool hands the handler json.Numbers.
func TestToolArgumentsPassThroughWithoutFloat64Laundering(t *testing.T) {
	var mu sync.Mutex
	var got map[string]any
	s := mcp.NewServer(mcp.ServerOptions{})
	must(t, s.RegisterTool(&mcp.Tool{Name: "inspect"},
		func(_ context.Context, args map[string]any) (*mcp.CallToolResult, error) {
			mu.Lock()
			got = args
			mu.Unlock()
			return nil, nil
		}))
	p := mcp.NewPool(mcp.ConnectionOptions{})
	must(t, p.Add(pair(t, s, mcp.ServerConfig{Name: "s"}, mcp.ConnectionOptions{})))

	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	if len(tools) != 1 {
		t.Fatalf("%d tools", len(tools))
	}
	res := tools[0].Execute(context.Background(),
		json.RawMessage(`{"id":9007199254740993,"ratio":1.10,"exp":1e3}`))
	if !res.OK {
		t.Fatalf("execute failed: %+v", res)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]json.Number{"id": "9007199254740993", "ratio": "1.10", "exp": "1e3"}
	for k, w := range want {
		if n, ok := got[k].(json.Number); !ok || n != w {
			t.Fatalf("server received %s = %v (%T); want the literal %s", k, got[k], got[k], w)
		}
	}
}

// ---- REQ-MCP-CLIENT-07 config

func TestServerConfigParsesFromTOML(t *testing.T) {
	src := `
[mcp]

[[mcp.servers]]
name = "github"
command = "gh-mcp"
args = ["--stdio"]
tool_prefix = "gh__"
allow_sampling = true
per_session_call_limit = 25
timeout_s = 10

[mcp.servers.env]
GITHUB_TOKEN = "${GH_PAT}"

[[mcp.servers]]
name = "db"
url = "https://db.example/mcp"

[mcp_server]
enabled = true
transport = "http"
port = 8931
api_key_env = "AGENTKIT_MCP_KEY"
`
	cfg, diags, err := mcp.ParseConfig("config.toml", []byte(src))
	must(t, err)
	for _, d := range diags {
		if d.Severity == "error" {
			t.Fatalf("unexpected error diagnostic: %s", d)
		}
	}
	if len(cfg.Servers) != 2 {
		t.Fatalf("%d servers, want 2", len(cfg.Servers))
	}
	gh := cfg.Servers[0]
	if gh.Name != "github" || gh.Command != "gh-mcp" || len(gh.Args) != 1 {
		t.Fatalf("github = %+v", gh)
	}
	if !gh.AllowSampling || gh.PerSessionCallLimit != 25 || gh.Timeout != 10*time.Second {
		t.Fatalf("github options = %+v", gh)
	}
	if gh.Env["GITHUB_TOKEN"] != "${GH_PAT}" {
		t.Fatalf("env = %v; the reference must survive parsing and be resolved at SPAWN "+
			"time, so the credential is never in the config file", gh.Env)
	}
	if cfg.Servers[1].URL == "" {
		t.Fatal("the url server did not parse")
	}
	if !cfg.Server.Enabled || cfg.Server.Transport != "http" || cfg.Server.Port != 8931 {
		t.Fatalf("mcp_server = %+v", cfg.Server)
	}
}

// TestTheRemoteTransportIsStreamableHTTPOrSSE: the SDK speaks both, so both
// are selectable; anything else is an error rather than a silent fallback.
func TestTheRemoteTransportIsStreamableHTTPOrSSE(t *testing.T) {
	cfg, diags, err := mcp.ParseConfig("c.toml", []byte(`
[[mcp.servers]]
name = "legacy"
url = "https://x/sse"
transport = "sse"

[[mcp.servers]]
name = "bad"
url = "https://x/ws"
transport = "websocket"
`))
	must(t, err)
	if len(cfg.Servers) != 1 || cfg.Servers[0].Transport != "sse" {
		t.Fatalf("servers = %+v; want only the sse one usable", cfg.Servers)
	}
	if len(diags) != 1 || diags[0].Severity != "error" || !strings.Contains(diags[0].Message, "websocket") {
		t.Fatalf("diagnostics = %v; an unknown transport must be an error naming it", diags)
	}
}

// TestTheServerIsOffUnlessTheConfigSaysOtherwise is REQ-MCP-SERVER-01.
func TestTheServerIsOffUnlessTheConfigSaysOtherwise(t *testing.T) {
	cfg, _, err := mcp.ParseConfig("c.toml", []byte("[mcp]\n"))
	must(t, err)
	if cfg.Server.Enabled {
		t.Fatal("the inbound server must be off unless a config explicitly enables it")
	}
	if cfg.Server.Transport != "stdio" {
		t.Fatalf("default transport = %q, want stdio", cfg.Server.Transport)
	}
}

func TestHTTPModeWithoutAnAPIKeyEnvIsAConfigError(t *testing.T) {
	_, diags, err := mcp.ParseConfig("c.toml", []byte(
		"[mcp_server]\nenabled = true\ntransport = \"http\"\n"))
	must(t, err)
	var flagged bool
	for _, d := range diags {
		if d.Severity == "error" && strings.Contains(d.Message, "api_key_env") {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("an http server with no api_key_env must be flagged at CONFIG time: %v", diags)
	}
}

func TestDuplicateServerNamesAreAConfigError(t *testing.T) {
	_, diags, err := mcp.ParseConfig("c.toml", []byte(`
[[mcp.servers]]
name = "x"
command = "a"

[[mcp.servers]]
name = "x"
command = "b"
`))
	must(t, err)
	var flagged bool
	for _, d := range diags {
		flagged = flagged || d.Severity == "error"
	}
	if !flagged {
		t.Fatal("the name keys the pool, the tool prefix and every audit event; two " +
			"servers cannot share one")
	}
}

// TestTimeoutSAcceptsAFloatAndTheReconnectLimitParses is REQ-MCP-CLIENT-07,
// whose own default is written `30.0`.
func TestTimeoutSAcceptsAFloatAndTheReconnectLimitParses(t *testing.T) {
	src := `
[[mcp.servers]]
name = "a"
command = "a"
timeout_s = 30.0
per_session_reconnect_limit = 5

[[mcp.servers]]
name = "b"
command = "b"
timeout_s = 2.5
`
	cfg, diags, err := mcp.ParseConfig("c.toml", []byte(src))
	must(t, err)
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %v", diags)
	}
	if cfg.Servers[0].Timeout != 30*time.Second || cfg.Servers[0].PerSessionReconnectLimit != 5 {
		t.Fatalf("a = %+v", cfg.Servers[0])
	}
	if cfg.Servers[1].Timeout != 2500*time.Millisecond {
		t.Fatalf("b.timeout = %v, want 2.5s", cfg.Servers[1].Timeout)
	}
}

// ---- stdio, against a real subprocess

// TestAStdioServerRunsAsASubprocessWithAReducedEnvironment is
// REQ-MCP-CLIENT-10, against a real process: this test binary re-executed.
func TestAStdioServerRunsAsASubprocessWithAReducedEnvironment(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	p := mcp.NewPool(mcp.ConnectionOptions{})
	defer p.Close()

	cfg := mcp.ServerConfig{
		Name: "child", Command: exe,
		Env: map[string]string{"SUPPLIED": "${A_SECRET}"},
	}
	conn, err := p.Connect(context.Background(), cfg,
		[]string{"AGENTKIT_MCP_CHILD=env", "PATH=" + os.Getenv("PATH")},
		func(name string) string {
			if name == "A_SECRET" {
				return "resolved-at-spawn"
			}
			return ""
		})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	res, err := conn.Call(context.Background(), "env", nil)
	must(t, err)
	got := firstText(t, res)
	if !strings.Contains(got, "SUPPLIED=resolved-at-spawn") {
		t.Fatalf("child env = %q; a ${VAR} must be resolved at spawn time", got)
	}
	if strings.Contains(got, "ANTHROPIC_") || strings.Contains(got, "OPENAI_") {
		t.Fatalf("the child inherited provider credentials: %q (REQ-MCP-CLIENT-10, REQ-SEC-08)", got)
	}
}

// TestMain is where this test binary becomes an MCP SERVER when re-executed
// as a subprocess: there is no script to keep in sync and no dependency on
// anything being installed.
func TestMain(m *testing.M) {
	switch os.Getenv("AGENTKIT_MCP_CHILD") {
	case "env":
		serveChild(func(s *mcp.Server) {
			_ = s.RegisterTool(&mcp.Tool{Name: "env"}, func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return text(strings.Join(os.Environ(), "\n")), nil
			})
		}, os.Stdout)
		return
	case "one-shot":
		runOneShotChild()
		return
	case "mortal":
		runMortalChild()
		return
	case "stderr-flood":
		// One 2 MiB line — more than the parent's stderr scanner buffers —
		// and only then serve. A parent that stops reading stderr there
		// leaves this child blocked on the write before it ever answers.
		_, _ = os.Stderr.Write([]byte(strings.Repeat("x", 2<<20) + "\n"))
		serveChild(func(s *mcp.Server) {
			_ = s.RegisterTool(&mcp.Tool{Name: "hello"}, func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return text("hi"), nil
			})
		}, os.Stdout)
		return
	}
	os.Exit(m.Run())
}

func serveChild(register func(*mcp.Server), out io.Writer) {
	s := mcp.NewServer(mcp.ServerOptions{Info: mcp.Implementation{Name: "child", Version: "1"}})
	register(s)
	_ = s.Server.Run(context.Background(), mcp.NewPipeTransport(os.Stdin, out, wire.Limits{}))
}

// runOneShotChild answers the first request with a discover result and exits
// IMMEDIATELY: the shape of a server whose last frame a stdio transport that
// let exec close its pipe could lose.
func runOneShotChild() {
	line, _ := bufio.NewReader(os.Stdin).ReadBytes('\n')
	var req struct{ ID json.RawMessage }
	_ = json.Unmarshal(line, &req)
	_, _ = fmt.Fprintf(os.Stdout, `{"jsonrpc":"2.0","id":%s,"result":{"supportedVersions":["2026-07-28"],"capabilities":{}}}`+"\n", req.ID)
	os.Exit(0)
}

// runMortalChild is a server that can be told to die: `die` answers and then
// exits as soon as that answer is written, `crash` exits without answering,
// and `hello` just works.
func runMortalChild() {
	out := &exitAfterWrite{w: os.Stdout}
	serveChild(func(s *mcp.Server) {
		_ = s.RegisterTool(&mcp.Tool{Name: "hello"}, func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			return text("hi"), nil
		})
		_ = s.RegisterTool(&mcp.Tool{Name: "die"}, func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			out.armed.Store(true)
			return text("bye"), nil
		})
		_ = s.RegisterTool(&mcp.Tool{Name: "crash"}, func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
			os.Exit(1)
			return nil, nil
		})
	}, out)
}

// exitAfterWrite exits the process right after the write that follows arming,
// so a response is fully written before the server is gone.
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

// childPool connects one re-executed child of the given mode through a Pool.
func childPool(t *testing.T, mode string, cfg mcp.ServerConfig) (*mcp.Pool, *mcp.ServerConnection) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	cfg.Command = exe
	p := mcp.NewPool(mcp.ConnectionOptions{
		Warnf: func(f string, a ...any) { t.Logf("client: "+f, a...) },
	})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := p.Connect(context.Background(), cfg,
		[]string{"AGENTKIT_MCP_CHILD=" + mode, "PATH=" + os.Getenv("PATH")}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return p, conn
}

// waitFor polls a condition, failing rather than hanging.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAResponseWrittenJustBeforeExitIsDelivered: a server that answers and
// exits at once must still have its answer read.
func TestAResponseWrittenJustBeforeExitIsDelivered(t *testing.T) {
	_, conn := childPool(t, "one-shot", mcp.ServerConfig{Name: "one-shot", PerSessionReconnectLimit: -1})
	if conn == nil {
		t.Fatal("the discover answer written just before exit was lost")
	}
}

// TestADeadStdioServerIsRespawnedWithinTheReconnectLimit is NFR-REL-03 end to
// end against a real process: a disconnect mid-call is an is_error tool
// result and the next call re-spawns the server; the budget is honoured; and
// past it the connection is dead and every call is is_error.
func TestADeadStdioServerIsRespawnedWithinTheReconnectLimit(t *testing.T) {
	p, conn := childPool(t, "mortal", mcp.ServerConfig{Name: "mortal", PerSessionReconnectLimit: 1})
	ctx := context.Background()
	tools, err := p.Tools(ctx, nil)
	must(t, err)
	tool := func(name string) core.Tool {
		for _, tl := range tools {
			if tl.Name == "mortal__"+name {
				return tl
			}
		}
		t.Fatalf("no tool %q in %v", name, tools)
		return core.Tool{}
	}

	// 1. The server dies DURING a call: an is_error result, not a Go error.
	if r := tool("crash").Execute(ctx, nil); r.OK {
		t.Fatal("a call cut off by the server's exit must be an error result")
	}
	waitFor(t, "the client to notice the exit", func() bool { return !conn.Alive() })

	// 2. The next call re-spawns the server and succeeds.
	if r := tool("hello").Execute(ctx, nil); !r.OK {
		t.Fatalf("the call after a death must re-spawn the server: %+v", r)
	}
	if conn.Reconnects() != 1 || !conn.Alive() {
		t.Fatalf("reconnects = %d, alive = %v; want 1 and alive", conn.Reconnects(), conn.Alive())
	}

	// 3. It dies again, this time AFTER answering, and the budget is spent.
	if r := tool("die").Execute(ctx, nil); !r.OK {
		t.Fatalf("die must answer before exiting: %+v", r)
	}
	waitFor(t, "the client to notice the second exit", func() bool { return !conn.Alive() })

	r := tool("hello").Execute(ctx, nil)
	if r.OK {
		t.Fatal("past the reconnect limit the call must fail")
	}
	if !strings.Contains(r.Detail, "reconnect limit") {
		t.Fatalf("the result must say why: %+v", r)
	}
	if !conn.Dead() || conn.Reconnects() != 1 {
		t.Fatalf("dead = %v, reconnects = %d; the limit must not be exceeded", conn.Dead(), conn.Reconnects())
	}
	// And it stays dead: no further spawn is attempted.
	if r := tool("hello").Execute(ctx, nil); r.OK || conn.Reconnects() != 1 {
		t.Fatalf("a dead connection must fail fast without re-spawning: %+v, reconnects = %d",
			r, conn.Reconnects())
	}
}

// TestAnOversizedStderrLineDoesNotWedgeTheServer. A child that logs a line
// longer than the stderr scanner buffers must keep being drained, or its next
// stderr write blocks and its stdout frames stop with it; the caller is told
// once why its diagnostics stopped.
func TestAnOversizedStderrLineDoesNotWedgeTheServer(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	var mu sync.Mutex
	var warnings []string
	p := mcp.NewPool(mcp.ConnectionOptions{
		Warnf: func(f string, a ...any) {
			mu.Lock()
			warnings = append(warnings, fmt.Sprintf(f, a...))
			mu.Unlock()
		},
	})
	t.Cleanup(func() { _ = p.Close() })
	conn, err := p.Connect(context.Background(), mcp.ServerConfig{Name: "flood", Command: exe},
		[]string{"AGENTKIT_MCP_CHILD=stderr-flood"}, nil)
	if err != nil {
		t.Fatalf("connect: %v — the child wrote a 2 MiB stderr line before serving, and did "+
			"not survive it", err)
	}
	res, err := conn.Call(context.Background(), "hello", nil)
	must(t, err)
	if firstText(t, res) != "hi" {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, w := range warnings {
		if strings.Contains(w, "stderr line exceeded") {
			return
		}
	}
	t.Fatalf("the caller was never told its diagnostics stopped: %q", warnings)
}

// ---- 06-REQ-2: output schemas imported from a server

// outputSchemaServer exposes three tools: calc declares a valid outputSchema,
// broken declares one that is not a schema at all, simple declares none.
// Every one of them answers a call, so a test can tell an adapted tool that
// still works from one that was dropped.
func outputSchemaServer(t *testing.T) *mcp.Server {
	t.Helper()
	s := mcp.NewServer(mcp.ServerOptions{Info: mcp.Implementation{Name: "schema-server", Version: "1"}})
	ok := func(context.Context, map[string]any) (*mcp.CallToolResult, error) { return text("ok"), nil }
	must(t, s.RegisterTool(&mcp.Tool{
		Name: "calc",
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"number"},` +
			`"unit":{"type":"string"}},"required":["value"]}`),
	}, ok))
	must(t, s.RegisterTool(&mcp.Tool{Name: "broken", OutputSchema: "not a valid schema object"}, ok))
	must(t, s.RegisterTool(&mcp.Tool{Name: "simple"}, ok))
	return s
}

func schemaPool(t *testing.T, srv *mcp.Server) (*mcp.Pool, map[string]core.Tool) {
	t.Helper()
	p := mcp.NewPool(mcp.ConnectionOptions{})
	must(t, p.Add(pair(t, srv, mcp.ServerConfig{Name: "srv"}, mcp.ConnectionOptions{})))
	t.Cleanup(func() { _ = p.Close() })
	tools, err := p.Tools(context.Background(), nil)
	must(t, err)
	byName := map[string]core.Tool{}
	for _, tl := range tools {
		byName[tl.Name] = tl
	}
	return p, byName
}

// TS-06-4: a valid outputSchema becomes the adapted tool's OutputSchema.
func TestAServerOutputSchemaIsImported_TS06_4(t *testing.T) {
	_, tools := schemaPool(t, outputSchemaServer(t))
	out := tools["srv__calc"].OutputSchema
	if out == nil {
		t.Fatal("srv__calc has no OutputSchema; the server declared one")
	}
	if out.Type != schema.TypeObject {
		t.Fatalf("OutputSchema.Type = %q, want object", out.Type)
	}
	if v := out.Properties["value"]; v == nil || v.Type != schema.TypeNumber {
		t.Fatalf("value = %+v, want a number property", v)
	}
	if u := out.Properties["unit"]; u == nil || u.Type != schema.TypeString {
		t.Fatalf("unit = %+v, want a string property", u)
	}
	if !out.IsRequired("value") || out.IsRequired("unit") {
		t.Fatalf("required = %v, want [value]", out.Required)
	}
}

// TS-06-5: a malformed outputSchema is dropped with a SeverityError diagnostic
// naming the server and the tool; the tool is still imported and callable.
func TestAMalformedOutputSchemaIsDroppedWithADiagnostic_TS06_5(t *testing.T) {
	p, tools := schemaPool(t, outputSchemaServer(t))
	broken, ok := tools["srv__broken"]
	if !ok {
		t.Fatal("srv__broken was not imported; a bad output schema must not cost the tool")
	}
	if broken.OutputSchema != nil {
		t.Fatalf("OutputSchema = %+v, want nil for a malformed declaration", broken.OutputSchema)
	}
	var found bool
	for _, d := range p.Diagnostics() {
		if d.Severity == diag.SeverityError && strings.Contains(d.Message, `"srv"`) &&
			strings.Contains(d.Message, `"broken"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("diagnostics = %+v, want a SeverityError naming server srv and tool broken", p.Diagnostics())
	}
	if res := broken.Execute(context.Background(), json.RawMessage(`{}`)); !res.OK {
		t.Fatalf("srv__broken call = %+v; the connection must stay usable", res)
	}
}

// TS-06-6: a tool with no outputSchema gets nil and adds no diagnostic.
func TestAnOmittedOutputSchemaIsNilWithoutDiagnostics_TS06_6(t *testing.T) {
	s := mcp.NewServer(mcp.ServerOptions{Info: mcp.Implementation{Name: "plain", Version: "1"}})
	must(t, s.RegisterTool(&mcp.Tool{Name: "simple"},
		func(context.Context, map[string]any) (*mcp.CallToolResult, error) { return text("ok"), nil }))
	p, tools := schemaPool(t, s)
	if tl, ok := tools["srv__simple"]; !ok || tl.OutputSchema != nil {
		t.Fatalf("srv__simple = %+v (found %v), want an imported tool with nil OutputSchema", tl.OutputSchema, ok)
	}
	if d := p.Diagnostics(); len(d) != 0 {
		t.Fatalf("diagnostics = %+v, want none", d)
	}
}

// A non-object output schema describes a structuredContent the adapted tool
// returns as Data {"value": ...}, so the imported schema is wrapped the same
// way and still describes Data. A server's array or number schema is valid
// JSON Schema and must not be reported as malformed.
func TestANonObjectOutputSchemaDescribesTheWrappedValue(t *testing.T) {
	s := mcp.NewServer(mcp.ServerOptions{Info: mcp.Implementation{Name: "n", Version: "1"}})
	must(t, s.RegisterTool(&mcp.Tool{Name: "count", OutputSchema: json.RawMessage(`{"type":"integer"}`)},
		func(context.Context, map[string]any) (*mcp.CallToolResult, error) { return text("1"), nil }))
	p, tools := schemaPool(t, s)
	out := tools["srv__count"].OutputSchema
	if out == nil || out.Type != schema.TypeObject || !out.IsRequired("value") ||
		out.Properties["value"].Type != schema.TypeInteger {
		t.Fatalf("OutputSchema = %+v, want object{value: integer}", out)
	}
	if d := p.Diagnostics(); len(d) != 0 {
		t.Fatalf("diagnostics = %+v, want none for a valid schema", d)
	}
}

// ---- 06-REQ-3: structuredContent becomes Data

// resultTool imports one tool whose every call answers with res, and returns
// it adapted.
func resultTool(t *testing.T, res *mcp.CallToolResult) core.Tool {
	t.Helper()
	s := mcp.NewServer(mcp.ServerOptions{Info: mcp.Implementation{Name: "r", Version: "1"}})
	must(t, s.RegisterTool(&mcp.Tool{Name: "r"},
		func(context.Context, map[string]any) (*mcp.CallToolResult, error) { return res, nil }))
	_, tools := schemaPool(t, s)
	tl, ok := tools["srv__r"]
	if !ok {
		t.Fatal("srv__r was not imported")
	}
	return tl
}

// TS-06-7: an object structuredContent IS Data, not nested under a key.
func TestObjectStructuredContentIsData_TS06_7(t *testing.T) {
	tl := resultTool(t, &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: "72.5 F"}},
		StructuredContent: map[string]any{"temperature": 72.5, "units": "F"},
	})
	res := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.OK {
		t.Fatalf("call = %+v", res)
	}
	want := map[string]any{"temperature": 72.5, "units": "F"}
	if !reflect.DeepEqual(res.Data, want) {
		t.Fatalf("Data = %#v, want %#v", res.Data, want)
	}
}

// TS-06-8: a non-object structuredContent is Data {"value": ...}.
func TestNonObjectStructuredContentIsWrapped_TS06_8(t *testing.T) {
	for _, tc := range []struct {
		name string
		sent any
		want any
	}{
		{"number", 42, float64(42)},
		{"string", "done", "done"},
		{"array", []any{"a", 1}, []any{"a", float64(1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := resultTool(t, &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: "x"}},
				StructuredContent: tc.sent,
			})
			res := tl.Execute(context.Background(), json.RawMessage(`{}`))
			want := map[string]any{"value": tc.want}
			if !res.OK || !reflect.DeepEqual(res.Data, want) {
				t.Fatalf("result = %+v, want OK with Data %#v", res, want)
			}
		})
	}
}

// TS-06-9: without structuredContent, Data keeps the joined text and the raw
// content blocks.
func TestNoStructuredContentFallsBackToTextAndContent_TS06_9(t *testing.T) {
	tl := resultTool(t, &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "hello "}, &mcp.TextContent{Text: "world"}},
	})
	res := tl.Execute(context.Background(), json.RawMessage(`{}`))
	if !res.OK {
		t.Fatalf("call = %+v", res)
	}
	if res.Data["text"] != "hello world" {
		t.Fatalf("Data[text] = %#v, want %q", res.Data["text"], "hello world")
	}
	content, ok := res.Data["content"].([]json.RawMessage)
	if !ok || len(content) != 2 || !strings.Contains(string(content[0]), `"hello "`) {
		t.Fatalf("Data[content] = %#v, want the two raw blocks", res.Data["content"])
	}
	if _, nested := res.Data["structured"]; nested {
		t.Fatal("Data carries a structured key with no structuredContent")
	}
}

// TS-06-10: for any mix of content blocks, Text is the text blocks joined in
// order — what the model reads.
func TestTextIsTheJoinedTextBlocks_TS06_10(t *testing.T) {
	r := rand.New(rand.NewSource(10))
	words := []string{"alpha", " ", "β\n", "{\"json\":1}", "line\nbreak", "tab\t"}
	for i := range 30 {
		var blocks []mcp.Content
		var want strings.Builder
		for range 1 + r.Intn(5) {
			if r.Intn(3) == 0 {
				blocks = append(blocks, &sdk.ImageContent{Data: []byte{0x89, 'P', 'N', 'G'}, MIMEType: "image/png"})
				continue
			}
			w := words[r.Intn(len(words))]
			blocks = append(blocks, &mcp.TextContent{Text: w})
			want.WriteString(w)
		}
		var structured any
		if r.Intn(2) == 0 {
			structured = map[string]any{"i": float64(i)}
		}
		tl := resultTool(t, &mcp.CallToolResult{Content: blocks, StructuredContent: structured})
		res := tl.Execute(context.Background(), json.RawMessage(`{}`))
		if !res.OK || res.Text != want.String() {
			t.Fatalf("iteration %d: Text = %q (OK %v), want %q", i, res.Text, res.OK, want.String())
		}
	}
}
