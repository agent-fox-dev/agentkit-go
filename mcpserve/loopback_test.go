package mcpserve

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tests that swap the package's seams (the listener, the random source and
// the SDK handler) are package state, so none of them runs in parallel.

// countSDK wraps the SDK handler so a test can tell whether a request got to it.
func countSDK(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	old := newSDKHandler
	newSDKHandler = func(srv *sdk.Server) http.Handler {
		inner := old(srv)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			inner.ServeHTTP(w, r)
		})
	}
	t.Cleanup(func() { newSDKHandler = old })
	return &n
}

// serveLoop serves s on a loopback endpoint that closes with the test.
func serveLoop(t *testing.T, s *Server) *Endpoint {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ep, err := s.ServeLoopback(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ep.Close()
		cancel()
	})
	return ep
}

var httpClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

// reply is what a request got back.
type reply struct {
	status int
	header http.Header
	body   string
}

// send makes one request. auth is the Authorization header, or "" for none.
func send(t *testing.T, method, url, auth string, body io.Reader) reply {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return reply{res.StatusCode, res.Header, string(b)}
}

// at is the endpoint's base URL with path replacing /mcp.
func at(ep *Endpoint, path string) string { return strings.TrimSuffix(ep.URL, "/mcp") + path }

// TS-23-4: every error New, ServeStdio and ServeLoopback return, other than a
// context's, starts "mcpserve: ". 23-REQ-1.4.
func TestErrorsAreMcpservePrefixed_TS23_4(t *testing.T) {
	var errs []error
	_, err := New(nil, Options{})
	errs = append(errs, err)
	_, err = New(runnerOf(t, textTool("bad", "x", schema.String())), Options{})
	errs = append(errs, err)

	toServer, _ := stdio(t)
	s, _ := echoServer(t, Options{})
	done := serving(context.Background(), s)
	go func() { _, _ = io.WriteString(toServer, `{"a":1,"a":2}`+"\n") }()
	errs = append(errs, within(t, 10*time.Second, done, "ServeStdio on a bad frame"))

	oldListen, oldRand := listenLoopback, randomSource
	defer func() { listenLoopback, randomSource = oldListen, oldRand }()
	listenLoopback = func() (net.Listener, error) { return nil, errors.New("no sockets today") }
	_, err = s.ServeLoopback(context.Background())
	errs = append(errs, err)
	listenLoopback = oldListen
	randomSource = failingReader{}
	_, err = s.ServeLoopback(context.Background())
	errs = append(errs, err)

	for i, err := range errs {
		if err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") {
			t.Errorf("error %d is %v, want one starting %q", i, err, "mcpserve: ")
		}
	}
}

// failingReader is a random source that fails.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("no entropy today") }

