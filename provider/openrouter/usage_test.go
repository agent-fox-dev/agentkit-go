package openrouter_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// usageOf is the final usage of a turn that answers ok and then reports usage
// (none when usage is "").
func usageOf(t *testing.T, m core.Model, usage string) core.Usage {
	t.Helper()
	rep := okTurn()
	if usage != "" {
		rep = okTurn(usageChunk(usage))
	}
	o := run(t, newProvider(t, newServer(t, rep), m), userRequest("hi"))
	if o.err != nil {
		t.Fatalf("the turn failed: %v", o.err)
	}
	return o.msg.Usage
}

// The input is net of the cache counters and never negative, reasoning is
// within the output, and a presence bit is set for each field reported.
func TestUsageIsNettedWithAPresenceBitPerReportedField(t *testing.T) {
	const (
		in, read, write, out, reason, total, cost = core.UsageInputTokens, core.UsageCacheReadTokens, core.UsageCacheWriteTokens,
			core.UsageOutputTokens, core.UsageReasoningTokens, core.UsageTotalTokens, core.UsageCostUSD
	)
	for _, tc := range []struct {
		usage string
		want  core.Usage
		set   core.UsageField
	}{
		{`{"prompt_tokens":354,"completion_tokens":120,"total_tokens":474,"cost":0.00012,` +
			`"prompt_tokens_details":{"cached_tokens":256},"completion_tokens_details":{"reasoning_tokens":80}}`,
			core.Usage{InputTokens: 98, CacheReadTokens: 256, OutputTokens: 120, ReasoningTokens: 80, TotalTokens: 474, CostUSD: 0.00012},
			in | read | out | reason | total | cost},
		{`{"prompt_tokens":354,"completion_tokens":120,"prompt_tokens_details":{"cached_tokens":256,"cache_write_tokens":50}}`,
			core.Usage{InputTokens: 48, CacheReadTokens: 256, CacheWriteTokens: 50, OutputTokens: 120}, in | read | write | out},
		{`{"prompt_tokens":10,"completion_tokens":5}`, core.Usage{InputTokens: 10, OutputTokens: 5}, in | out},
		{`{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":80,"cache_write_tokens":70},` +
			`"completion_tokens_details":{"reasoning_tokens":30}}`,
			core.Usage{CacheReadTokens: 80, CacheWriteTokens: 70, OutputTokens: 10, ReasoningTokens: 10}, in | read | write | out | reason},
		{`{"prompt_tokens":0,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}`, core.Usage{}, in | read},
		{`{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, core.Usage{}, 0},
	} {
		want := tc.want
		want.Set, want.Requests, want.BilledModel = tc.set, 1, "vendor/served"
		if u := usageOf(t, testModel(), tc.usage); u != want {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.usage, u, want)
		}
	}
}

// The cost is the one the response reported, else the model's price applied
// to the usage, else unset; a response with no usage stays unreported.
func TestTheCostIsReportedElsePricedElseUnset(t *testing.T) {
	counts := `"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500`
	priced := pricedModel()
	computed := provider.ComputeCost(&priced, core.Usage{InputTokens: 1000, OutputTokens: 500})
	for _, tc := range []struct {
		name  string
		model core.Model
		usage string
		cost  float64
		has   bool
	}{
		{"reported, priced model", priced, `{` + counts + `,"cost":0.5}`, 0.5, true},
		{"reported, free model", testModel(), `{` + counts + `,"cost":0.25}`, 0.25, true},
		{"unreported, priced model", priced, `{` + counts + `}`, computed, true},
		{"unreported, free model", testModel(), `{` + counts + `}`, 0, false},
		{"no usage at all", priced, "", 0, false},
	} {
		u := usageOf(t, tc.model, tc.usage)
		if u.CostUSD != tc.cost || u.Has(core.UsageCostUSD) != tc.has || tc.usage == "" && u.Reported() {
			t.Errorf("%s: cost %v (bit %v, reported %v), want %v (bit %v)", tc.name, u.CostUSD, u.Has(core.UsageCostUSD), u.Reported(), tc.cost, tc.has)
		}
	}
	if computed == 0 {
		t.Fatal("the priced model computes no cost")
	}
}
