package openrouter_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// errorChunk is a chunk that carries only an error object, as OpenRouter sends
// one inside a 200 stream.
func errorChunk(code int, message string) string {
	return data(fmt.Sprintf(`{"error":{"code":%d,"message":%q}}`, code, message))
}

// statusError returns the *StatusError an error is, failing the test if it is
// none.
func statusError(t *testing.T, err error) *openrouter.StatusError {
	t.Helper()
	var se *openrouter.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("error = %v (%T), want a *openrouter.StatusError", err, err)
	}
	return se
}

// TS-27-32: a body that ends without a finish_reason fails with
// ErrStreamTruncated, and the message keeps no tool call.
func TestATruncatedStreamFails_TS27_32(t *testing.T) {
	partial := []string{
		chunk(text("some text"), ""),
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{\"pa"}}]}`, ""),
	}
	for name, rep := range map[string]reply{
		"ended by [DONE]": sse(append(partial, done)...),
		"ended by EOF":    sse(partial...),
	} {
		o := run(t, newProvider(t, newReplay(t, rep)), userRequest("hi"))
		if !errors.Is(o.err, openrouter.ErrStreamTruncated) {
			t.Errorf("%s: error = %v, want ErrStreamTruncated", name, o.err)
			continue
		}
		if o.err.Error() != "openrouter: stream ended before finish_reason" {
			t.Errorf("%s: error text = %q", name, o.err)
		}
		if o.msg.StopReason != core.StopReasonError || len(core.ExtractToolUse(&o.msg)) != 0 {
			t.Errorf("%s: message = %q with %d tool calls, want an error turn and none", name, o.msg.StopReason, len(core.ExtractToolUse(&o.msg)))
		}
		if o.msg.Content.Text() != "some text" {
			t.Errorf("%s: text = %q, want the text streamed so far", name, o.msg.Content.Text())
		}
	}
}

// TS-27-34: an in-stream error after content, with a code that is not 429 or
// 5xx, or with no retries left, fails the turn as a StatusError, without a
// retry.
func TestInStreamErrorsFail_TS27_34(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rep     reply
		code    int
		message string
		retries int
	}{
		{"429 after content", sse(chunk(text("hi"), ""), errorChunk(429, "slow down"), done), 429, "slow down", 2},
		{"400 with no content", sse(errorChunk(400, "bad input"), done), 400, "bad input", 2},
		{"503 with no retries", sse(errorChunk(503, "overloaded"), done), 503, "overloaded", 0},
		{"finish_reason error with an error object", sse(data(`{"id":"g","model":"m","error":{"code":502,"message":"upstream"},`+
			`"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"error"}]}`), done), 502, "upstream", 0},
	} {
		srv := newReplay(t, tc.rep)
		c := newProvider(t, srv, func(o *openrouter.Options) { o.MaxRetries = &tc.retries })
		o := run(t, c, userRequest("hi"))
		se := statusError(t, o.err)
		want := fmt.Sprintf("openrouter: error %d in stream: %s", tc.code, tc.message)
		if se.Code != tc.code || se.Text != want || se.Error() != want {
			t.Errorf("%s: StatusError = %d %q, want %d %q", tc.name, se.Code, se.Text, tc.code, want)
		}
		if srv.Count() != 1 {
			t.Errorf("%s: the server received %d requests, want 1", tc.name, srv.Count())
		}
		if o.msg.StopReason != core.StopReasonError || o.msg.ErrorMessage != want {
			t.Errorf("%s: message = %q %q", tc.name, o.msg.StopReason, o.msg.ErrorMessage)
		}
	}

	// A finish_reason error with no error object still fails the turn.
	o := run(t, newProvider(t, newReplay(t, sse(chunk(`{}`, "error"), done))), userRequest("hi"))
	if o.err == nil || o.msg.StopReason != core.StopReasonError || !strings.Contains(o.err.Error(), "finish_reason") {
		t.Errorf("finish_reason error: err = %v, stop = %q; want a failed turn that names finish_reason", o.err, o.msg.StopReason)
	}
}

// TS-27-35: a failed turn keeps its text and thinking but no tool call, keeps
// the usage that arrived and prices it, and ends with the error event, the
// message end and a result whose error is the event's.
func TestAFailedTurnKeepsOnlyTextAndThinking_TS27_35(t *testing.T) {
	srv := newReplay(t, sse(
		chunk(`{"reasoning":"thinking"}`, ""),
		chunk(text("some text"), ""),
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{}"}}]}`, ""),
		usageChunk(`{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500}`),
		errorChunk(400, "bad request"),
		done))
	m := pricedModel()
	o := run(t, newProviderFor(t, srv, m), userRequest("hi"))

	se := statusError(t, o.err)
	msg := o.msg
	if msg.StopReason != core.StopReasonError || msg.ErrorMessage != se.Text || msg.ErrorMessage != o.err.Error() {
		t.Errorf("message = %q %q, want an error turn whose ErrorMessage is the error's text", msg.StopReason, msg.ErrorMessage)
	}
	var kinds []string
	for _, b := range msg.Content {
		kinds = append(kinds, fmt.Sprintf("%T", b))
	}
	if !reflect.DeepEqual(kinds, []string{"core.ThinkingBlock", "core.TextBlock"}) {
		t.Errorf("content = %v, want the thinking and the text only", kinds)
	}
	if calls := core.ExtractToolUse(&msg); len(calls) != 0 {
		t.Errorf("the failed message holds tool calls: %+v", calls)
	}
	u := msg.Usage
	if !u.Reported() || u.InputTokens != 1000 || u.OutputTokens != 500 || !u.Has(core.UsageCostUSD) || u.CostUSD == 0 || u.Requests != 1 {
		t.Errorf("usage = %+v, want the counts that arrived, priced, for one request", u)
	}

	n := len(o.events)
	if n < 2 {
		t.Fatalf("only %d events", n)
	}
	errEv, ok := o.events[n-2].(core.ErrorEvent)
	if !ok || !errEv.Terminal || errEv.Err != o.err {
		t.Errorf("the second-to-last event = %#v, want a terminal ErrorEvent carrying the result's error", o.events[n-2])
	}
	if _, ok := o.events[n-1].(core.MessageEndEvent); !ok {
		t.Errorf("the last event = %T, want the message end", o.events[n-1])
	}
	// The channel adapter adds a message end when the last event is not one, so
	// only a count of one shows the provider emitted the two in this order.
	ends := 0
	for _, e := range o.events {
		if _, ok := e.(core.MessageEndEvent); ok {
			ends++
		}
	}
	if ends != 1 {
		t.Errorf("%d message-end events, want 1", ends)
	}
}
