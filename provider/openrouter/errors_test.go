package openrouter_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// jsonError is an HTTP error reply whose body is OpenRouter's error object.
func jsonError(code int, message string) reply {
	return reply{status: code, header: map[string]string{"Content-Type": "application/json"},
		body: fmt.Sprintf(`{"error":{"code":%d,"message":%q}}`, code, message)}
}

// TS-27-39: a 401 or 403 fails the turn at once, with a StatusError that names
// the variable to check.
func TestAuthenticationFailuresAreNotRetried_TS27_39(t *testing.T) {
	for _, code := range []int{401, 403} {
		srv := newReplay(t, jsonError(code, "bad key"), okTurn())
		o := run(t, newProvider(t, srv), userRequest("hi"))
		se := statusError(t, o.err)
		if srv.Count() != 1 || se.Code != code {
			t.Errorf("%d: %d requests and code %d, want 1 and %d", code, srv.Count(), se.Code, code)
		}
		prefix := fmt.Sprintf("openrouter: HTTP %d: bad key", code)
		if !strings.HasPrefix(se.Text, prefix) || !strings.Contains(se.Text, "OPENROUTER_API_KEY") {
			t.Errorf("%d: Text = %q, want it to start %q and name OPENROUTER_API_KEY", code, se.Text, prefix)
		}
		if o.msg.StopReason != core.StopReasonError || o.msg.ErrorMessage != se.Text {
			t.Errorf("%d: message = %q %q", code, o.msg.StopReason, o.msg.ErrorMessage)
		}
	}
}

// TS-27-40: any other non-2xx status fails without a retry, with the vendor's
// message when the body has one.
func TestOtherStatusesFailWithTheVendorMessage_TS27_40(t *testing.T) {
	for _, tc := range []struct {
		name string
		rep  reply
		code int
		text string
	}{
		{"400", jsonError(400, "invalid model"), 400, "openrouter: HTTP 400: invalid model"},
		{"402", jsonError(402, "insufficient credits"), 402, "openrouter: HTTP 402: insufficient credits"},
		{"a body that is not JSON", reply{status: 418, header: map[string]string{"Content-Type": "text/plain"}, body: "plain text failure"},
			418, "openrouter: HTTP 418: plain text failure"},
		{"an empty body", reply{status: 400}, 400, "openrouter: HTTP 400: Bad Request"},
		{"an error that is a string", reply{status: 400, header: map[string]string{"Content-Type": "application/json"}, body: `{"error":"just words"}`},
			400, "openrouter: HTTP 400: just words"},
	} {
		srv := newReplay(t, tc.rep, okTurn())
		o := run(t, newProvider(t, srv), userRequest("hi"))
		se := statusError(t, o.err)
		if srv.Count() != 1 || se.Code != tc.code || se.Text != tc.text {
			t.Errorf("%s: %d requests, code %d, Text %q; want 1, %d and %q", tc.name, srv.Count(), se.Code, se.Text, tc.code, tc.text)
		}
	}
}

