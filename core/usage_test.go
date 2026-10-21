package core

import (
	"math/rand"
	"testing"
)

// TS-29-32 (property): for any two usages, Add leaves Unpriced true when either
// was, and combines every other field as it did before the flag existed: the
// counters and the cost sum, the presence masks OR, and a later BilledModel
// replaces an earlier one.
func TestUsageAddOrsUnpriced_TS29_32(t *testing.T) {
	r := rand.New(rand.NewSource(32))
	models := []string{"", "", "claude-opus-5-5", "qwen/qwen3.8-flash"}
	random := func() Usage {
		return Usage{
			InputTokens: r.Int63n(1_000_000), OutputTokens: r.Int63n(1_000_000),
			ReasoningTokens: r.Int63n(1_000), CacheReadTokens: r.Int63n(1_000_000),
			CacheWriteTokens: r.Int63n(1_000_000), CacheWrite1hTokens: r.Int63n(1_000),
			TotalTokens: r.Int63n(1_000_000), Requests: r.Int63n(10),
			CostUSD: float64(r.Intn(100_000)) / 100, BilledModel: models[r.Intn(len(models))],
			Set: UsageField(r.Intn(256)), Unpriced: r.Intn(2) == 0,
		}
	}
	seen := map[[2]bool]bool{}
	for range 2000 {
		a, b := random(), random()
		want := a
		want.InputTokens += b.InputTokens
		want.OutputTokens += b.OutputTokens
		want.ReasoningTokens += b.ReasoningTokens
		want.CacheReadTokens += b.CacheReadTokens
		want.CacheWriteTokens += b.CacheWriteTokens
		want.CacheWrite1hTokens += b.CacheWrite1hTokens
		want.TotalTokens += b.TotalTokens
		want.Requests += b.Requests
		want.CostUSD += b.CostUSD
		want.Set |= b.Set
		if b.BilledModel != "" {
			want.BilledModel = b.BilledModel
		}
		want.Unpriced = a.Unpriced || b.Unpriced

		if got := a.Add(b); got != want {
			t.Fatalf("(%+v).Add(%+v) = %+v, want %+v", a, b, got, want)
		}
		seen[[2]bool{a.Unpriced, b.Unpriced}] = true
	}
	if len(seen) != 4 {
		t.Errorf("the generator reached %d of the 4 combinations of the two flags", len(seen))
	}
}
