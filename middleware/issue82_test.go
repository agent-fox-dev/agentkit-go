package middleware

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// strictSpan enforces core.Tracer's contract: the span lives until End, and
// nothing may be written to it after End. It is NOT ended when StartSpan's
// callback returns.
type strictSpan struct {
	mu      sync.Mutex
	ended   int
	late    int
	attrs   map[string]any
	endedCh chan struct{}
}

func (s *strictSpan) write(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended > 0 {
		s.late++
		return
	}
	f()
}
func (s *strictSpan) SetAttributes(kv map[string]any) {
	s.write(func() {
		for k, v := range kv {
			s.attrs[k] = v
		}
	})
}
func (s *strictSpan) SetStatus(error)                 { s.write(func() {}) }
func (s *strictSpan) AddEvent(string, map[string]any) { s.write(func() {}) }
func (s *strictSpan) End() {
	s.mu.Lock()
	s.ended++
	if s.ended == 1 {
		close(s.endedCh)
	}
	s.mu.Unlock()
}

type strictTracer struct{ span *strictSpan }

func (t *strictTracer) StartSpan(_ string, fn func(core.Span) error) error { return fn(t.span) }

// Issue #82 §2: Tracing follows the documented contract — attributes written
// before End, End exactly once — so an adapter built to the contract records
// them. (core/trace.go now states that contract; it used to say the span was
// "callback-scoped", which Tracing never honoured and an OTel adapter would
// have taken literally.)
func TestTracingFollowsTheSpanContract(t *testing.T) {
	msg := core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "hi"}}, StopReason: core.StopReasonStop,
		Provider: "acme", Model: "m1"}
	msg.Usage.SetCost(0.01)
	h, _ := handlerReturning(msg)
	sp := &strictSpan{attrs: map[string]any{}, endedCh: make(chan struct{})}
	_ = Tracing(&strictTracer{span: sp})(h)(context.Background(), core.Request{}).Result()
	select {
	case <-sp.endedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the span was never ended")
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.ended != 1 || sp.late != 0 {
		t.Fatalf("End called %d times, %d writes after End", sp.ended, sp.late)
	}
	if sp.attrs["stop_reason"] == nil || sp.attrs["provider"] == nil {
		t.Fatalf("attributes = %v; the model call's attributes must land before End", sp.attrs)
	}
}

// Issue #82 §3: the exponential backoff saturates at MaxDelay however many
// attempts there are; `BaseDelay << (attempt-1)` overflowed to a negative
// duration after attempt 35 and to zero at 64, a retry storm.
func TestBackoffSaturatesInsteadOfOverflowing(t *testing.T) {
	o := RetryOptions{MaxAttempts: 100, Rand: func() float64 { return 0 }}.withDefaults()
	for _, attempt := range []int{1, 5, 35, 36, 40, 63, 64, 65, 1000} {
		d := backoff(o, attempt)
		if d <= 0 || d > o.MaxDelay {
			t.Errorf("backoff(%d) = %v, want within (0, %v]", attempt, d, o.MaxDelay)
		}
	}
	if d := backoff(o, 64); d != o.MaxDelay {
		t.Errorf("backoff(64) = %v, want the %v ceiling", d, o.MaxDelay)
	}
}

// Issue #82 §3: a zero, negative or NaN rate is a misread config, not "no
// limit": every call fails with a clear error instead of passing unthrottled.
func TestRateLimitRefusesANonPositiveRate(t *testing.T) {
	h, calls := handlerReturning(core.AssistantMessage{StopReason: core.StopReasonStop})
	for _, rate := range []float64{0, -1, math.NaN()} {
		msg := RateLimit(rate, 1)(h)(context.Background(), core.Request{}).Result()
		if msg == nil || msg.StopReason != core.StopReasonError {
			t.Errorf("RateLimit(%v): stop %v, want an error", rate, msg)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("%d requests went through a rate limiter configured with no rate", calls.Load())
	}
}
