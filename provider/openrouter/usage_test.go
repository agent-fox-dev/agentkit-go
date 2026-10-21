package openrouter_test

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// usageChunk is the chunk OpenRouter sends after the finish: no choices, and
// the usage.
func usageChunk(usage string) string {
	return data(fmt.Sprintf(`{"id":"gen-1","object":"chat.completion.chunk","created":1,"model":"vendor/served","choices":[],"usage":%s}`, usage))
}

// textTurn is a stream that answers "ok" and ends with stop; extra chunks
// follow the finish chunk.
func textTurn(after ...string) reply {
	parts := []string{chunk(text("ok"), ""), chunk(`{}`, "stop")}
	parts = append(parts, after...)
	return sse(append(parts, done)...)
}

// pricedModel has a price on every counter, in round numbers.
func pricedModel() core.Model {
	m := testModel()
	m.InputCostPerMillion, m.OutputCostPerMillion = 3, 15
	m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion = 0.3, 3.75
	return m
}

// usageOf runs a stream against a provider for m and returns the final usage.
func usageOf(t *testing.T, m core.Model, rep reply) core.Usage {
	t.Helper()
	srv := newReplay(t, rep)
	c, err := openrouter.New(m, openrouter.Options{
		Env: map[string]string{openrouter.APIKeyVar: "sk-or-test-key", openrouter.BaseURLVar: srv.URL()},
	})
	if err != nil {
		t.Fatal(err)
	}
	o := run(t, c, userRequest("hi"))
	if o.err != nil {
		t.Fatalf("the turn failed: %v", o.err)
	}
	return o.msg.Usage
}

