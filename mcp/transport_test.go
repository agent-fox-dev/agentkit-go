package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/wire"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// cannedRoundTripper answers every request with one response.
type cannedRoundTripper struct {
	contentType, body string
	seen              *http.Request
}

func (f *cannedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = r
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {f.contentType}},
		Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

// The SDK's decoder takes the last of two duplicate keys and bounds nothing we
// chose, so a JSON body and each SSE event's data are checked before it reads
// them; a valid body passes through unchanged.
func TestHTTPResponseBodiesAreHeldToTheWireRules(t *testing.T) {
	ok := `{"jsonrpc":"2.0","id":1,"result":{}}`
	dup := `{"jsonrpc":"2.0","id":1,"result":{},"result":{"x":1}}`
	small := wire.Limits{MaxMessageBytes: 512}
	big := `{"jsonrpc":"2.0","id":1,"result":{"pad":"` + strings.Repeat("a", 4096) + `"}}`

	for _, tc := range []struct {
		name, contentType, body string
		limits                  wire.Limits
		wantErr                 bool
	}{
		{"valid json", "application/json", ok, wire.Limits{}, false},
		{"duplicate key json", "application/json", dup, wire.Limits{}, true},
		{"oversized json", "application/json", big, small, true},
		{"other content types untouched", "text/plain", dup, wire.Limits{}, false},
		{"valid sse", "text/event-stream", "event: message\ndata: " + ok + "\n\n", wire.Limits{}, false},
		{"duplicate key sse", "text/event-stream", "data: " + ok + "\n\ndata: " + dup + "\n\n", wire.Limits{}, true},
		{"oversized sse", "text/event-stream", "data: " + big + "\n\n", small, true},
		{"data split over lines", "text/event-stream", "data: {\"a\":\ndata: 1,\"a\":2}\n\n", wire.Limits{}, true},
		{"unterminated event", "text/event-stream", "data: " + dup, wire.Limits{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &strictRoundTripper{base: &cannedRoundTripper{contentType: tc.contentType, body: tc.body},
				limits: tc.limits}
			req, _ := http.NewRequest(http.MethodPost, "http://x/", nil)
			var out []byte
			resp, err := rt.RoundTrip(req)
			if err == nil {
				out, err = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && bytes.Contains(out, []byte(`"x":1`)) {
				t.Fatalf("the rejected event was released to the reader: %q", out)
			}
			if !tc.wantErr && string(out) != tc.body {
				t.Fatalf("a valid body must pass through unchanged: %q", out)
			}
		})
	}
}

func TestTheRoundTripperAddsTheHeadersWithoutTouchingTheCallersRequest(t *testing.T) {
	base := &cannedRoundTripper{contentType: "application/json", body: `{}`}
	rt := &strictRoundTripper{base: base, headers: map[string]string{"Authorization": "Bearer t"}}
	req, _ := http.NewRequest(http.MethodPost, "http://x/", nil)
	resp, err := rt.RoundTrip(req)
	must(t, err)
	_ = resp.Body.Close()
	if base.seen.Header.Get("Authorization") != "Bearer t" || req.Header.Get("Authorization") != "" {
		t.Fatalf("sent %v; caller's request %v", base.seen.Header, req.Header)
	}
}

// An event is released whole, after its blank line, so the SDK never reads a
// prefix of an event that is later rejected; lines longer than the reader's
// buffer are joined, and an event past the bound fails before it is buffered.
func TestTheSSEGuardReleasesWholeEvents(t *testing.T) {
	src := "id: 1\ndata: {\"a\":1,\"padding\":\"0123456789\"}\n\ndata: {\"b\":2}\r\n\r\n"
	g := &sseGuard{src: bufio.NewReaderSize(strings.NewReader(src), 16), body: io.NopCloser(nil), max: 1 << 20}
	out, err := io.ReadAll(g)
	if err != nil || string(out) != src {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	g = &sseGuard{src: bufio.NewReaderSize(strings.NewReader(src), 16), body: io.NopCloser(nil), max: 24}
	if _, err := io.ReadAll(g); err == nil || !strings.Contains(err.Error(), "exceeds 24 bytes") {
		t.Fatalf("err = %v, want the event bound", err)
	}
}

// eofSignal closes hit the first time its reader reports EOF.
type eofSignal struct {
	r    io.Reader
	once sync.Once
	hit  chan struct{}
}

func (e *eofSignal) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err == io.EOF {
		e.once.Do(func() { close(e.hit) })
	}
	return n, err
}

// lockedBuffer is a bytes.Buffer safe for the SDK's writer goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A client that writes its request and closes its end at once
// (`echo '<request>' | server`) still gets the answer of a call that finishes
// only after the server read EOF.
func TestAPipeServerAnswersACallStillRunningAtEOF(t *testing.T) {
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	in := &eofSignal{r: strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow","arguments":{},` +
		meta + `}}` + "\n"), hit: make(chan struct{})}
	s := sdk.NewServer(&sdk.Implementation{Name: "eof", Version: "1"}, nil)
	s.AddTool(&Tool{Name: "slow", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(context.Context, *sdk.CallToolRequest) (*CallToolResult, error) {
			<-in.hit
			return text("done"), nil
		})
	var out lockedBuffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Run(ctx, NewPipeTransport(in, &out, wire.Limits{}))
	if got := out.String(); !strings.Contains(got, `"id":7`) || !strings.Contains(got, "done") {
		t.Fatalf("the call's reply was dropped at EOF; wrote %q", got)
	}
}

// A duplicate key is legal to encoding/json and to the SDK; here it ends the
// session, because a stream whose framing is untrustworthy has no safe place to
// resume from.
func TestAMalformedFrameEndsThePipeSession(t *testing.T) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	go func() { _, _ = io.Copy(io.Discard, s2cR) }()
	ss, err := echoServer().Connect(context.Background(), NewPipeTransport(c2sR, s2cW, wire.Limits{}), nil)
	must(t, err)
	t.Cleanup(func() { _ = ss.Close(); _ = c2sW.Close() })

	_, _ = c2sW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","method":"ping"}` + "\n"))
	done := make(chan struct{})
	go func() { _ = ss.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server kept reading after a malformed frame")
	}
}

