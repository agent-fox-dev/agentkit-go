package middleware

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// delayErr is a provider error carrying a server-dictated delay, as
// provider.StatusErr builds one from a 429's Retry-After.
type delayErr struct{ d time.Duration }

func (e delayErr) Error() string                     { return "acme: HTTP 429: rate limit exceeded" }
func (e delayErr) RetryAfter() (time.Duration, bool) { return e.d, true }

// replay answers each call with the next stream factory's result.
func replay(streams ...func() *core.EventStream) (core.Handler, *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context, core.Request) *core.EventStream {
		i := int(calls.Add(1)) - 1
		if i >= len(streams) {
			i = len(streams) - 1
		}
		return streams[i]()
	}, &calls
}

func rateLimited(d time.Duration, u core.Usage) func() *core.EventStream {
	return func() *core.EventStream {
		err := delayErr{d}
		return core.ErrorStream(&core.AssistantMessage{StopReason: core.StopReasonError,
			ErrorMessage: err.Error(), Usage: u}, err)
	}
}

func ok() func() *core.EventStream {
	return func() *core.EventStream {
		s := core.NewEventStream(core.StreamOptions{})
		s.End(core.StreamResult{Message: &core.AssistantMessage{StopReason: core.StopReasonStop,
			Content: core.Content{core.TextBlock{Text: "done"}}}})
		return s
	}
}

// Issue #75 §1: the semantic layer honours a server-dictated delay. Its own
// backoff (500ms, then 1s) inside a 30s cool-down burns every attempt.
func TestRetryWaitsForTheServersDelay(t *testing.T) {
	h, calls := replay(rateLimited(30*time.Second, core.Usage{}), ok())
	var slept []time.Duration
	mw := Retry(RetryOptions{MaxAttempts: 3, Rand: func() float64 { return 0 },
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }})
	msg := mw(h)(context.Background(), core.Request{}).Result()
	if msg == nil || msg.StopReason != core.StopReasonStop || calls.Load() != 2 {
		t.Fatalf("calls=%d msg=%+v, want a success on the second attempt", calls.Load(), msg)
	}
	if len(slept) != 1 || slept[0] < 30*time.Second {
		t.Fatalf("slept %v before the retry, want at least the server's 30s", slept)
	}
}

// A delay past the ceiling is not slept: the semantic layer returns the
// failure, as the transport layer abandons such a request (REQ-PROV-13).
func TestRetryDoesNotSleepPastTheServerDelayCeiling(t *testing.T) {
	h, calls := replay(rateLimited(time.Hour, core.Usage{}), ok())
	var slept []time.Duration
	mw := Retry(RetryOptions{MaxAttempts: 3,
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }})
	msg := mw(h)(context.Background(), core.Request{}).Result()
	if calls.Load() != 1 || len(slept) != 0 || msg.StopReason != core.StopReasonError {
		t.Fatalf("calls=%d slept=%v stop=%q; a Retry-After of an hour must not be slept or retried",
			calls.Load(), slept, msg.StopReason)
	}
}

// Issue #75 §1: an attempt the layer discards was still billed. Its usage is
// reported through the context, so it reaches Agent.Usage.
func TestRetryReportsTheUsageOfDiscardedAttempts(t *testing.T) {
	var u core.Usage
	u.SetField(core.UsageInputTokens, 50_000)
	u.SetCost(0.25)
	h, _ := replay(rateLimited(0, u), ok())
	var reported core.Usage
	ctx := core.WithUsageReporter(context.Background(), func(x core.Usage) { reported = reported.Add(x) })
	_ = Retry(RetryOptions{MaxAttempts: 2, Sleep: func(context.Context, time.Duration) error { return nil }})(h)(ctx, core.Request{}).Result()
	if reported.CostUSD != 0.25 || reported.InputTokens != 50_000 {
		t.Fatalf("reported %+v, want the discarded attempt's $0.25 and 50K input tokens", reported)
	}
}
