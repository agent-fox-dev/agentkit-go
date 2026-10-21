package agentkit

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// TS-26-39 (smoke, 26-PATH-4): an existing embedder builds an agent through the
// unchanged catalog.Lookup and runs one turn with the limits and prices the
// claude-sonnet-5-5 row had before spec 26.
//
// A core.Request carries no model, so the model New resolved is read from the
// agent, the model the run reports from its assistant message, and the output
// cap and prices from the request and the priced usage.
func TestSmokeAgentNewStillResolvesThroughLookup_TS26_39(t *testing.T) {
	fp := faux.New(faux.Turn{
		Blocks:     []core.ContentBlock{core.TextBlock{Text: "hello"}},
		StopReason: core.StopReasonStop,
		Usage:      core.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000},
	})
	a, err := New(Config{Provider: fp, Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	want, listed := catalog.Lookup("claude-sonnet-5-5")
	if !listed || !reflect.DeepEqual(a.model, want) {
		t.Errorf("New resolved %+v, want catalog.Lookup's row %+v (listed=%v)", a.model, want, listed)
	}
	// The row's values before spec 26, written out.
	m := a.model
	if m.ID != "claude-sonnet-5-5" || m.ContextWindow != 1_000_000 || m.MaxOutputTokens != 128_000 ||
		m.InputCostPerMillion != 2 || m.OutputCostPerMillion != 10 {
		t.Errorf("model = %q window %d cap %d prices %v/%v, want claude-sonnet-5-5 1000000 128000 2/10",
			m.ID, m.ContextWindow, m.MaxOutputTokens, m.InputCostPerMillion, m.OutputCostPerMillion)
	}

	res, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != core.RunStopEndTurn || res.FinalText() != "hello" {
		t.Errorf("run = %v %q, want a completed turn answering hello", res.StopReason, res.FinalText())
	}

	reqs := fp.Requests()
	if len(reqs) != 1 || reqs[0].MaxTokens == nil || *reqs[0].MaxTokens != DefaultMaxTokens {
		t.Fatalf("requests = %+v, want one with MaxTokens %d (below the model's 128000 cap)", reqs, DefaultMaxTokens)
	}

	var answered string
	for _, msg := range res.Messages {
		if am, ok := msg.(core.AssistantMessage); ok {
			answered = am.Model
		}
	}
	if answered != "claude-sonnet-5-5" {
		t.Errorf("the assistant message names model %q, want claude-sonnet-5-5", answered)
	}
	// 1M input at $2 and 1M output at $10 per million: the faux provider did
	// not price the usage, so the loop priced it at the row.
	if math.Abs(res.Usage.CostUSD-12) > 1e-9 {
		t.Errorf("run cost = %v USD, want 12", res.Usage.CostUSD)
	}
}
