package openrouter_test

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// A status that is not 2xx and not 429 or 5xx fails the turn at once with a
// StatusError carrying the vendor's message; a 401 or 403 also names the
// variable to check.
func TestHTTPErrorsFailAtOnceWithTheVendorMessage(t *testing.T) {
	long := strings.Repeat("é", 250)
	for _, tc := range []struct {
		name string
		rep  reply
		code int
		text string
	}{
		{"401", jsonError(401, "bad key", ""), 401, "openrouter: HTTP 401: bad key (check OPENROUTER_API_KEY)"},
		{"403", jsonError(403, "forbidden", ""), 403, "openrouter: HTTP 403: forbidden (check OPENROUTER_API_KEY)"},
		{"400", jsonError(400, " invalid model ", ""), 400, "openrouter: HTTP 400: invalid model"},
		{"402", jsonError(402, "insufficient credits", ""), 402, "openrouter: HTTP 402: insufficient credits"},
		{"a body that is not JSON", reply{status: 418, body: "plain failure\nsecond line"}, 418, "openrouter: HTTP 418: plain failure"},
		{"a long body that is not JSON", reply{status: 400, body: long}, 400, "openrouter: HTTP 400: " + long[:400] + "..."},
		{"an empty body", reply{status: 400}, 400, "openrouter: HTTP 400: Bad Request"},
		{"an error that is a string", reply{status: 400, body: `{"error":"just words"}`}, 400, "openrouter: HTTP 400: just words"},
		{"an error without a message", reply{status: 400, body: `{"error":{"code":400}}`}, 400, "openrouter: HTTP 400: Bad Request"},
		{"JSON without an error", reply{status: 404, body: `{"detail":"x"}`}, 404, "openrouter: HTTP 404: Not Found"},
		{"a status with no name", reply{status: 499}, 499, "openrouter: HTTP 499"},
	} {
		srv := newServer(t, tc.rep, okTurn())
		o := run(t, newProvider(t, srv, testModel()), userRequest("hi"))
		se := statusError(t, o.err)
		if srv.Count() != 1 || se.Code != tc.code || se.Text != tc.text || se.Error() != tc.text {
			t.Errorf("%s: %d requests, %d %q; want 1, %d %q", tc.name, srv.Count(), se.Code, se.Text, tc.code, tc.text)
		}
		if o.msg.StopReason != core.StopReasonError || o.msg.ErrorMessage != tc.text {
			t.Errorf("%s: message = %q %q", tc.name, o.msg.StopReason, o.msg.ErrorMessage)
		}
	}
}

// A failure with no HTTP response, or a 200 that is not an event stream, is
// not retried and reads "openrouter: ...".
func TestFailuresWithoutAnErrorStatusAreNotRetried(t *testing.T) {
	var attempts atomic.Int32
	failing := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts.Add(1)
		return nil, errors.New("connection reset by peer")
	})}
	jsonSrv := newServer(t, reply{status: 200, header: map[string]string{"Content-Type": "application/json"}, body: `{"id":"x","choices":[]}`})
	for _, tc := range []struct {
		name, base string
		client     *http.Client
		mention    string
		count      func() int
	}{
		{"a failing transport", "", failing, "connection reset by peer", func() int { return int(attempts.Load()) }},
		{"a JSON answer", jsonSrv.URL(), nil, "not an event stream", jsonSrv.Count},
	} {
		o := options(tc.base)
		o.HTTPClient = tc.client
		c, err := openrouter.New(testModel(), o)
		if err != nil {
			t.Fatal(err)
		}
		out := run(t, c, userRequest("hi"))
		if out.err == nil || !strings.HasPrefix(out.err.Error(), "openrouter: ") || !strings.Contains(out.err.Error(), tc.mention) ||
			out.msg.StopReason != core.StopReasonError || tc.count() != 1 {
			t.Errorf("%s: err = %v, stop %q after %d attempts; want one attempt failing with %q", tc.name, out.err, out.msg.StopReason, tc.count(), tc.mention)
		}
	}
}

// An error inside a 200 stream that comes after content, or is not a 429 or
// 5xx, or has no retries left, fails the turn with its code and message.
func TestInStreamErrorsFailTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rep     reply
		retries int
		code    int
		text    string
	}{
		{"429 after content", sse(chunk(text("hi"), ""), errorChunk(429, "slow down"), done), 2, 429, "openrouter: error 429 in stream: slow down"},
		{"400 before content", sse(errorChunk(400, "bad input"), done), 2, 400, "openrouter: error 400 in stream: bad input"},
		{"503 with no retries", sse(errorChunk(503, "overloaded"), done), 0, 503, "openrouter: error 503 in stream: overloaded"},
		{"finish_reason error with an error object", sse(data(`{"id":"g","model":"m","error":{"code":502,"message":"upstream"},`+
			`"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"error"}]}`), done), 0, 502, "openrouter: error 502 in stream: upstream"},
		{"finish_reason error alone", sse(chunk(`{}`, "error"), done), 2, 0, `openrouter: error in stream: finish_reason "error"`},
	} {
		srv := newServer(t, tc.rep)
		o := run(t, newProvider(t, srv, testModel(), func(o *openrouter.Options) { o.MaxRetries = &tc.retries }), userRequest("hi"))
		se := statusError(t, o.err)
		if se.Code != tc.code || se.Text != tc.text || srv.Count() != 1 {
			t.Errorf("%s: %d %q after %d requests; want %d %q after 1", tc.name, se.Code, se.Text, srv.Count(), tc.code, tc.text)
		}
		if o.msg.StopReason != core.StopReasonError || o.msg.ErrorMessage != tc.text {
			t.Errorf("%s: message = %q %q", tc.name, o.msg.StopReason, o.msg.ErrorMessage)
		}
	}
}