// TS-23-27: ServeLoopback with a done ctx returns nil and ctx.Err() and listens
// on nothing. 23-REQ-5.1.
func TestDoneContextListensOnNothing_TS23_27(t *testing.T) {
	old := listenLoopback
	defer func() { listenLoopback = old }()
	var listened atomic.Int32
	listenLoopback = func() (net.Listener, error) {
		listened.Add(1)
		return old()
	}
	s, _ := echoServer(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ep, err := s.ServeLoopback(ctx)
	if ep != nil || err != context.Canceled {
		t.Errorf("ServeLoopback = %v, %v; want nil and context.Canceled", ep, err)
	}
	if n := listened.Load(); n != 0 {
		t.Errorf("a done ctx opened %d listeners, want none", n)
	}
}

var (
	urlRE   = regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/mcp$`)
	tokenRE = regexp.MustCompile(`^[0-9a-f]{48}$`)
)

// TS-23-28: ServeLoopback listens on 127.0.0.1 and returns the URL and a fresh
// token of 48 lowercase hex characters. 23-REQ-5.2.
func TestLoopbackURLAndToken_TS23_28(t *testing.T) {
	s, _ := echoServer(t, Options{})
	a, b := serveLoop(t, s), serveLoop(t, s)
	for _, ep := range []*Endpoint{a, b} {
		if !urlRE.MatchString(ep.URL) || !tokenRE.MatchString(ep.Token) {
			t.Errorf("endpoint %q with token %q", ep.URL, ep.Token)
		}
		host := strings.TrimSuffix(strings.TrimPrefix(ep.URL, "http://"), "/mcp")
		c, err := net.DialTimeout("tcp", host, 2*time.Second)
		if err != nil {
			t.Errorf("nothing listens on %s: %v", host, err)
			continue
		}
		_ = c.Close()
	}
	if a.Token == b.Token {
		t.Error("two endpoints share a token: it is not fresh for each call")
	}
}

// TS-23-29: a listen or random-source failure returns an mcpserve error and
// leaves no listener. 23-REQ-5.3.
func TestListenAndRandomFailures_TS23_29(t *testing.T) {
	oldListen, oldRand := listenLoopback, randomSource
	defer func() { listenLoopback, randomSource = oldListen, oldRand }()
	s, _ := echoServer(t, Options{})

	listenLoopback = func() (net.Listener, error) { return nil, errors.New("no sockets today") }
	if ep, err := s.ServeLoopback(context.Background()); ep != nil || err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") {
		t.Errorf("a listen failure gave %v, %v; want nil and an mcpserve error", ep, err)
	}

	var opened string
	listenLoopback = func() (net.Listener, error) {
		l, err := oldListen()
		if err == nil {
			opened = l.Addr().String()
		}
		return l, err
	}
	randomSource = failingReader{}
	if ep, err := s.ServeLoopback(context.Background()); ep != nil || err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") {
		t.Errorf("a random-source failure gave %v, %v; want nil and an mcpserve error", ep, err)
	}
	if opened == "" {
		t.Fatal("the listener was never opened: the random source failed first")
	}
	if c, err := net.DialTimeout("tcp", opened, time.Second); err == nil {
		_ = c.Close()
		t.Errorf("%s still accepts connections after the failure", opened)
	}
}

// rawSend sends the headers of a request that declares a 1 MB body and sends no
// body at all, then reads the reply. A server that tried to read the body
// before answering would wait here until the deadline.
func rawSend(t *testing.T, ep *Endpoint, method, path, auth string) reply {
	t.Helper()
	host := strings.TrimSuffix(strings.TrimPrefix(ep.URL, "http://"), "/mcp")
	c, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	req := method + " " + path + " HTTP/1.1\r\nHost: " + host + "\r\nContent-Type: application/json\r\nContent-Length: 1000000\r\n"
	if auth != "" {
		req += "Authorization: " + auth + "\r\n"
	}
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Errorf("%s %s with %q: no reply before the body was sent: %v", method, path, auth, err)
		return reply{}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return reply{res.StatusCode, res.Header, string(b)}
}

// TS-23-30 (integration): a request without the exact bearer token gets 401,
// WWW-Authenticate and "unauthorized" before its body is read, whatever its path
// or method. 23-REQ-5.4. The spec also lists "Bearer <token> " with a trailing
// space; net/http strips trailing whitespace from a header value, so the handler
// receives exactly "Bearer <token>" and the request is authorized
// (docs/errata/23_mcp_serve.md).
func TestUnauthorizedRequestsAreRefusedFirst_TS23_30(t *testing.T) {
	n := countSDK(t)
	s, rec := echoServer(t, Options{})
	ep := serveLoop(t, s)
	for _, path := range []string{"/mcp", "/other"} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
			for name, auth := range map[string]string{
				"no header":       "",
				"wrong token":     "Bearer " + strings.Repeat("0", 48),
				"lower case":      "bearer " + ep.Token,
				"extra character": "Bearer " + ep.Token + "x",
				"inner blank":     "Bearer  " + ep.Token,
				"two headers":     "Bearer " + ep.Token + "\r\nAuthorization: Bearer " + ep.Token,
			} {
				r := rawSend(t, ep, method, path, auth)
				if r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != "Bearer" || r.body != "unauthorized" {
					t.Errorf("%s %s (%s): %d %q %q, want 401, Bearer and %q", method, path, name, r.status, r.header.Get("WWW-Authenticate"), r.body, "unauthorized")
				}
			}
		}
	}
	if got := n.Load(); got != 0 {
		t.Errorf("the SDK handler saw %d refused requests", got)
	}
	if got := len(rec.seen()); got != 0 {
		t.Errorf("the runner saw %d calls", got)
	}
}

// TS-23-31 (integration): an authorized request for a path other than /mcp gets
// 404. 23-REQ-5.5.
func TestOtherPathsAre404_TS23_31(t *testing.T) {
	n := countSDK(t)
	s, _ := echoServer(t, Options{})
	ep := serveLoop(t, s)
	for _, path := range []string{"/", "/mcp/x", "/health", "/mcp/"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if r := send(t, method, at(ep, path), "Bearer "+ep.Token, strings.NewReader("{}")); r.status != http.StatusNotFound {
				t.Errorf("%s %s: %d, want 404", method, path, r.status)
			}
		}
	}
	if got := n.Load(); got != 0 {
		t.Errorf("the SDK handler saw %d requests for other paths", got)
	}
}

// TS-23-32 (integration): an authorized body over MaxMessageBytes gets 413 and
// is not served. 23-REQ-5.6.
func TestOversizeBodyIs413_TS23_32(t *testing.T) {
	n := countSDK(t)
	s, _ := echoServer(t, Options{})
	ep := serveLoop(t, s)
	body := bytes.Repeat([]byte("a"), int(wire.Defaults().MaxMessageBytes)+1)
	if r := send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token, bytes.NewReader(body)); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("a body one byte over the limit: %d, want 413", r.status)
	}
	if got := n.Load(); got != 0 {
		t.Errorf("the SDK handler saw %d oversize requests", got)
	}
}

// TS-23-33 (integration): an authorized body that wire.Guard rejects gets 400
// and is not served. 23-REQ-5.7.
func TestRejectedBodyIs400_TS23_33(t *testing.T) {
	n := countSDK(t)
	s, _ := echoServer(t, Options{})
	ep := serveLoop(t, s)
	for name, body := range map[string]string{
		"duplicate key": `{"a":1,"a":2}`,
		"too deep":      strings.Repeat("[", 70) + strings.Repeat("]", 70),
		"bad syntax":    `{`,
	} {
		if r := send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token, strings.NewReader(body)); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, r.status)
		}
	}
	if got := n.Load(); got != 0 {
		t.Errorf("the SDK handler saw %d rejected bodies", got)
	}
}

// sseData returns the JSON of the first data line of an event stream, or body
// itself when it is plain JSON.
func sseData(t *testing.T, body string) []byte {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
			return []byte(strings.TrimSpace(v))
		}
	}
	return []byte(body)
}

// TS-23-34 (integration): an authorized initialize gets 200 and a session id,
// the SDK serves the session's later requests, and a request with no body is
// passed on rather than refused. 23-REQ-5.8.
func TestAuthorizedRequestsReachTheSDK_TS23_34(t *testing.T) {
	n := countSDK(t)
	s, _ := echoServer(t, Options{})
	ep := serveLoop(t, s)
	auth := "Bearer " + ep.Token

	r := send(t, http.MethodPost, ep.URL, auth, strings.NewReader(initFrame))
	session := r.header.Get("Mcp-Session-Id")
	if r.status != http.StatusOK || session == "" {
		t.Fatalf("initialize: %d with session %q: %s", r.status, session, r.body)
	}
	post := func(frame string) reply {
		req, err := http.NewRequest(http.MethodPost, ep.URL, strings.NewReader(frame))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Session-Id", session)
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return reply{res.StatusCode, res.Header, string(b)}
	}
	if r := post(readyFrame); r.status/100 != 2 {
		t.Fatalf("initialized: %d %s", r.status, r.body)
	}
	r = post(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var list struct {
		Result struct {
			Tools []struct{ Name string } `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(sseData(t, r.body), &list); err != nil || len(list.Result.Tools) != 1 || list.Result.Tools[0].Name != "echo" {
		t.Fatalf("tools/list in the session: %d %q (%v)", r.status, r.body, err)
	}

	before := n.Load()
	if r := send(t, http.MethodPost, ep.URL, auth, nil); r.status == http.StatusUnauthorized || r.status == http.StatusNotFound {
		t.Errorf("a POST with no body was refused by mcpserve: %d", r.status)
	}
	if n.Load() == before {
		t.Error("a POST with no body never reached the SDK handler")
	}
}

// TS-23-46 (integration): each refused request emits one Warn record with the
// peer and a reason that tells the cases apart. 23-REQ-8.3.
func TestRefusalsAreLoggedOnce_TS23_46(t *testing.T) {
	logs := newCapture()
	s, _ := echoServer(t, Options{Logger: slog.New(logs)})
	ep := serveLoop(t, s)
	send(t, http.MethodPost, ep.URL, "Bearer wrong", strings.NewReader("{}"))
	send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token, bytes.NewReader(bytes.Repeat([]byte("a"), int(wire.Defaults().MaxMessageBytes)+1)))
	send(t, http.MethodPost, ep.URL, "Bearer "+ep.Token, strings.NewReader(`{"a":1,"a":2}`))

	var warns []slog.Record
	for _, r := range logs.records() {
		if r.Level == slog.LevelWarn {
			warns = append(warns, r)
		}
	}
	if len(warns) != 3 {
		t.Fatalf("%d Warn records, want one per refusal", len(warns))
	}
	var reasons []string
	for _, w := range warns {
		a := attrsOf(w)
		if p := a["peer"].String(); !strings.HasPrefix(p, "127.0.0.1:") {
			t.Errorf("peer %q, want the client's loopback address", p)
		}
		reasons = append(reasons, a["reason"].String())
	}
	slices.Sort(reasons)
	if reasons = slices.Compact(reasons); len(reasons) != 3 || slices.Contains(reasons, "") {
		t.Errorf("reasons %q: want three different ones", reasons)
	}
}

// TS-23-34, the server's settings: the HTTP server gives up on a client that
// does not finish its headers within headerTimeout, and the contexts its
// handlers run with derive from the ctx the endpoint was served with.
// 23-REQ-5.8.
func TestServerTimeoutAndContext_TS23_34(t *testing.T) {
	old := headerTimeout
	headerTimeout = 300 * time.Millisecond
	defer func() { headerTimeout = old }()
	oldHandler := newSDKHandler
	defer func() { newSDKHandler = oldHandler }()
	started, release := make(chan context.Context, 1), make(chan struct{})
	newSDKHandler = func(*sdk.Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started <- r.Context()
			<-release
		})
	}
	s, _ := echoServer(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ep, err := s.ServeLoopback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	// A client that sends part of its headers and stops is dropped.
	host := strings.TrimSuffix(strings.TrimPrefix(ep.URL, "http://"), "/mcp")
	c, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(c, "POST /mcp HTTP/1.1\r\nHost: "+host+"\r\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(c); err != nil {
		t.Errorf("the server held a client with incomplete headers for over 3s: %v", err)
	}

	// A request in flight sees its context end with ctx's.
	reqDone := make(chan struct{})
	go func() {
		defer close(reqDone)
		poke(ep, strings.NewReader(initFrame))
	}()
	var reqCtx context.Context
	select {
	case reqCtx = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	if reqCtx.Err() != nil {
		t.Fatalf("the request's context is already done: %v", reqCtx.Err())
	}
	cancel()
	// Well inside the two seconds the endpoint's own stop waits before it
	// closes connections, which would end the context too.
	select {
	case <-reqCtx.Done():
	case <-time.After(time.Second):
		t.Error("the request's context did not end with the ctx the endpoint was served with")
	}
	close(release)
	<-reqDone
}
