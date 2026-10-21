package openrouter

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// retryBase is the wait before the first retry; it doubles for each retry
	// up to retryCap, and up to a quarter of it is added as jitter.
	retryBase = 500 * time.Millisecond
	retryCap  = 8 * time.Second
	// retryAfterMax is the longest Retry-After the provider waits for. A longer
	// one is not honoured, so a turn fails within a bounded time.
	retryAfterMax = 60 * time.Second
)

// failure is how an attempt ended when it did not finish the turn.
type failure struct {
	err  error
	text string
	// retryable is whether the attempt may be sent again: a 429 or a 5xx, as an
	// HTTP status or as an error inside the stream before any content.
	retryable  bool
	retryAfter string // the response's Retry-After header
}

// retryableCode reports whether an error code is one to try again.
func retryableCode(code int) bool { return code == 429 || (code >= 500 && code <= 599) }

// retryDelay is how long to wait before the next attempt, given the retries
// made so far, the response's Retry-After header, the time now and a source of
// jitter in [0, 1). A Retry-After of 0 to 60 seconds, as a number of seconds or
// an HTTP date, is the wait. Otherwise the wait is 0.5 s doubling for each retry
// up to 8 s, with up to 25% added.
func retryDelay(retries int, retryAfter string, now time.Time, jitter func() float64) time.Duration {
	if d, ok := parseRetryAfter(retryAfter, now); ok && d >= 0 && d <= retryAfterMax {
		return d
	}
	base := retryBase
	for i := 0; i < retries && base < retryCap; i++ {
		base = min(base*2, retryCap)
	}
	return base + time.Duration(float64(base)*0.25*jitter())
}

// parseRetryAfter reads a Retry-After header: a number of seconds, or an HTTP
// date, which is taken relative to now.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 || n > 24*60*60 {
			return 0, false
		}
		return time.Duration(n) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now), true
	}
	return 0, false
}

// sleepCtx waits for d, or until ctx is done, whose error it then returns.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
