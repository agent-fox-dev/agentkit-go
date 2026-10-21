package agentkit

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// reportedUsage is a usage a provider reported: the counters and their presence
// bits, as UsageWire sets them, with no cost.
func reportedUsage(in, out int64) core.Usage {
	var u core.Usage
	core.UsageWire{InputTokens: &in, OutputTokens: &out}.Into(&u)
	return u
}

const (
	// An id the catalog lists for no provider.
	unlistedClaude = "claude-3-5-sonnet"
	unlistedVendor = "some-vendor/unlisted-model"
	// A usage chunk without a cost, and with one.
	noCostUsage   = `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`
	withCostUsage = `{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"cost":0.0001}`
)

// runOn builds an agent from cfg, runs one prompt and returns the result.
func runOn(t *testing.T, cfg Config) (*Agent, core.RunResult) {
	t.Helper()
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return a, res
}

// noRates reports whether a row has all four per-million rates at zero.
func noRates(m core.Model) bool {
	return m.InputCostPerMillion == 0 && m.OutputCostPerMillion == 0 &&
		m.CacheReadCostPerMillion == 0 && m.CacheWriteCostPerMillion == 0
}

// TS-29-34: a run on a model with no price, whose cost the vendor did not
// report, ends Unpriced: an Anthropic id the catalog does not list, and an
// OpenRouter id it does not list whose response carried no cost.
func TestUnlistedModelsAreUnpriced_TS29_34(t *testing.T) {
	anth := anthropicServer(t)
	a, res := runOn(t, Config{Model: unlistedClaude, Getenv: (&envRecorder{vars: map[string]string{
		"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": anth.URL()}}).Getenv})
	if !res.Usage.Unpriced || !noRates(a.Resolution().Model) || !res.Usage.Reported() {
		t.Errorf("Anthropic on %s: unpriced %v, reported %v, row without rates %v; want true, true, true",
			unlistedClaude, res.Usage.Unpriced, res.Usage.Reported(), noRates(a.Resolution().Model))
	}

	or := openRouterServer(t, noCostUsage)
	a, res = runOn(t, Config{Provider: "openrouter", Model: unlistedVendor, Getenv: (&envRecorder{vars: map[string]string{
		"OPENROUTER_API_KEY": "k", "OPENROUTER_BASE_URL": or.URL()}}).Getenv})
	if !res.Usage.Unpriced || !noRates(a.Resolution().Model) || !res.Usage.Reported() {
		t.Errorf("OpenRouter on %s: unpriced %v, reported %v, row without rates %v; want true, true, true",
			unlistedVendor, res.Usage.Unpriced, res.Usage.Reported(), noRates(a.Resolution().Model))
	}
}

// TS-29-35: an Ollama run is never Unpriced: zero is its price. That holds for
// the provider the agent builds and for any client given in its place, because
// the rule follows the resolved provider's name.
func TestOllamaRunsAreNeverUnpriced_TS29_35(t *testing.T) {
	ol := ollamaServer(t)
	getenv := (&envRecorder{vars: map[string]string{"OLLAMA_HOST": ol.URL()}}).Getenv
	for _, model := range []string{"STANDARD", "an-unlisted:7b"} {
		_, res := runOn(t, Config{Provider: "ollama", Model: model, Getenv: getenv})
		if res.Usage.Unpriced || !res.Usage.Reported() {
			t.Errorf("Ollama on %s: unpriced %v, reported %v; want false, true", model, res.Usage.Unpriced, res.Usage.Reported())
		}
	}
	// A client of the embedder's, which reports usage and no cost.
	for _, model := range []string{"STANDARD", "an-unlisted:7b"} {
		wrapped := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}, Usage: reportedUsage(10, 5)})
		a, res := runOn(t, Config{Client: wrapped, Provider: "ollama", Model: model})
		if res.Usage.Unpriced || !res.Usage.Reported() || !noRates(a.Resolution().Model) {
			t.Errorf("a given client on Ollama %s: unpriced %v, reported %v, row without rates %v; want false, true, true",
				model, res.Usage.Unpriced, res.Usage.Reported(), noRates(a.Resolution().Model))
		}
	}
}

// TS-29-36: Unpriced stays false for a model with a price, for an OpenRouter
// response that carried a cost, and for a turn that reported no usage.
func TestPricedRunsAreNotUnpriced_TS29_36(t *testing.T) {
	anth := anthropicServer(t)
	a, res := runOn(t, Config{Model: "claude-opus-5-5", Getenv: (&envRecorder{vars: map[string]string{
		"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": anth.URL()}}).Getenv})
	if res.Usage.Unpriced || noRates(a.Resolution().Model) || res.Usage.CostUSD == 0 {
		t.Errorf("a listed Anthropic model: unpriced %v, cost %v, row without rates %v; want false, a cost, a priced row",
			res.Usage.Unpriced, res.Usage.CostUSD, noRates(a.Resolution().Model))
	}

	or := openRouterServer(t, withCostUsage)
	a, res = runOn(t, Config{Provider: "openrouter", Model: unlistedVendor, Getenv: (&envRecorder{vars: map[string]string{
		"OPENROUTER_API_KEY": "k", "OPENROUTER_BASE_URL": or.URL()}}).Getenv})
	if res.Usage.Unpriced || !noRates(a.Resolution().Model) || res.Usage.CostUSD != 0.0001 {
		t.Errorf("an OpenRouter response with a cost: unpriced %v, cost %v; want false and the vendor's 0.0001", res.Usage.Unpriced, res.Usage.CostUSD)
	}

	// Usage the provider did not report at all (a zero presence mask).
	silent := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}})
	_, res = runOn(t, Config{Client: silent, Provider: "anthropic", Model: unlistedClaude})
	if res.Usage.Unpriced {
		t.Error("a turn with no reported usage was flagged Unpriced")
	}
}

