package mcp

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/wire"
)

// fakeRoundTripper answers every request with one canned response.
type fakeRoundTripper struct {
	contentType, body string
	seen              *http.Request
}

func (f *fakeRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = r
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {f.contentType}},
		Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

func roundTrip(t *testing.T, limits wire.Limits, contentType, body string) (string, error) {
	t.Helper()
	rt := &strictRoundTripper{base: &fakeRoundTripper{contentType: contentType, body: body},
		headers: map[string]string{"X-Key": "v"}, limits: limits}
	req, _ := http.NewRequest(http.MethodPost, "http://x/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	return string(out), err
}

// TestResponseBodiesAreHeldToTheWireRules is REQ-SEC-11 on the HTTP client
// surface: the SDK's decoder takes the last of two duplicate keys and bounds
// nothing we chose, so a JSON body and each SSE event's data are checked
// before it reads them.
func TestResponseBodiesAreHeldToTheWireRules(t *testing.T) {
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
		{"valid sse", "text/event-stream", "event: message\ndata: " + ok + "\n\n", wire.Limits{}, false},
		{"duplicate key sse", "text/event-stream", "data: " + ok + "\n\ndata: " + dup + "\n\n", wire.Limits{}, true},
		{"oversized sse", "text/event-stream", "data: " + big + "\n\n", small, true},
		{"data split over lines", "text/event-stream", "data: {\"a\":\ndata: 1,\"a\":2}\n\n", wire.Limits{}, true},
		{"unterminated event", "text/event-stream", "data: " + dup, wire.Limits{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := roundTrip(t, tc.limits, tc.contentType, tc.body)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, want error %v", err, tc.wantErr)
			}
			if tc.wantErr && strings.Contains(out, `"x":1`) {
				t.Fatalf("the rejected event was released to the reader: %q", out)
			}
			if !tc.wantErr && out != tc.body {
				t.Fatalf("a valid body must pass through unchanged: %q", out)
			}
		})
	}
}

// TestTheRoundTripperAddsTheConfiguredHeaders without touching the caller's
// request.
func TestTheRoundTripperAddsTheConfiguredHeaders(t *testing.T) {
	base := &fakeRoundTripper{contentType: "application/json", body: `{}`}
	rt := &strictRoundTripper{base: base, headers: map[string]string{"Authorization": "Bearer t"}}
	req, _ := http.NewRequest(http.MethodPost, "http://x/", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if base.seen.Header.Get("Authorization") != "Bearer t" || req.Header.Get("Authorization") != "" {
		t.Fatalf("sent %v; caller's request %v", base.seen.Header, req.Header)
	}
}

// TestTheSSEGuardKeepsEventBoundaries: an event is released whole, after its
// blank line, so the SDK never reads a prefix of an event that is later
// rejected.
func TestTheSSEGuardKeepsEventBoundaries(t *testing.T) {
	src := "id: 1\ndata: {\"a\":1}\n\ndata: {\"b\":2}\r\n\r\n"
	g := &sseGuard{src: bufio.NewReaderSize(strings.NewReader(src), 16), body: io.NopCloser(nil),
		max: 1 << 20}
	out, err := io.ReadAll(g)
	if err != nil || string(out) != src {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
