package ollama_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// The input is the prompt count less the cached count, never negative; the
// output is the eval count; the cache read is set only when positive; any
// reported count prices the turn at 0 with the cost bit, listed model or not;
// a done chunk with no count reports only the request.
func TestUsageIsNettedAndPricedAtZero(t *testing.T) {
	const counted = core.UsageInputTokens | core.UsageOutputTokens | core.UsageCostUSD
	for _, tc := range []struct {
		model               string
		prompt, cached, out int
		want                core.Usage
	}{
		{"qwen3.8:27b", 354, 256, 120, core.Usage{InputTokens: 98, CacheReadTokens: 256, OutputTokens: 120, Set: counted | core.UsageCacheReadTokens}},
		{"model-nobody-lists", 354, 256, 120, core.Usage{InputTokens: 98, CacheReadTokens: 256, OutputTokens: 120, Set: counted | core.UsageCacheReadTokens}},
		{"m", 10, 0, 5, core.Usage{InputTokens: 10, OutputTokens: 5, Set: counted}},
		{"m", 10, -1, 5, core.Usage{InputTokens: 10, OutputTokens: 5, Set: counted}},
		{"m", 100, 300, 7, core.Usage{CacheReadTokens: 300, OutputTokens: 7, Set: counted | core.UsageCacheReadTokens}},
		{"m", 0, 0, 0, core.Usage{Set: counted}},
		{"m", 0, -1, 9, core.Usage{OutputTokens: 9, Set: counted}},
		{"m", 0, -1, 0, core.Usage{}},
	} {
		srv := newServer(t, lines(chunk("ok", ""), usageDone(tc.prompt, tc.cached, tc.out)))
		o := run(t, newClient(t, srv, core.Model{ID: tc.model}), userRequest("hi"))
		want := tc.want
		want.Requests = 1
		if o.err != nil || o.msg.Usage != want {
			t.Errorf("%+v:\n got %+v, %v\nwant %+v", tc, o.msg.Usage, o.err, want)
		}
	}
}
