package openrouter_test

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// failing is an HTTP error reply with a JSON body and a Retry-After header
// ("" for none).
func failing(code int, retryAfter string) reply {
	h := map[string]string{"Content-Type": "application/json"}
	if retryAfter != "" {
		h["Retry-After"] = retryAfter
	}
	return reply{status: code, header: h, body: fmt.Sprintf(`{"error":{"code":%d,"message":"upstream said %d"}}`, code, code)}
}

// withRetryAfter returns a copy of an event-stream reply that also carries a
// Retry-After header.
func withRetryAfter(r reply, v string) reply {
	h := map[string]string{"Retry-After": v}
	for k, x := range r.header {
		h[k] = x
	}
	r.header = h
	return r
}

// noWait makes a provider skip its waits and records them.
type noWait struct {
	mu    sync.Mutex
	waits []time.Duration
}

func (w *noWait) sleep(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waits = append(w.waits, d)
	return nil
}

func (w *noWait) recorded() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.waits...)
}

// retries builds a provider with the given MaxRetries and recorded waits.
func retrying(t *testing.T, srv *replay, maxRetries *int) (core.ProviderClient, *noWait) {
	t.Helper()
	c := newProvider(t, srv, func(o *openrouter.Options) { o.MaxRetries = maxRetries })
	w := &noWait{}
	openrouter.SetSleep(c, w.sleep)
	return c, w
}

// TS-27-24: one message start per turn, even when the first attempt failed,
// with the model, effort and time the turn was asked with.
func TestOneMessageStartAcrossARetry_TS27_24(t *testing.T) {
	srv := newReplay(t, failing(503, "0"), okTurn())
	c, _ := retrying(t, srv, nil)
	req := userRequest("hi")
	req.Effort = core.EffortLow
	o := run(t, c, req)
	if o.err != nil {
		t.Fatal(o.err)
	}
	starts, firstContent := 0, -1
	for i, e := range o.events {
		switch e.(type) {
		case core.MessageStartEvent:
			starts++
			if firstContent >= 0 {
				t.Errorf("the message start (event %d) comes after content (event %d)", i, firstContent)
			}
		case core.TextStartEvent, core.TextDeltaEvent:
			if firstContent < 0 {
				firstContent = i
			}
		}
	}
	if starts != 1 {
		t.Errorf("%d message-start events, want 1", starts)
	}
	start := o.events[0].(core.MessageStartEvent).Message
	if start.Model != "vendor/model" || start.Effort != core.EffortLow || !start.Timestamp.Equal(fixedNow) {
		t.Errorf("start message = %q %q at %v, want vendor/model, low, the clock", start.Model, start.Effort, start.Timestamp)
	}
	if srv.Count() != 2 {
		t.Errorf("the server received %d requests, want 2", srv.Count())
	}
}

// TS-27-33: an in-stream 429 or 5xx before any content discards the attempt and
// tries again; the consumer sees only the second attempt.
func TestAnInStreamErrorBeforeContentIsRetried_TS27_33(t *testing.T) {
	clean := run(t, newProvider(t, newReplay(t, okTurn())), userRequest("hi"))
	for _, code := range []int{429, 502} {
		srv := newReplay(t, withRetryAfter(sse(errorChunk(code, "try later"), done), "0"), okTurn())
		c, _ := retrying(t, srv, nil)
		o := run(t, c, userRequest("hi"))
		if o.err != nil || o.msg.Content.Text() != "ok" {
			t.Fatalf("code %d: turn = %q, %v; want the second attempt's ok", code, o.msg.Content.Text(), o.err)
		}
		if srv.Count() != 2 {
			t.Errorf("code %d: the server received %d requests, want 2", code, srv.Count())
		}
		if !reflect.DeepEqual(o.events, clean.events) {
			t.Errorf("code %d: the events differ from a clean run's:\n%v\n%v", code, kinds(o.events), kinds(clean.events))
		}
	}
}

// What a discarded attempt had read is forgotten: a usage chunk that came
// before its error does not reach the message of the attempt that succeeds.
func TestADiscardedAttemptLeavesNothingBehind(t *testing.T) {
	first := withRetryAfter(sse(usageChunk(`{"prompt_tokens":99,"completion_tokens":99,"total_tokens":198}`),
		errorChunk(503, "overloaded"), done), "0")
	c, _ := retrying(t, newReplay(t, first, okTurn()), nil)
	o := run(t, c, userRequest("hi"))
	if o.err != nil || o.msg.Usage.Reported() {
		t.Errorf("turn = %v with usage %+v, want the second attempt's, which reported none", o.err, o.msg.Usage)
	}
}

// TS-27-36: 429 and 5xx are retried up to MaxRetries (2 by default; none for 0
// or a negative value).
func TestRetryCounts_TS27_36(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxRetries *int
		requests   int
	}{
		{"default", nil, 3},
		{"zero", ptr(0), 1},
		{"negative", ptr(-1), 1},
		{"one", ptr(1), 2},
	} {
		srv := newReplay(t, failing(503, "0"))
		c, _ := retrying(t, srv, tc.maxRetries)
		o := run(t, c, userRequest("hi"))
		if srv.Count() != tc.requests {
			t.Errorf("%s: the server received %d requests, want %d", tc.name, srv.Count(), tc.requests)
		}
		if o.err == nil || o.msg.StopReason != core.StopReasonError {
			t.Errorf("%s: the turn did not fail: %v %q", tc.name, o.err, o.msg.StopReason)
		}
	}

	srv := newReplay(t, failing(429, "0"), okTurn())
	c, _ := retrying(t, srv, nil)
	if o := run(t, c, userRequest("hi")); o.err != nil || srv.Count() != 2 {
		t.Errorf("429 then 200: err = %v after %d requests, want success on the second", o.err, srv.Count())
	}
}

