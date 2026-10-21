package ollama_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// usageDone is the done chunk with its counts. A cached count of -1 leaves
// prompt_eval_cached_count out of the chunk, as a server that does not report it
// does.
func usageDone(prompt, cached, eval int) string {
	cachedField := ""
	if cached >= 0 {
		cachedField = fmt.Sprintf(`,"prompt_eval_cached_count":%d`, cached)
	}
	return fmt.Sprintf(`{"model":"served-model","created_at":"2026-10-21T10:00:00Z",`+
		`"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop",`+
		`"prompt_eval_count":%d%s,"eval_count":%d}`, prompt, cachedField, eval)
}

// usageOf streams a turn that ends in the done chunk and returns the final
// usage.
func usageOf(t *testing.T, m core.Model, done string) core.Usage {
	t.Helper()
	srv := newRecorder(t, lines(chunk("ok", ""), done))
	o := run(t, newClient(t, srv, m), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	return o.msg.Usage
}

// TS-28-40: the counts of the done chunk are netted and priced at zero, with the
// cost's presence bit set, whether the model id is listed or not.
func TestUsageIsNettedAndPricedAtZero_TS28_40(t *testing.T) {
	const bits = core.UsageInputTokens | core.UsageOutputTokens | core.UsageCacheReadTokens | core.UsageCostUSD
	for _, id := range []string{"qwen3.8:27b", "model-nobody-lists"} {
		u := usageOf(t, core.Model{ID: id}, usageDone(354, 256, 120))
		want := core.Usage{InputTokens: 98, CacheReadTokens: 256, OutputTokens: 120, Requests: 1, CostUSD: 0, Set: bits}
		if u != want {
			t.Errorf("%s: usage = %+v, want %+v", id, u, want)
		}
		if !u.Has(core.UsageCostUSD) || u.Has(core.UsageReasoningTokens) {
			t.Errorf("%s: cost bit %v, reasoning bit %v; want set and clear", id, u.Has(core.UsageCostUSD), u.Has(core.UsageReasoningTokens))
		}
	}

	// A cached count of zero, and none at all, leave the cache read unset.
	for _, cached := range []int{0, -1} {
		u := usageOf(t, testModel(), usageDone(10, cached, 5))
		if u.Has(core.UsageCacheReadTokens) || u.CacheReadTokens != 0 || u.InputTokens != 10 || u.OutputTokens != 5 {
			t.Errorf("cached %d: usage = %+v, want 10 in, 5 out and no cache read", cached, u)
		}
	}

	// A done chunk that reports no count at all reports no usage and no cost.
	u := usageOf(t, testModel(), doneChunk("served-model", "stop"))
	if u.Set != 0 || u.Requests != 1 {
		t.Errorf("a done chunk without counts: usage = %+v, want nothing reported and one request", u)
	}
}

// TS-28-41 (property): the input is the prompt count less the cached count and
// never negative, the output is the eval count, and the cache read is set exactly
// when the cached count is positive.
func TestUsageNettingProperty_TS28_41(t *testing.T) {
	r := rand.New(rand.NewSource(41))
	pick := func() int {
		if r.Intn(4) == 0 {
			return 0
		}
		return r.Intn(5000)
	}
	type counts struct{ prompt, cached, eval int }
	var cases []counts
	var replies []reply
	for range 150 {
		c := counts{pick(), pick(), pick()}
		if c.prompt == 0 && c.eval == 0 {
			c.eval = 1 // a chunk with no count at all reports nothing
		}
		cases = append(cases, c)
		replies = append(replies, lines(chunk("ok", ""), usageDone(c.prompt, c.cached, c.eval)))
	}
	srv := newRecorder(t, replies...)
	client := newClient(t, srv, testModel())
	for i, c := range cases {
		u := run(t, client, userRequest("hi")).msg.Usage
		if u.InputTokens < 0 || u.InputTokens != int64(max(0, c.prompt-c.cached)) || u.OutputTokens != int64(c.eval) {
			t.Errorf("case %d %+v: usage = %+v, want input %d and output %d", i, c, u, max(0, c.prompt-c.cached), c.eval)
		}
		if u.Has(core.UsageCacheReadTokens) != (c.cached > 0) || (c.cached > 0 && u.CacheReadTokens != int64(c.cached)) {
			t.Errorf("case %d %+v: cache read bit %v, value %d", i, c, u.Has(core.UsageCacheReadTokens), u.CacheReadTokens)
		}
	}
}