// TS-27-41: a failure with no HTTP response is not retried and reads
// "openrouter: <error>".
func TestTransportFailuresAreNotRetried_TS27_41(t *testing.T) {
	// A closed port.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	c, err := openrouter.New(testModel(), openrouter.Options{
		Env: map[string]string{openrouter.APIKeyVar: "sk-or-test-key", openrouter.BaseURLVar: "http://" + addr},
	})
	if err != nil {
		t.Fatal(err)
	}
	o := run(t, c, userRequest("hi"))
	if o.err == nil || !strings.HasPrefix(o.err.Error(), "openrouter: ") || o.msg.StopReason != core.StopReasonError {
		t.Errorf("closed port: err = %v, stop = %q; want an openrouter: error and an error turn", o.err, o.msg.StopReason)
	}

	// A client that fails, counted.
	var attempts atomic.Int32
	c, err = openrouter.New(testModel(), openrouter.Options{
		Env: map[string]string{openrouter.APIKeyVar: "sk-or-test-key"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts.Add(1)
			return nil, errors.New("connection reset by peer")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	o = run(t, c, userRequest("hi"))
	if attempts.Load() != 1 || o.err == nil || !strings.HasPrefix(o.err.Error(), "openrouter: ") ||
		!strings.Contains(o.err.Error(), "connection reset by peer") {
		t.Errorf("%d attempts, err = %v; want 1 attempt and openrouter: ...connection reset by peer", attempts.Load(), o.err)
	}
}

// TS-27-42: cancelling the context during the request, in the middle of a
// stream or during a retry wait ends the turn as aborted within a second.
func TestCancellationAborts_TS27_42(t *testing.T) {
	entered := make(chan struct{}, 4)
	handler := func(mode string, release <-chan struct{}) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			switch mode {
			case "hang":
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			case "stall":
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, chunk(text("partial"), "")+"\n\n")
				w.(http.Flusher).Flush()
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
				}
			case "retry-after":
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"error":{"message":"busy"}}`)
				entered <- struct{}{}
			}
		}
	}
	for _, mode := range []string{"hang", "stall", "retry-after"} {
		release := make(chan struct{})
		srv := httptest.NewServer(handler(mode, release))
		c, err := openrouter.New(testModel(), openrouter.Options{
			Env: map[string]string{openrouter.APIKeyVar: "sk-or-test-key", openrouter.BaseURLVar: srv.URL},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		ch, err := c.Stream(ctx, userRequest("hi"))
		if err != nil {
			t.Fatal(err)
		}
		<-entered
		if mode == "retry-after" {
			time.Sleep(100 * time.Millisecond) // the provider is now waiting out the 60 s
		}
		start := time.Now()
		cancel()
		done := make(chan outcome, 1)
		go func() { done <- drain(t, ch) }()
		select {
		case o := <-done:
			if o.msg.StopReason != core.StopReasonAborted || o.msg.ErrorMessage != "Request was aborted" || !errors.Is(o.err, core.ErrAborted) {
				t.Errorf("%s: message = %q %q, err = %v; want an aborted turn with core.ErrAborted", mode, o.msg.StopReason, o.msg.ErrorMessage, o.err)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("%s: the turn took %v to end after the cancellation", mode, d)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%s: the turn did not end within 3 seconds of the cancellation", mode)
		}
		close(release) // let the server's handler return, so Close does not wait
		srv.Close()
	}
}

// keyAlphabet holds the characters of an API key, including some that a URL
// escapes.
const keyAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.~+/="

func randomKey(r *rand.Rand) string {
	b := make([]byte, 16+r.Intn(30))
	for i := range b {
		b[i] = keyAlphabet[r.Intn(len(keyAlphabet))]
	}
	return "sk-" + string(b)
}

// leaks reports every string in all that contains the key, raw or escaped for
// a URL.
func leaks(key string, all []string) []string {
	var out []string
	for _, s := range all {
		for _, form := range []string{key, url.PathEscape(key), url.QueryEscape(key)} {
			if strings.Contains(s, form) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// strings gathers everything a turn produced that the provider built: the
// error, the message's ErrorMessage, the error events, and the warnings.
func builtText(o outcome, warns []string) []string {
	out := append([]string(nil), warns...)
	if o.err != nil {
		out = append(out, o.err.Error())
	}
	out = append(out, o.msg.ErrorMessage)
	for _, e := range o.events {
		if ev, ok := e.(core.ErrorEvent); ok {
			out = append(out, ev.Message)
			if ev.Err != nil {
				out = append(out, ev.Err.Error())
			}
		}
	}
	return out
}

// TS-27-43 (property): for any key, no error text, error event, ErrorMessage or
// warning the provider builds contains it, though the vendor, the stream, the
// transport and a tool call all echo it.
func TestTheKeyNeverAppearsInOutput_TS27_43(t *testing.T) {
	r := rand.New(rand.NewSource(43))
	for i := 0; i < 25; i++ {
		key := randomKey(r)
		var warns []string
		opts := func(base string) openrouter.Options {
			return openrouter.Options{
				Env:        map[string]string{openrouter.APIKeyVar: key, openrouter.BaseURLVar: base},
				MaxRetries: ptr(0),
				Warnf:      func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) },
			}
		}
		check := func(scenario string, o outcome, wantMarker bool) {
			t.Helper()
			all := builtText(o, warns)
			if l := leaks(key, all); len(l) > 0 {
				t.Errorf("key %q, %s: the key appears in %q", key, scenario, l)
			}
			if wantMarker && !strings.Contains(strings.Join(all, "\n"), "[redacted]") {
				t.Errorf("key %q, %s: no [redacted] in %q", key, scenario, all)
			}
			warns = nil
		}

		// The vendor's message echoes the key.
		srv := newReplay(t, jsonError(401, "rejected key "+key))
		c, err := openrouter.New(testModel(), opts(srv.URL()))
		if err != nil {
			t.Fatal(err)
		}
		check("HTTP error", run(t, c, userRequest("hi")), true)

		// An error inside the stream echoes it.
		srv = newReplay(t, sse(errorChunk(400, "bad "+key), done))
		c, _ = openrouter.New(testModel(), opts(srv.URL()))
		check("in-stream error", run(t, c, userRequest("hi")), true)

		// The base URL holds it, and nothing listens there.
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		addr := l.Addr().String()
		l.Close()
		c, err = openrouter.New(testModel(), opts("http://"+addr+"/"+url.PathEscape(key)))
		if err != nil {
			t.Fatal(err)
		}
		check("transport error", run(t, c, userRequest("hi")), true)

		// A tool call named for it, with arguments that are not an object.
		srv = newReplay(t, sse(
			chunk(`{"tool_calls":[{"index":0,"id":"call_1","function":{"name":`+jsonStr(key)+`,"arguments":"[1"}}]}`, ""),
			chunk(`{}`, "tool_calls"), done))
		c, _ = openrouter.New(testModel(), opts(srv.URL()))
		check("warning", run(t, c, userRequest("hi")), true)

		// A base URL that is rejected shows the key.
		_, err = openrouter.New(testModel(), opts("ftp://host/"+key))
		if err == nil {
			t.Fatalf("key %q: New accepted an ftp base URL", key)
		}
		if l := leaks(key, []string{err.Error()}); len(l) > 0 || !strings.Contains(err.Error(), "[redacted]") {
			t.Errorf("key %q: New's error is %q, want the key replaced by [redacted]", key, err)
		}
	}
}
