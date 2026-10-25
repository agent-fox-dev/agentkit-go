package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// countSDK wraps the SDK handler for the test and counts the requests it sees.
func countSDK(t *testing.T) *atomic.Int32 {
	var n atomic.Int32
	old := newSDKHandler
	newSDKHandler = func(srv *sdk.Server) http.Handler {
		inner := old(srv)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { n.Add(1); inner.ServeHTTP(w, r) })
	}
	t.Cleanup(func() { newSDKHandler = old })
	return &n
}

func TestServeLoopbackListensOnNothingWhenItsContextIsDoneOrItsListenOrTokenFails(t *testing.T) {
	oldListen, oldRand := listenLoopback, randomSource
	defer func() { listenLoopback, randomSource = oldListen, oldRand }()
	s, _ := serverOf(t, Options{})
	var listened []string
	listenLoopback = func() (net.Listener, error) {
		l, err := oldListen()
		listened = append(listened, l.Addr().String())
		return l, err
	}
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if ep, err := s.ServeLoopback(done); ep != nil || err != context.Canceled || len(listened) != 0 {
		t.Errorf("a done ctx: %v, %v after %d listens; want nil, context.Canceled and none", ep, err, len(listened))
	}
	randomSource = iotest.ErrReader(errors.New("no entropy today"))
	if ep, err := s.ServeLoopback(context.Background()); ep != nil || err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") || len(listened) != 1 || !refused(listened[0]) {
		t.Errorf("a failed token: %v, %v, and %v left listening; want nil, an mcpserve error and the listener closed", ep, err, listened)
	}
	listenLoopback = func() (net.Listener, error) { return nil, errors.New("no sockets today") }
	if ep, err := s.ServeLoopback(context.Background()); ep != nil || err == nil || !strings.HasPrefix(err.Error(), "mcpserve: ") {
		t.Errorf("a failed listen: %v, %v; want nil and an mcpserve error", ep, err)
	}
}

// Each unauthorized request declares a 1 MB body and sends none, so a server that read it first would not answer.
// ("Bearer <token> " is authorized: net/http strips a trailing blank from a header value.)
func TestRequestsTheEndpointRefusesNeverReachTheSDK(t *testing.T) {
	n := countSDK(t)
	s, rec := serverOf(t, Options{})
	ep := serveLoop(t, s)
	for name, auth := range map[string]string{
		"no header":       "",
		"wrong token":     "Authorization: Bearer " + strings.Repeat("0", 48) + "\r\n",
		"lower case":      "Authorization: bearer " + ep.Token + "\r\n",
		"extra character": "Authorization: Bearer " + ep.Token + "x\r\n",
		"inner blank":     "Authorization: Bearer  " + ep.Token + "\r\n",
		"two headers":     strings.Repeat("Authorization: Bearer "+ep.Token+"\r\n", 2),
	} {
		for _, target := range []string{"POST /mcp", "GET /mcp", "DELETE /other"} {
			c := must(net.Dial("tcp", strings.TrimSuffix(strings.TrimPrefix(ep.URL, "http://"), endpointPath)))
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.WriteString(c, target+" HTTP/1.1\r\nHost: x\r\nContent-Length: 1000000\r\n"+auth+"\r\n")
			res, err := http.ReadResponse(bufio.NewReader(c), nil)
			if err != nil || res.StatusCode != http.StatusUnauthorized || res.Header.Get("WWW-Authenticate") != "Bearer" ||
				string(must(io.ReadAll(res.Body))) != "unauthorized" {
				t.Errorf("%s (%s): %v, %v; want 401, WWW-Authenticate Bearer and unauthorized", target, name, res, err)
			}
			_ = c.Close()
		}
	}
	for name, c := range map[string]struct {
		path, body string
		status     int
	}{
		"the root":                {"/", "{}", http.StatusNotFound},
		"a path below /mcp":       {"/mcp/x", "{}", http.StatusNotFound},
		"/mcp/":                   {"/mcp/", "{}", http.StatusNotFound},
		"one byte over the limit": {"/mcp", strings.Repeat("a", int(wire.Defaults().MaxMessageBytes)+1), http.StatusRequestEntityTooLarge},
		"a duplicate key":         {"/mcp", `{"a":1,"a":2}`, http.StatusBadRequest},
		"too deep":                {"/mcp", strings.Repeat("[", 70) + strings.Repeat("]", 70), http.StatusBadRequest},
		"bad syntax":              {"/mcp", `{`, http.StatusBadRequest},
	} {
		if r := post(strings.TrimSuffix(ep.URL, endpointPath)+c.path, "Bearer "+ep.Token, c.body); r.StatusCode != c.status {
			t.Errorf("authorized, %s: %d, want %d", name, r.StatusCode, c.status)
		}
	}
	if n.Load() != 0 || len(rec.results()) != 0 {
		t.Errorf("refused requests reached the SDK %d times and the runner %d times", n.Load(), len(rec.results()))
	}
}

