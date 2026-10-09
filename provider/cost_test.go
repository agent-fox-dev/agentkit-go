package provider_test

import (
	"math"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

func near(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %.10f, want %.10f", what, got, want)
	}
}

// priced is a model whose rates are round numbers, so every expectation below
// is arithmetic a reader can check in their head rather than a golden value.
func priced() *core.Model {
	return &core.Model{ID: "m", InputCostPerMillion: 3, OutputCostPerMillion: 15,
		CacheReadCostPerMillion: 0.3, CacheWriteCostPerMillion: 3.75}
}

func TestCostIsRatePerMillionTokens(t *testing.T) {
	var u core.Usage
	u.SetField(core.UsageInputTokens, 1_000_000)
	u.SetField(core.UsageOutputTokens, 1_000_000)
	near(t, provider.ComputeCost(priced(), u), 18, "cost")
}

// TestOneHourCacheWritesBillAtTwiceBaseInput is REQ-PROV-05.3, and the trap is
// the word SUBSET.
//
// cache_write_1h is inside cache_write, not beside it. Treating them as two
// addends bills the 1h portion twice — once at the cache-write rate and again
// at 2x input — which is a 150% overcharge on exactly the long-lived agent
// session that opted into 1h retention to save money.
func TestOneHourCacheWritesBillAtTwiceBaseInput(t *testing.T) {
	var u core.Usage
	u.SetField(core.UsageCacheWriteTokens, 1_000_000)
	u.SetField(core.UsageCacheWrite1hTokens, 400_000)

	// 600k at the 3.75 cache-write rate + 400k at 2 x 3 base input.
	want := 0.6*3.75 + 0.4*6
	near(t, provider.ComputeCost(priced(), u), want, "cost")

	var naive core.Usage
	naive.SetField(core.UsageCacheWriteTokens, 1_000_000)
	if provider.ComputeCost(priced(), u) <= provider.ComputeCost(priced(), naive) {
		t.Fatal("a 1h write must cost MORE than the same volume of 5m writes")
	}
}

// TestServiceTierMultipliesPostHoc is REQ-PROV-05.6. Post-hoc matters: folding
// the multiplier into the rates would apply it before tier selection.
func TestServiceTierMultipliesPostHoc(t *testing.T) {
	var u core.Usage
	u.SetField(core.UsageInputTokens, 1_000_000)
	base := provider.ComputeCost(priced(), u)
	near(t, provider.ApplyServiceTier(base, provider.ServiceTier{Name: "flex", Multiplier: 0.5}), 1.5, "flex")
	near(t, provider.ApplyServiceTier(base, provider.ServiceTier{Name: "priority", Multiplier: 2}), 6, "priority")
	near(t, provider.ApplyServiceTier(base, provider.ServiceTier{}), 3, "unset tier is a no-op")
}

// TestAFallbackServedResponseIsBilledAtTheServedModelsRates is REQ-PROV-05.5.
//
// A server-side refusal fallback serves a cheaper or dearer model than the one
// requested. Billing the requested row is silently wrong, and the only symptom
// is a budget gate that fires at the wrong time.
func TestAFallbackServedResponseIsBilledAtTheServedModelsRates(t *testing.T) {
	requested := priced()
	served := &core.Model{ID: "cheap", InputCostPerMillion: 0.25, OutputCostPerMillion: 1.25}
	lookup := func(id string) *core.Model {
		if id == "cheap" {
			return served
		}
		return nil
	}

	m, billed := provider.BillingModel(requested, "cheap", lookup)
	if billed != "cheap" || m != served {
		t.Fatalf("BillingModel = (%v, %q), want the served row recorded as billed_model", m, billed)
	}

	// And when a later event names the requested model again, the SAME call
	// reprices BACK. This is why cost is computed once from the final name
	// rather than accumulated per event.
	m2, billed2 := provider.BillingModel(requested, requested.ID, lookup)
	if m2 != requested || billed2 != "" {
		t.Fatalf("repricing back gave (%v, %q), want the requested row and no billed_model",
			m2, billed2)
	}
}

func TestAnUnknownServedModelStillRecordsTheServedName(t *testing.T) {
	requested := priced()
	m, billed := provider.BillingModel(requested, "some-model-shipped-today", nil)
	if m != requested {
		t.Fatal("an unknown served model bills at the requested rates: the alternative " +
			"is billing a fallback-served response at zero")
	}
	if billed != "some-model-shipped-today" {
		t.Fatalf("billed_model = %q, want the served name recorded even when its row "+
			"is unknown", billed)
	}
}

func TestNegativeFiveMinuteWriteVolumeDoesNotCreditTheCaller(t *testing.T) {
	var u core.Usage
	u.SetField(core.UsageCacheWriteTokens, 100)
	u.SetField(core.UsageCacheWrite1hTokens, 500) // nonsense: 1h exceeds the total
	if got := provider.ComputeCost(priced(), u); got < 0 {
		t.Fatalf("cost = %v; a provider reporting something we do not model must never "+
			"produce a negative charge", got)
	}
}
