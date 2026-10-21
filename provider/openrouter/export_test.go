package openrouter

import (
	"context"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// RetryDelay exposes the wait between attempts to the tests: the retries made
// so far, the Retry-After header, the clock and a source of jitter in [0, 1).
func RetryDelay(retries int, retryAfter string, now time.Time, jitter func() float64) time.Duration {
	return retryDelay(retries, retryAfter, now, jitter)
}

// SetSleep replaces how the provider waits between attempts, so a test records
// the waits instead of sitting through them.
func SetSleep(p core.ProviderClient, sleep func(context.Context, time.Duration) error) {
	p.(*client).sleep = sleep
}
