package openrouter_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// waits replaces a provider's sleep: it records each wait and returns at once.
// The turn's goroutine writes it before the stream ends, so a test reads it
// after draining.
type waits struct{ got []time.Duration }

func (w *waits) sleep(_ context.Context, d time.Duration) error {
	w.got = append(w.got, d)
	return nil
}

// retrying is a provider against srv with maxRetries whose waits are recorded.
func retrying(t *testing.T, srv *fakeServer, maxRetries *int) (core.ProviderClient, *waits) {
	t.Helper()
	c := newProvider(t, srv, testModel(), func(o *openrouter.Options) { o.MaxRetries = maxRetries })
	w := &waits{}
	openrouter.SetSleep(c, w.sleep)
	return c, w
}

// The wait is a Retry-After of 0 to 60 seconds, as seconds or an HTTP date;
// otherwise 0.5 s doubling per retry up to 8 s, plus up to a quarter as
// jitter, which a Retry-After never gets.
func TestRetryDelayFollowsRetryAfterElseABackoffCurve(t *testing.T) {
	zero, almost := func() float64 { return 0 }, func() float64 { return 0.999999 }
	for _, tc := range []struct {
		name       string
		retries    int
		retryAfter string
		jitter     func() float64
		min, max   time.Duration
	}{
		{"zero seconds", 0, "0", almost, 0, 0},
		{"five seconds", 0, " 5 ", almost, 5 * time.Second, 5 * time.Second},
		{"the boundary", 0, "60", zero, time.Minute, time.Minute},
		{"past the boundary", 0, "61", zero, 500 * time.Millisecond, 500 * time.Millisecond},
		{"negative", 0, "-1", zero, 500 * time.Millisecond, 500 * time.Millisecond},
		{"more than a day", 0, "100000", zero, 500 * time.Millisecond, 500 * time.Millisecond},
		{"a date ahead", 0, fixedNow.Add(3 * time.Second).Format(http.TimeFormat), almost, 3 * time.Second, 3 * time.Second},
		{"a date past", 0, fixedNow.Add(-time.Hour).Format(http.TimeFormat), zero, 500 * time.Millisecond, 500 * time.Millisecond},
		{"no header, second retry", 1, "", zero, time.Second, time.Second},
		{"not a number", 2, "soon", zero, 2 * time.Second, 2 * time.Second},
		{"the cap", 10, "", zero, 8 * time.Second, 8 * time.Second},
		{"jitter on the first wait", 0, "", almost, 500 * time.Millisecond, 625 * time.Millisecond},
		{"jitter on the cap", 10, "", almost, 8 * time.Second, 10 * time.Second},
	} {
		if got := openrouter.RetryDelay(tc.retries, tc.retryAfter, fixedNow, tc.jitter); got < tc.min || got > tc.max {
			t.Errorf("%s: wait = %v, want within [%v, %v]", tc.name, got, tc.min, tc.max)
		}
	}
}

// A 429 or 5xx is sent again up to MaxRetries: 2 when nil, none when 0 or
// negative; each wait is the delay the failure asks for; the last failure
// ends the turn.
func TestRetryableStatusesAreSentAgainUpToMaxRetries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxRetries *int
		replies    []reply
		requests   int
		code       int // of the final error, 0 for success
	}{
		{"default", nil, []reply{jsonError(503, "a", "0"), jsonError(502, "b", "0"), jsonError(429, "c", "0")}, 3, 429},
		{"zero", ptr(0), []reply{jsonError(503, "a", "0")}, 1, 503},
		{"negative", ptr(-1), []reply{jsonError(503, "a", "0")}, 1, 503},
		{"one", ptr(1), []reply{jsonError(500, "a", "0")}, 2, 500},
		{"success after waiting 5s, then about 1s", nil, []reply{jsonError(429, "a", "5"), jsonError(503, "b", ""), okTurn()}, 3, 0},
	} {
		srv := newServer(t, tc.replies...)
		c, w := retrying(t, srv, tc.maxRetries)
		o := run(t, c, userRequest("hi"))
		if srv.Count() != tc.requests || len(w.got) != tc.requests-1 {
			t.Errorf("%s: %d requests, want %d", tc.name, srv.Count(), tc.requests)
		}
		if tc.code == 0 {
			if o.err != nil || o.msg.Content.Text() != "ok" || w.got[0] != 5*time.Second || w.got[1] < time.Second || w.got[1] > 1250*time.Millisecond {
				t.Errorf("%s: turn = %q, %v after waits %v", tc.name, o.msg.Content.Text(), o.err, w.got)
			}
			continue
		}
		if se := statusError(t, o.err); se.Code != tc.code || o.msg.StopReason != core.StopReasonError {
			t.Errorf("%s: code %d, stop %q; want %d and an error turn", tc.name, se.Code, o.msg.StopReason, tc.code)
		}
	}
}

