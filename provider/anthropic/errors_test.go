package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// cutBefore is streamFixture up to the n-th occurrence of marker.
func cutBefore(marker string, n int) string {
	full, at := streamFixture(), -1
	for range n {
		at += 1 + strings.Index(full[at+1:], marker)
	}
	return full[:at]
}

// A failed turn is ONE value: the content streamed so far, open blocks
// included, an error stop reason and text, a terminal error event, and the
// usage message_start already reported, priced.
func TestAFailedStreamKeepsWhatItStreamedInOneValue(t *testing.T) {
	startCost := (3*100 + 15*1 + 0.3*900 + 3.75*50) / 1e6
	cases := []struct {
		name, body, text, errText string
		is                        error
	}{
		{"a body that stops between blocks", cutBefore("event: content_block_start", 2), "Hello",
			"stream ended before message_stop", anthropic.ErrStreamTruncated},
		{"a body that stops inside a block", cutBefore("event: content_block_delta", 2), "Hel",
			"stream ended before message_stop", anthropic.ErrStreamTruncated},
		{"an error event", cutBefore("event: content_block_stop", 1) +
			sse("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`), "Hello",
			"overloaded_error: Overloaded", nil},
		{"a content block that does not decode", cutBefore("event: content_block_start", 1) +
			sse("content_block_start", `{"index":0,"content_block":"not an object"}`), "",
			"content_block_start: content_block", nil},
		{"duplicate keys", cutBefore("event: content_block_start", 1) +
			sse("message_delta", `{"delta":{"stop_reason":"end_turn","stop_reason":"tool_use"}}`), "",
			"message_delta event", nil},
	}
	for _, c := range cases {
		x := run(t, testModel(), core.Request{}, anthropic.Options{}, 200, c.body)
		m := x.msg
		if m.StopReason != core.StopReasonError || m.Content.Text() != c.text || !strings.Contains(m.ErrorMessage, c.errText) {
			t.Errorf("%s: stop %q, text %q, error %q; want text %q and an error naming %q",
				c.name, m.StopReason, m.Content.Text(), m.ErrorMessage, c.text, c.errText)
		}
		if x.err == nil || (c.is != nil && !errors.Is(x.err, c.is)) {
			t.Errorf("%s: err = %v, want %v", c.name, x.err, c.is)
		}
		if ee, ok := x.events[len(x.events)-2].(core.ErrorEvent); !ok || !ee.Terminal {
			t.Errorf("%s: events = %s, want a terminal error before the message end", c.name, eventNames(x.events))
		}
		if !m.Usage.Has(core.UsageCostUSD) || math.Abs(m.Usage.CostUSD-startCost) > 1e-12 {
			t.Errorf("%s: cost = %v, want %v from message_start's usage", c.name, m.Usage.CostUSD, startCost)
		}
	}
}

// A non-2xx answer carries its status code, in the text and as a
// StatusError, beside the provider's own words, and no usage it never sent.
func TestANon2xxIsAStatusErrorCarryingTheCodeAndTheBody(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`,
			"anthropic: HTTP 429: rate_limit_error: slow down"},
		{400, `{"error":{"message":"bad"}}`, "anthropic: HTTP 400: bad"},
		{529, `{"error":{"type":"overloaded_error"}}`, "anthropic: HTTP 529: overloaded_error"},
		{500, `{"type":"error"}`, "anthropic: HTTP 500"},
	} {
		x := run(t, testModel(), core.Request{}, anthropic.Options{}, c.status, c.body)
		var se *anthropic.StatusError
		if !errors.As(x.err, &se) || se.Code != c.status || se.Error() != c.want || x.msg.ErrorMessage != c.want {
			t.Errorf("%d: err = %v, message %q; want %q", c.status, x.err, x.msg.ErrorMessage, c.want)
		}
		if x.msg.StopReason != core.StopReasonError || x.msg.Usage.Has(core.UsageCostUSD) {
			t.Errorf("%d: stop %q, usage %+v", c.status, x.msg.StopReason, x.msg.Usage)
		}
	}
}

// OnPayload sees the wire body after translation and may replace it; its
// error ends the turn unmodified, before any request is sent.
func TestOnPayloadReplacesOrRefusesTheRequest(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{OnPayload: func(p any, m *core.Model) (any, error) {
		return map[string]any{"model": m.ID, "custom_vendor_field": true}, nil
	}}, 200, streamFixture())
	if b := x.sent(t); b["custom_vendor_field"] != true || b["messages"] != nil {
		t.Errorf("body = %s, want OnPayload's replacement", x.body)
	}

	policy := errors.New("policy says no")
	x = run(t, testModel(), core.Request{}, anthropic.Options{
		OnPayload: func(any, *core.Model) (any, error) { return nil, policy }}, 200, streamFixture())
	if x.err != policy || x.req != nil {
		t.Errorf("err = %v, request sent = %v; want the caller's own error and no request", x.err, x.req != nil)
	}
}

// OnResponse sees each response before it is read, and its error ends the
// turn.
func TestOnResponseCanEndTheTurn(t *testing.T) {
	var status int
	refused := errors.New("refused by hook")
	x := run(t, testModel(), core.Request{}, anthropic.Options{OnResponse: func(r *http.Response, m *core.Model) error {
		status = r.StatusCode
		return refused
	}}, 200, streamFixture())
	if status != 200 || !errors.Is(x.err, refused) || x.msg.StopReason != core.StopReasonError {
		t.Errorf("status seen %d, err %v, stop %q", status, x.err, x.msg.StopReason)
	}
	x = run(t, testModel(), core.Request{}, anthropic.Options{OnResponse: func(*http.Response, *core.Model) error { return nil }},
		200, streamFixture())
	if x.err != nil || x.msg.StopReason != core.StopReasonToolUse {
		t.Errorf("a hook that accepts: %v, %q", x.err, x.msg.StopReason)
	}
}

// The caller's cancellation is an abort, never a retryable error; a done
// context is refused before anything starts.
func TestCancellationIsAnAbortNotAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := anthropic.Provider(*testModel(), anthropic.Options{Env: map[string]string{anthropic.APIKeyVar: testKey},
		Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			cancel()
			return nil, r.Context().Err()
		})})
	res := core.EventStreamOf(p.Stream(ctx, core.Request{})).Wait()
	if res.Message.StopReason != core.StopReasonAborted || res.Message.ErrorMessage != anthropic.AbortText ||
		!errors.Is(res.Err, core.ErrAborted) {
		t.Fatalf("result = %+v, %v; want an aborted turn", res.Message, res.Err)
	}
	if _, err := p.Stream(ctx, core.Request{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a done context: err = %v", err)
	}
}

// Options.Timeout bounds a request whose caller set no deadline, and says so.
func TestTheRequestTimeoutBoundsEachRequest(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{Timeout: time.Millisecond,
		Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		})}, 0, "")
	if x.msg.StopReason != core.StopReasonError || !strings.Contains(x.msg.ErrorMessage, "request timeout (Options.Timeout elapsed)") {
		t.Fatalf("result = %+v", x.msg)
	}
}

func TestATransportFailureIsReported(t *testing.T) {
	x := run(t, testModel(), core.Request{}, anthropic.Options{
		Transport: rtFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }),
	}, 0, "")
	if !strings.HasPrefix(x.msg.ErrorMessage, "anthropic: ") || !strings.Contains(x.msg.ErrorMessage, "connection refused") {
		t.Fatalf("error = %q", x.msg.ErrorMessage)
	}
}

// A tool schema that cannot be encoded fails the request naming the tool,
// and no request is sent.
func TestASchemaThatCannotBeEncodedFailsNamingTheTool(t *testing.T) {
	bad := []core.ToolWire{{Name: "ok", InputSchema: schema.Object()},
		{Name: "broken", InputSchema: &schema.Schema{Type: schema.TypeString, Enum: []json.RawMessage{json.RawMessage("not json")}}}}
	if _, err := anthropic.BuildRequestJSON(core.Request{Tools: bad}, *testModel()); err == nil ||
		!strings.Contains(err.Error(), `tool "broken"`) {
		t.Errorf("BuildRequestJSON err = %v", err)
	}
	x := run(t, testModel(), core.Request{Tools: bad}, anthropic.Options{}, 200, streamFixture())
	if x.req != nil || !strings.Contains(x.msg.ErrorMessage, `anthropic: building request: anthropic: tool "broken"`) {
		t.Errorf("sent %v, error %q", x.req != nil, x.msg.ErrorMessage)
	}
}