// TS-27-37: the wait is Retry-After when it is between 0 and 60 seconds, as
// seconds or an HTTP date; otherwise it follows a jittered curve of 0.5 s
// doubling to 8 s.
func TestRetryWaits_TS27_37(t *testing.T) {
	zero := func() float64 { return 0 }
	almost := func() float64 { return 0.999999 }
	now := fixedNow
	for _, tc := range []struct {
		name       string
		retries    int
		retryAfter string
		want       time.Duration
	}{
		{"zero seconds", 0, "0", 0},
		{"five seconds", 0, "5", 5 * time.Second},
		{"the boundary", 0, "60", 60 * time.Second},
		{"past the boundary", 0, "61", 500 * time.Millisecond},
		{"too long", 0, "120", 500 * time.Millisecond},
		{"a date three seconds ahead", 0, now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{"a date in the past", 0, now.Add(-time.Hour).Format(http.TimeFormat), 500 * time.Millisecond},
		{"nothing sent", 1, "", time.Second},
		{"not a number", 2, "soon", 2 * time.Second},
		{"the curve caps", 10, "", 8 * time.Second},
	} {
		if got := openrouter.RetryDelay(tc.retries, tc.retryAfter, now, zero); got != tc.want {
			t.Errorf("%s: wait = %v, want %v", tc.name, got, tc.want)
		}
	}
	// Jitter adds up to a quarter to the curve only, never to a Retry-After.
	if got := openrouter.RetryDelay(0, "", now, almost); got < 500*time.Millisecond || got > 625*time.Millisecond {
		t.Errorf("jittered first wait = %v, want within [0.5s, 0.625s]", got)
	}
	if got := openrouter.RetryDelay(10, "", now, almost); got > 10*time.Second {
		t.Errorf("jittered capped wait = %v, want at most 10s", got)
	}
	if got := openrouter.RetryDelay(0, "5", now, almost); got != 5*time.Second {
		t.Errorf("Retry-After 5 with jitter = %v, want exactly 5s", got)
	}

	// The provider waits as many times, and as long, as the delays say.
	srv := newReplay(t, failing(503, "5"), failing(503, ""), okTurn())
	c, w := retrying(t, srv, nil)
	if o := run(t, c, userRequest("hi")); o.err != nil {
		t.Fatal(o.err)
	}
	waits := w.recorded()
	if len(waits) != 2 || waits[0] != 5*time.Second || waits[1] < time.Second || waits[1] > 1250*time.Millisecond {
		t.Errorf("waits = %v, want 5s, then about 1s", waits)
	}
}

// TS-27-38: when the retries run out, the last error ends the turn.
func TestRetriesExhaustedEndWithTheLastError_TS27_38(t *testing.T) {
	srv := newReplay(t, failing(503, "0"), failing(502, "0"), failing(429, "0"))
	c, _ := retrying(t, srv, nil)
	o := run(t, c, userRequest("hi"))
	se := statusError(t, o.err)
	if se.Code != 429 {
		t.Errorf("Code = %d, want 429, the last error", se.Code)
	}
	if o.msg.StopReason != core.StopReasonError || srv.Count() != 3 {
		t.Errorf("message = %q after %d requests, want an error turn after 3", o.msg.StopReason, srv.Count())
	}
}

// servedStream is a good stream whose chunks name the model that served them.
func servedStream(model string) reply {
	c := func(delta, finish string) string {
		f := "null"
		if finish != "" {
			f = fmt.Sprintf("%q", finish)
		}
		return data(fmt.Sprintf(`{"id":"gen-1","model":%q,"choices":[{"index":0,"delta":%s,"finish_reason":%s}]}`, model, delta, f))
	}
	return sse(c(text("ok"), ""), c(`{}`, "stop"), done)
}

// TS-27-46: one request however many attempts it took, and the served model is
// billed when it is not the one asked for.
func TestOneRequestWhateverTheAttempts_TS27_46(t *testing.T) {
	srv := newReplay(t, failing(503, "0"), failing(503, "0"), servedStream("vendor/other-served"))
	c, _ := retrying(t, srv, nil)
	o := run(t, c, userRequest("hi"))
	if o.err != nil || o.msg.Usage.Requests != 1 || o.msg.Usage.BilledModel != "vendor/other-served" {
		t.Errorf("usage = %+v, %v; want Requests 1 and BilledModel vendor/other-served after 3 attempts", o.msg.Usage, o.err)
	}
	if srv.Count() != 3 {
		t.Errorf("the server received %d requests, want 3", srv.Count())
	}

	srv = newReplay(t, servedStream("vendor/model"))
	o = run(t, newProvider(t, srv), userRequest("hi"))
	if o.msg.Usage.BilledModel != "" {
		t.Errorf("BilledModel = %q when the served model is the requested one, want empty", o.msg.Usage.BilledModel)
	}
}