// A turn that fails mid-stream, by an error or by a body ending before any
// finish_reason (ErrStreamTruncated), keeps its thinking and text but no tool
// call, keeps and prices the usage that arrived, and ends with one terminal
// error event carrying the result's error, then the message end.
func TestAFailedTurnKeepsTextAndThinkingButNoToolCall(t *testing.T) {
	partial := []string{chunk(`{"reasoning":"thinking"}`, ""), chunk(text("some text"), ""), toolDelta(0, "call_a", "read", `{"pa`),
		usageChunk(`{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}`)}
	for _, tc := range []struct {
		name string
		rep  reply
		is   error
		text string
	}{
		{"an error", sse(append(partial, errorChunk(400, "bad request"), done)...), nil, "openrouter: error 400 in stream: bad request"},
		{"[DONE] before a finish", sse(append(partial, done)...), openrouter.ErrStreamTruncated, "openrouter: stream ended before finish_reason"},
		{"EOF before a finish", sse(partial...), openrouter.ErrStreamTruncated, "openrouter: stream ended before finish_reason"},
	} {
		o := run(t, newProvider(t, newServer(t, tc.rep), pricedModel()), userRequest("hi"))
		if o.err == nil || o.err.Error() != tc.text || tc.is != nil && !errors.Is(o.err, tc.is) {
			t.Errorf("%s: error = %v, want %q", tc.name, o.err, tc.text)
		}
		if got := kinds(o.events); got != "start think+0 think~0 text+1 text~1 tool+2 tool~2 error end" {
			t.Errorf("%s: events = %s", tc.name, got)
		}
		if ev := o.events[len(o.events)-2].(core.ErrorEvent); !ev.Terminal || ev.Err != o.err || ev.Message != tc.text {
			t.Errorf("%s: error event = %#v, want a terminal one carrying the result's error", tc.name, ev)
		}
		m := o.msg
		if _, ok := m.Content[0].(core.ThinkingBlock); !ok || len(m.Content) != 2 || m.Content.Text() != "some text" ||
			m.StopReason != core.StopReasonError || m.ErrorMessage != tc.text {
			t.Errorf("%s: message = %q %q %+v, want the thinking and the text", tc.name, m.StopReason, m.ErrorMessage, m.Content)
		}
		if u := m.Usage; u.InputTokens != 1000 || u.OutputTokens != 500 || !u.Has(core.UsageCostUSD) || u.CostUSD == 0 || u.Requests != 1 {
			t.Errorf("%s: usage = %+v, want the counts that arrived, priced, for one request", tc.name, u)
		}
	}
}

// Whatever the key, and though the vendor, the stream, the transport and a
// tool call echo it, no error, error event, ErrorMessage or warning the
// provider builds holds it, raw or URL-escaped; the redacted error still
// unwraps to its cause.
func TestTheKeyNeverAppearsInWhatTheProviderBuilds(t *testing.T) {
	for _, key := range []string{"sk-or-plain-key-5e9", "sk-a+b/c=d~e.f_g", "sk-%41 spaced?&#"} {
		forms := []string{key, url.PathEscape(key), url.QueryEscape(key)}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		closed := l.Addr().String()
		_ = l.Close()
		for _, tc := range []struct {
			name string
			base func() string
			rep  reply
			is   func(error) bool
		}{
			{"an HTTP error", nil, jsonError(401, "rejected key "+key, ""), nil},
			{"an in-stream error", nil, sse(errorChunk(400, "bad "+key), done), nil},
			{"a transport error", func() string { return "http://" + closed + "/" + url.PathEscape(key) }, reply{},
				func(err error) bool { var ue *url.Error; return errors.As(err, &ue) }},
			{"a warning", nil, sse(toolDelta(0, "call_1", key, "[1"), chunk(`{}`, "tool_calls"), done), nil},
		} {
			srv := newServer(t, tc.rep)
			o := options(srv.URL())
			if tc.base != nil {
				o.Env[openrouter.BaseURLVar] = tc.base()
			}
			o.Env[openrouter.APIKeyVar] = key
			o.MaxRetries = ptr(0)
			warn, warns := collectWarnings()
			warn(&o)
			c, err := openrouter.New(testModel(), o)
			if err != nil {
				t.Fatal(err)
			}
			out := run(t, c, userRequest("hi"))
			built := append([]string{out.msg.ErrorMessage}, *warns...)
			if out.err != nil {
				built = append(built, out.err.Error())
			}
			for _, e := range out.events {
				if ev, ok := e.(core.ErrorEvent); ok {
					built = append(built, ev.Message, ev.Err.Error())
				}
			}
			all := strings.Join(built, "\n")
			for _, f := range forms {
				if strings.Contains(all, f) {
					t.Errorf("key %q, %s: %q appears in %q", key, tc.name, f, all)
				}
			}
			if !strings.Contains(all, "[redacted]") {
				t.Errorf("key %q, %s: no [redacted] in %q", key, tc.name, all)
			}
			if tc.is != nil && !tc.is(out.err) {
				t.Errorf("key %q, %s: the error %v no longer unwraps to its cause", key, tc.name, out.err)
			}
		}
		_, err = openrouter.New(testModel(), openrouter.Options{Env: map[string]string{
			openrouter.APIKeyVar: key, openrouter.BaseURLVar: "ftp://host/" + key}})
		if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "[redacted]") {
			t.Errorf("key %q: New's error = %v, want the key replaced by [redacted]", key, err)
		}
	}
}