// An in-stream 429 or 5xx before content discards the attempt: the consumer
// sees one message start, with the turn's model, effort and time, and only
// the next attempt's events and usage.
func TestAnAttemptFailingBeforeContentLeavesNoTrace(t *testing.T) {
	cleanReq := userRequest("hi")
	cleanReq.Effort = core.EffortLow
	clean := run(t, newProvider(t, newServer(t, okTurn()), testModel()), cleanReq)
	for _, first := range []reply{
		jsonError(503, "busy", "0"),
		sse(errorChunk(429, "try later"), done),
		sse(usageChunk(`{"prompt_tokens":99,"completion_tokens":99,"total_tokens":198}`), errorChunk(502, "upstream"), done),
	} {
		srv := newServer(t, first, okTurn())
		c, _ := retrying(t, srv, nil)
		o := run(t, c, cleanReq)
		if o.err != nil || srv.Count() != 2 || mustJSON(o.events) != mustJSON(clean.events) || o.msg.Usage.Reported() {
			t.Errorf("first attempt %q: %s with usage %+v, %v after %d requests; want a clean run's events",
				first.body, kinds(o.events), o.msg.Usage, o.err, srv.Count())
		}
		if start := o.events[0].(core.MessageStartEvent).Message; start.Model != "vendor/model" || start.Effort != core.EffortLow || !start.Timestamp.Equal(fixedNow) {
			t.Errorf("start = %q %q %v", start.Model, start.Effort, start.Timestamp)
		}
	}
}

// Without a replaced sleep the provider really waits, here the few
// milliseconds a Retry-After date gives against the clock.
func TestTheDefaultSleepWaitsOutTheDelay(t *testing.T) {
	due := fixedNow.Add(time.Second)
	srv := newServer(t, jsonError(503, "busy", due.Format(http.TimeFormat)), okTurn())
	start := time.Now()
	o := run(t, newProvider(t, srv, testModel(), func(o *openrouter.Options) {
		o.Now = func() time.Time { return due.Add(-20 * time.Millisecond) }
	}), userRequest("hi"))
	if o.err != nil || srv.Count() != 2 || time.Since(start) < 20*time.Millisecond {
		t.Errorf("turn = %v after %d requests in %v, want success after a 20ms wait", o.err, srv.Count(), time.Since(start))
	}
}

// The usage is one request however many attempts it took, and the served
// model is billed when it is not the one asked for.
func TestOneRequestIsBilledWhateverTheAttempts(t *testing.T) {
	served := func(model string) reply {
		c := func(delta, finish string) string {
			return data(`{"id":"gen-1","model":"` + model + `","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}`)
		}
		return sse(c(text("ok"), "null"), c(`{}`, `"stop"`), done)
	}
	srv := newServer(t, jsonError(503, "a", "0"), jsonError(503, "b", "0"), served("vendor/other-served"))
	c, _ := retrying(t, srv, nil)
	o := run(t, c, userRequest("hi"))
	if o.err != nil || srv.Count() != 3 || o.msg.Usage.Requests != 1 || o.msg.Usage.BilledModel != "vendor/other-served" {
		t.Errorf("usage = %+v, %v after %d requests; want one request billed to vendor/other-served", o.msg.Usage, o.err, srv.Count())
	}
	o = run(t, newProvider(t, newServer(t, served("vendor/model")), testModel()), userRequest("hi"))
	if o.msg.Usage.BilledModel != "" {
		t.Errorf("BilledModel = %q for the model asked for, want empty", o.msg.Usage.BilledModel)
	}
}