// A child's stderr line too long to buffer does not stop the draining (a
// child blocked on a full stderr pipe stops answering too); the caller is told
// once why the rest is gone.
func TestStderrIsDrainedPastAnOversizedLine(t *testing.T) {
	r, w, err := os.Pipe()
	must(t, err)
	written := make(chan error, 1)
	go func() {
		_, err := w.Write([]byte("first\n" + strings.Repeat("x", 2<<20) + "\nlater\n"))
		_ = w.Close()
		written <- err
	}()
	var lines []string
	pumpStderr(r, func(l string) { lines = append(lines, l) })
	must(t, <-written)
	if len(lines) != 2 || lines[0] != "first" || !strings.Contains(lines[1], "stderr line exceeded") {
		t.Fatalf("delivered %q; want first, then one note", lines)
	}
}

func TestInterpolateExpandsReferencesAndNamesTheUnset(t *testing.T) {
	vars := map[string]string{"A": "1", "EMPTY": "", "LONG_NAME_2": "x"}
	lookup := func(n string) (string, bool) { v, ok := vars[n]; return v, ok }
	for _, tc := range []struct {
		in, want string
		missing  []string
	}{
		{"${A}-$A", "1-1", nil},
		{"[${EMPTY}]", "[]", nil},
		{"$LONG_NAME_2.", "x.", nil},
		{"$$A costs $", "$A costs $", nil},
		{"a $-b", "a $-b", nil},
		{"${A", "${A", nil},
		{"${GONE}/$ALSO", "/", []string{"GONE", "ALSO"}},
	} {
		got, missing := interpolate(tc.in, lookup)
		if got != tc.want || !reflect.DeepEqual(missing, tc.missing) {
			t.Errorf("interpolate(%q) = %q, %v; want %q, %v", tc.in, got, missing, tc.want, tc.missing)
		}
	}
}