// TS-27-30: after the finish chunk the provider reads on to the usage chunk,
// then emits the block ends in block order and the message end, whose message
// is the stream's result.
func TestUsageAfterTheFinishChunkReachesTheMessage_TS27_30(t *testing.T) {
	srv := newReplay(t, sse(
		chunk(text("hello"), ""),
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"read","arguments":"{}"}}]}`, ""),
		chunk(`{}`, "tool_calls"),
		usageChunk(`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`),
		done))
	o := run(t, newProvider(t, srv), userRequest("hi"))
	if o.err != nil {
		t.Fatal(o.err)
	}
	want := []string{"start", "text+0", "text~0", "tool+1", "tool~1", "text-0", "tool-1", "end"}
	if got := kinds(o.events); !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if !o.msg.Usage.Reported() || o.msg.Usage.InputTokens != 10 || o.msg.Usage.OutputTokens != 5 {
		t.Errorf("usage = %+v, want the later chunk's 10 and 5", o.msg.Usage)
	}
}

// TS-27-44: the usage of 354 prompt tokens (256 cached), 120 completion tokens
// (80 reasoning) and a cost is netted into core.Usage.
func TestUsageIsNetted_TS27_44(t *testing.T) {
	u := usageOf(t, testModel(), textTurn(usageChunk(
		`{"prompt_tokens":354,"completion_tokens":120,"total_tokens":474,"cost":0.00012,`+
			`"prompt_tokens_details":{"cached_tokens":256},"completion_tokens_details":{"reasoning_tokens":80}}`)))
	if u.InputTokens != 98 || u.CacheReadTokens != 256 || u.OutputTokens != 120 || u.ReasoningTokens != 80 ||
		u.TotalTokens != 474 || u.CostUSD != 0.00012 {
		t.Errorf("usage = %+v, want 98 input, 256 cache read, 120 output, 80 reasoning, 474 total and 0.00012", u)
	}
	for f, name := range map[core.UsageField]string{
		core.UsageInputTokens: "input", core.UsageCacheReadTokens: "cache read", core.UsageOutputTokens: "output",
		core.UsageReasoningTokens: "reasoning", core.UsageTotalTokens: "total", core.UsageCostUSD: "cost",
	} {
		if !u.Has(f) {
			t.Errorf("the %s presence bit is clear", name)
		}
	}
	if u.Has(core.UsageCacheWriteTokens) {
		t.Error("the cache write bit is set; the response reported none")
	}
	if u.Requests != 1 || u.BilledModel != "vendor/served" {
		t.Errorf("Requests = %d, BilledModel = %q; want 1 and the served model", u.Requests, u.BilledModel)
	}

	// Cache writes are netted out of the input as well.
	u = usageOf(t, testModel(), textTurn(usageChunk(
		`{"prompt_tokens":354,"completion_tokens":120,"total_tokens":474,`+
			`"prompt_tokens_details":{"cached_tokens":256,"cache_write_tokens":50}}`)))
	if u.CacheWriteTokens != 50 || u.InputTokens != 48 || !u.Has(core.UsageCacheWriteTokens) {
		t.Errorf("usage = %+v, want 50 cache write and 48 input", u)
	}

	// Only prompt and completion tokens reported: exactly those two bits.
	u = usageOf(t, testModel(), textTurn(usageChunk(`{"prompt_tokens":10,"completion_tokens":5}`)))
	if u.Set != core.UsageInputTokens|core.UsageOutputTokens {
		t.Errorf("presence bits = %b, want only input and output (%b)", u.Set, core.UsageInputTokens|core.UsageOutputTokens)
	}
}

// TS-27-45 (property): over generated counts the input is never negative, the
// reasoning tokens never exceed the output, and the input plus the cache
// counters give the larger of the prompt tokens and the cache counters.
func TestUsageNettingConserves_TS27_45(t *testing.T) {
	type counts struct{ prompt, cached, write, completion, reasoning int64 }
	r := rand.New(rand.NewSource(45))
	var cases []counts
	var replies []reply
	pick := func() int64 {
		if r.Intn(4) == 0 {
			return 0
		}
		return int64(r.Intn(2000))
	}
	for range 150 {
		c := counts{pick(), pick(), pick(), pick(), pick()}
		cases = append(cases, c)
		replies = append(replies, textTurn(usageChunk(fmt.Sprintf(
			`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,`+
				`"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":%d},`+
				`"completion_tokens_details":{"reasoning_tokens":%d}}`,
			c.prompt, c.completion, c.prompt+c.completion, c.cached, c.write, c.reasoning))))
	}
	srv := newReplay(t, replies...)
	prov := newProvider(t, srv)
	for i, c := range cases {
		u := run(t, prov, userRequest("hi")).msg.Usage
		if u.InputTokens < 0 || u.ReasoningTokens > u.OutputTokens {
			t.Errorf("%+v: usage = %+v, want a non-negative input and reasoning within the output", c, u)
		}
		if got, want := u.InputTokens+u.CacheReadTokens+u.CacheWriteTokens, max(c.prompt, c.cached+c.write); got != want {
			t.Errorf("case %d %+v: input + cache read + cache write = %d, want %d (usage %+v)", i, c, got, want, u)
		}
	}
}

// TS-27-47: the cost is the reported one, else the model's price, else unset.
func TestCostSources_TS27_47(t *testing.T) {
	counts := `"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500`
	priced := pricedModel()

	u := usageOf(t, priced, textTurn(usageChunk(`{`+counts+`,"cost":0.5}`)))
	if u.CostUSD != 0.5 || !u.Has(core.UsageCostUSD) {
		t.Errorf("reported cost: usage = %+v, want 0.5 with the cost bit", u)
	}

	u = usageOf(t, priced, textTurn(usageChunk(`{`+counts+`}`)))
	netted := u
	netted.CostUSD = 0
	if want := provider.ComputeCost(&priced, netted); u.CostUSD != want || want == 0 || !u.Has(core.UsageCostUSD) {
		t.Errorf("priced model: cost = %v (bit %v), want %v from the row's prices", u.CostUSD, u.Has(core.UsageCostUSD), want)
	}

	u = usageOf(t, testModel(), textTurn(usageChunk(`{`+counts+`}`)))
	if u.CostUSD != 0 || u.Has(core.UsageCostUSD) {
		t.Errorf("zero-price model: usage = %+v, want the cost left at 0 with its bit clear", u)
	}

	// A reported cost wins even where every price is zero.
	u = usageOf(t, testModel(), textTurn(usageChunk(`{`+counts+`,"cost":0.25}`)))
	if u.CostUSD != 0.25 || !u.Has(core.UsageCostUSD) {
		t.Errorf("reported cost on a zero-price model: usage = %+v, want 0.25 with the cost bit", u)
	}
}

// TS-27-48: a response with no usage leaves the usage unreported and unpriced.
func TestNoUsageStaysUnreported_TS27_48(t *testing.T) {
	u := usageOf(t, pricedModel(), textTurn())
	if u.Reported() || u.CostUSD != 0 || u.Has(core.UsageCostUSD) {
		t.Errorf("usage = %+v, want nothing reported and no cost", u)
	}
}