// The endpoint has a standalone event stream open, which never ends on its
// own, and a call in flight, which sees its context end.
func TestCloseOrTheEndOfTheContextStopsTheEndpointAndCancelsItsCalls(t *testing.T) {
	for _, byClose := range []bool{true, false} {
		started, observed := make(chan struct{}, 1), make(chan error, 1)
		s, rec := serverOf(t, Options{}, blockingTool(started, observed))
		ctx, cancel := context.WithCancel(context.Background())
		ep := must(s.ServeLoopback(ctx))
		auth := "Bearer " + ep.Token
		id := post(ep.URL, auth, initFrame).Header.Get("Mcp-Session-Id")
		post(ep.URL, auth, readyFrame, "Mcp-Session-Id", id)
		get, _ := http.NewRequest(http.MethodGet, ep.URL, nil)
		get.Header = http.Header{"Authorization": {auth}, "Mcp-Session-Id": {id}, "Accept": {"text/event-stream"}}
		stream := must(httpClient.Do(get))
		defer stream.Body.Close()
		go dial(t, ep)[0].Execute(context.Background(), json.RawMessage(`{}`))
		<-started
		if byClose {
			_ = ep.Close()
		} else {
			cancel()
		}
		if !testutil.Within(3*time.Second, func() bool { return refused(ep.URL) }) || <-observed == nil || stream.StatusCode != http.StatusOK {
			t.Errorf("by Close %v: with a stream open (%d), the port still accepts connections or the call's context did not end", byClose, stream.StatusCode)
		}
		testutil.Eventually(t, "the runner's result", func() bool { return len(rec.results()) == 1 })
		if got := rec.results()[0].Error; got != "aborted" {
			t.Errorf("by Close %v: the runner answered %q, want aborted", byClose, got)
		}
		cancel()
	}
}

func TestEndpointsAreOnLoopbackWithTheirOwnTokenAndCloseIdempotently(t *testing.T) {
	s, _ := serverOf(t, Options{})
	a, b := serveLoop(t, s), must(s.ServeLoopback(context.Background()))
	re := regexp.MustCompile(`^http://127\.0\.0\.1:[0-9]+/mcp [0-9a-f]{48}$`)
	if !re.MatchString(a.URL+" "+a.Token) || a.URL == b.URL || a.Token == b.Token || post(b.URL, "Bearer "+a.Token, initFrame).StatusCode != http.StatusUnauthorized {
		t.Fatalf("endpoints %+v and %+v share a port or a token, or are not on loopback", a, b)
	}
	for range 16 {
		go func() { _ = b.Close() }()
	}
	for _, e := range []*Endpoint{b, {}, {URL: "x", Token: "y"}, nil} {
		if err := e.Close(); err != nil {
			t.Errorf("Close on %+v returned %v", e, err)
		}
	}
	if !refused(b.URL) || post(a.URL, "Bearer "+a.Token, initFrame).StatusCode != http.StatusOK {
		t.Error("B still accepts connections, or A stopped serving when B closed")
	}
}
