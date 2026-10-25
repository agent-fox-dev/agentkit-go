package core

import (
	"context"
	"math/rand"
	"testing"
)

func TestUsageSetFieldWritesTheFieldAndItsPresenceBit(t *testing.T) {
	for f, get := range map[UsageField]func(Usage) int64{
		UsageInputTokens:        func(u Usage) int64 { return u.InputTokens },
		UsageOutputTokens:       func(u Usage) int64 { return u.OutputTokens },
		UsageReasoningTokens:    func(u Usage) int64 { return u.ReasoningTokens },
		UsageCacheReadTokens:    func(u Usage) int64 { return u.CacheReadTokens },
		UsageCacheWriteTokens:   func(u Usage) int64 { return u.CacheWriteTokens },
		UsageCacheWrite1hTokens: func(u Usage) int64 { return u.CacheWrite1hTokens },
		UsageTotalTokens:        func(u Usage) int64 { return u.TotalTokens },
	} {
		var u Usage
		u.SetField(f, 42)
		if get(u) != 42 || !u.Has(f) || u.Set != f {
			t.Errorf("SetField(%b) = %+v", f, u)
		}
	}
	var u Usage
	u.SetCost(0.25)
	if u.CostUSD != 0.25 || !u.Has(UsageCostUSD) || u.Has(UsageInputTokens) {
		t.Fatalf("SetCost = %+v", u)
	}
}

func TestUsageWireIntoSetsPresenceForExactlyTheReportedFields(t *testing.T) {
	zero, ten := int64(0), int64(10)
	var u Usage
	UsageWire{InputTokens: &zero, OutputTokens: &ten, ReasoningTokens: &ten, CacheReadTokens: &ten,
		CacheWriteTokens: &ten, CacheWrite1hTokens: &ten}.Into(&u)
	if !u.Reported() || !u.Has(UsageInputTokens) || u.Has(UsageTotalTokens) || u.OutputTokens != 10 || u.CacheWrite1hTokens != 10 {
		t.Fatalf("Into = %+v; an explicit 0 must set its bit and an absent field must not", u)
	}
	var none Usage
	UsageWire{}.Into(&none)
	if none.Reported() {
		t.Fatal("nothing reported must not read as reported")
	}
	// Both are zero as an anchor: IsZero is a different question from Reported.
	var reportedZero Usage
	UsageWire{InputTokens: &zero}.Into(&reportedZero)
	if !reportedZero.IsZero() || !none.IsZero() || (Usage{OutputTokens: 1}).IsZero() {
		t.Fatal("IsZero must be true exactly when the context tokens are zero")
	}
}

func TestUsageContextTokensPrefersTheTotal(t *testing.T) {
	u := Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 2, CacheWriteTokens: 1, ReasoningTokens: 4}
	if u.ContextTokens() != 18 {
		t.Fatalf("ContextTokens() = %d, want the components' 18", u.ContextTokens())
	}
	u.TotalTokens = 100
	if u.ContextTokens() != 100 {
		t.Fatalf("ContextTokens() = %d, want TotalTokens to win", u.ContextTokens())
	}
}

// Over random pairs: the counters and the cost sum, the masks and Unpriced
// OR, and a later BilledModel replaces an earlier one.
func TestUsageAddSumsCountersAndCombinesFlags(t *testing.T) {
	r := rand.New(rand.NewSource(32))
	models := []string{"", "", "claude-opus-5-5", "qwen/qwen3.8-flash"}
	random := func() Usage {
		return Usage{
			InputTokens: r.Int63n(1_000_000), OutputTokens: r.Int63n(1_000_000), ReasoningTokens: r.Int63n(1_000),
			CacheReadTokens: r.Int63n(1_000_000), CacheWriteTokens: r.Int63n(1_000_000), CacheWrite1hTokens: r.Int63n(1_000),
			TotalTokens: r.Int63n(1_000_000), Requests: r.Int63n(10), CostUSD: float64(r.Intn(100_000)) / 100,
			BilledModel: models[r.Intn(len(models))], Set: UsageField(r.Intn(256)), Unpriced: r.Intn(2) == 0,
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

func TestReportUsageGoesToTheContextsReporterIfAny(t *testing.T) {
	var got []Usage
	ctx := WithUsageReporter(context.Background(), func(u Usage) { got = append(got, u) })
	ReportUsage(ctx, Usage{InputTokens: 7})
	ReportUsage(context.Background(), Usage{InputTokens: 8}) // no reporter: nothing happens
	ReportUsage(WithUsageReporter(context.Background(), nil), Usage{InputTokens: 9})
	if len(got) != 1 || got[0].InputTokens != 7 {
		t.Fatalf("reported %+v, want the one usage on the reporting context", got)
	}
}