// TS-29-37: an Unpriced turn reaches the run's result, the done event's result
// and aggregate, and Agent.Usage; the turn-end event keeps the provider's own
// usage.
func TestUnpricedReachesEveryRunLevelUsage_TS29_37(t *testing.T) {
	anth := anthropicServer(t)
	a, err := New(Config{Model: unlistedClaude, Getenv: (&envRecorder{vars: map[string]string{
		"ANTHROPIC_API_KEY": "k", "ANTHROPIC_BASE_URL": anth.URL()}}).Getenv})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Stream(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	var done *core.AgentDoneEvent
	var turn *core.TurnEndEvent
	for e := range s.Events() {
		switch v := e.(type) {
		case core.AgentDoneEvent:
			done = &v
		case core.TurnEndEvent:
			turn = &v
		}
	}
	res, err := s.RunResult()
	if err != nil {
		t.Fatal(err)
	}
	if done == nil || turn == nil {
		t.Fatalf("done event %v, turn-end event %v", done, turn)
	}
	if !res.Usage.Unpriced || !done.Result.Usage.Unpriced || !done.Usage.Unpriced || !a.Usage().Unpriced {
		t.Errorf("unpriced: result %v, done result %v, done aggregate %v, Agent.Usage %v; want all true",
			res.Usage.Unpriced, done.Result.Usage.Unpriced, done.Usage.Unpriced, a.Usage().Unpriced)
	}
	if turn.Usage.Unpriced {
		t.Error("TurnEndEvent.Usage is flagged Unpriced; it is the provider's own usage of the turn")
	}
}

// TS-29-38: an unpriced turn is still priced at zero with the cost bit set, and
// MaxCostUSD keeps reading CostUSD: an unpriced run is not stopped by it, a
// priced one is.
func TestUnpricedTurnsKeepTheirCostAndTheBudget_TS29_38(t *testing.T) {
	tool := func() []core.Tool { return []core.Tool{echoTool("echo", nil)} }

	// Two turns on a model with no price, under a budget of a cent.
	unpriced := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("c1", "echo", "{}")}, StopReason: core.StopReasonToolUse, Usage: reportedUsage(100_000, 100_000)},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, Usage: reportedUsage(100_000, 100_000)},
	)
	a, res := runOn(t, Config{Client: unpriced, Model: unlistedClaude, MaxCostUSD: 0.01, Toolset: Toolset{Tools: tool()}})
	if !res.Usage.Unpriced || res.Usage.CostUSD != 0 || !res.Usage.Has(core.UsageCostUSD) || a.Usage().CostUSD != 0 {
		t.Errorf("unpriced run: unpriced %v, cost %v, cost bit %v; want true, 0, true", res.Usage.Unpriced, res.Usage.CostUSD, res.Usage.Has(core.UsageCostUSD))
	}
	if res.StopReason != core.RunStopEndTurn || res.TurnCount != 2 {
		t.Errorf("unpriced run stopped %q after %d turns, want to end its turn after 2: a cost of zero is under any budget", res.StopReason, res.TurnCount)
	}

	// The same shape on a model with a price: the first turn costs more than the
	// budget, so the second request is not sent. The usage stays well inside the
	// model's window, which the loop checks against the reported size.
	priced := faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("c1", "echo", "{}")}, StopReason: core.StopReasonToolUse, Usage: reportedUsage(100_000, 100_000)},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, Usage: reportedUsage(100_000, 100_000)},
	)
	b, err := New(Config{Client: priced, Model: "claude-opus-5-5", MaxCostUSD: 0.01, Toolset: Toolset{Tools: tool()}})
	if err != nil {
		t.Fatal(err)
	}
	res, err = b.Run(context.Background(), "hi")
	if !errors.Is(err, core.ErrBudgetExceeded) || res.StopReason != core.RunStopBudgetExceeded || res.TurnCount != 1 {
		t.Errorf("priced run = %v, stop %q after %d turns; want the budget error after 1 turn", err, res.StopReason, res.TurnCount)
	}
	if res.Usage.Unpriced || res.Usage.CostUSD <= 0.01 {
		t.Errorf("priced run: unpriced %v, cost %v; want false and a cost over the budget of 0.01", res.Usage.Unpriced, res.Usage.CostUSD)
	}
}

// The agent only raises the flag: one a client set on its own usage (the field
// is exported) reaches the run's usage, whatever the agent's own rule says.
func TestAFlagTheClientSetIsKept(t *testing.T) {
	u := reportedUsage(10, 5)
	u.Unpriced = true
	client := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("ok")}, Usage: u})
	_, res := runOn(t, Config{Client: client, Provider: "ollama", Model: "STANDARD"})
	if !res.Usage.Unpriced {
		t.Error("the flag the client set on the turn's usage was cleared by the agent")
	}
}
