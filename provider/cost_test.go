package provider_test

import (
	"math"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// priced has round rates, so every expected cost is arithmetic a reader can
// check in their head.
func priced() *core.Model {
	return &core.Model{ID: "m", InputCostPerMillion: 3, OutputCostPerMillion: 15,
		CacheReadCostPerMillion: 0.3, CacheWriteCostPerMillion: 3.75}
}

func usage(fields map[core.UsageField]int64) core.Usage {
	var u core.Usage
	for f, v := range fields {
		u.SetField(f, v)
	}
	return u
}

func TestComputeCostPricesEachTokenKindPerMillion(t *testing.T) {
	cases := []struct {
		name  string
		model *core.Model
		u     core.Usage
		want  float64
	}{
		{"input and output", priced(), usage(map[core.UsageField]int64{
			core.UsageInputTokens: 1_000_000, core.UsageOutputTokens: 1_000_000}), 18},
		{"cache reads", priced(), usage(map[core.UsageField]int64{core.UsageCacheReadTokens: 1_000_000}), 0.3},
		// The 1h writes are a SUBSET of all writes: 600k at the write rate and
		// 400k at twice base input, never both rates on the 400k.
		{"1h writes bill at twice base input", priced(), usage(map[core.UsageField]int64{
			core.UsageCacheWriteTokens: 1_000_000, core.UsageCacheWrite1hTokens: 400_000}), 0.6*3.75 + 0.4*6},
		{"more 1h writes than writes never credits", priced(), usage(map[core.UsageField]int64{
			core.UsageCacheWriteTokens: 100, core.UsageCacheWrite1hTokens: 500}), 500 * 6 / 1e6},
		{"no model costs nothing", nil, usage(map[core.UsageField]int64{core.UsageInputTokens: 5}), 0},
	}
	for _, c := range cases {
		if got := provider.ComputeCost(c.model, c.u); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: cost = %.10f, want %.10f", c.name, got, c.want)
		}
	}
}

func TestServiceTiersMultiplyTheComputedCost(t *testing.T) {
	cases := []struct {
		name  string
		known bool
		want  float64 // a cost of 3 after the tier
	}{
		{"flex", true, 1.5}, {"PRIORITY", true, 6}, {" standard ", true, 3},
		{"default", true, 3}, {"auto", true, 3}, {"turbo", false, 3},
	}
	for _, c := range cases {
		tier, ok := provider.KnownServiceTier(c.name)
		if ok != c.known {
			t.Errorf("KnownServiceTier(%q) known = %v, want %v", c.name, ok, c.known)
		}
		if got := provider.ApplyServiceTier(3, tier); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%q: cost = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBillingModelBillsTheModelThatServed(t *testing.T) {
	requested := priced()
	cheap := &core.Model{ID: "cheap", InputCostPerMillion: 0.25}
	lookup := func(id string) *core.Model {
		if id == "cheap" {
			return cheap
		}
		return nil
	}
	cases := []struct {
		name, served string
		lookup       func(string) *core.Model
		model        *core.Model
		billed       string
	}{
		{"a fallback is billed at its own row", "cheap", lookup, cheap, "cheap"},
		{"naming the requested model again reprices back", "m", lookup, requested, ""},
		{"no served name bills the request", "", lookup, requested, ""},
		{"an unknown served model keeps the requested rates and its own name", "new-model", lookup, requested, "new-model"},
		{"no lookup still records the served name", "cheap", nil, requested, "cheap"},
	}
	for _, c := range cases {
		m, billed := provider.BillingModel(requested, c.served, c.lookup)
		if m != c.model || billed != c.billed {
			t.Errorf("%s: BillingModel = (%s, %q), want (%s, %q)", c.name, m.ID, billed, c.model.ID, c.billed)
		}
	}
}
