package anthropic_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// wireOf builds req for m and returns the request body as JSON.
func wireOf(t *testing.T, m core.Model, req core.Request) map[string]any {
	t.Helper()
	body, _, err := anthropic.BuildRequest(&m, req, core.CacheRetentionNone)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func userTurn() core.Messages {
	return core.Messages{core.UserMessage{Content: core.Content{core.TextBlock{Text: "hi"}}}}
}

// TS-10-16: the five effort levels.
func TestEffortLevels_TS10_16(t *testing.T) {
	for got, want := range map[core.Effort]string{
		core.EffortLow: "low", core.EffortMedium: "medium", core.EffortHigh: "high",
		core.EffortXHigh: "xhigh", core.EffortMax: "max",
	} {
		if string(got) != want {
			t.Errorf("effort %q, want %q", got, want)
		}
	}
}

// TS-10-17: an adaptive model gets thinking {"type":"adaptive"} and the
// effort, and no budget.
func TestAdaptiveThinkingCarriesTheEffort_TS10_17(t *testing.T) {
	m, ok := catalog.Lookup("claude-sonnet-5-5")
	if !ok || m.Thinking != core.ThinkingKindAdaptive {
		t.Fatalf("claude-sonnet-5-5 = %v, %q; want an adaptive row", ok, m.Thinking)
	}
	w := wireOf(t, m, core.Request{Messages: userTurn(), Effort: core.EffortHigh})
	th, _ := w["thinking"].(map[string]any)
	if th == nil || th["type"] != "adaptive" {
		t.Fatalf("thinking = %v, want {\"type\":\"adaptive\"}", w["thinking"])
	}
	if _, ok := th["budget_tokens"]; ok {
		t.Fatalf("thinking = %v carries budget_tokens", th)
	}
	oc, _ := w["output_config"].(map[string]any)
	if oc == nil || oc["effort"] != "high" {
		t.Fatalf("output_config = %v, want effort high", w["output_config"])
	}

	// An uncataloged model is adaptive too, and takes every level.
	m, _ = catalog.Lookup("claude-next-gen-future")
	w = wireOf(t, m, core.Request{Messages: userTurn(), Effort: core.EffortMax})
	if oc, _ := w["output_config"].(map[string]any); oc == nil || oc["effort"] != "max" {
		t.Fatalf("uncataloged output_config = %v, want effort max", w["output_config"])
	}
}

// TS-10-18: a budget model gets budget_tokens from its row and no effort.
func TestBudgetThinkingCarriesTheRowsBudget_TS10_18(t *testing.T) {
	m, ok := catalog.Lookup("claude-sonnet-4-5")
	if !ok || m.Thinking != core.ThinkingKindBudget {
		t.Fatalf("claude-sonnet-4-5 = %v, %q; want a budget row", ok, m.Thinking)
	}
	w := wireOf(t, m, core.Request{Messages: userTurn(), Effort: core.EffortLow})
	th, _ := w["thinking"].(map[string]any)
	if th == nil || th["type"] != "enabled" || th["budget_tokens"] != float64(4096) {
		t.Fatalf("thinking = %v, want {\"type\":\"enabled\",\"budget_tokens\":4096}", w["thinking"])
	}
	if oc, _ := w["output_config"].(map[string]any); oc != nil && oc["effort"] != nil {
		t.Fatalf("output_config = %v carries an effort", oc)
	}
}

// TS-10-19: a model that takes no thinking gets neither key, and the request
// is not modified.
func TestNoThinkingModelOmitsBoth_TS10_19(t *testing.T) {
	m := core.Model{ID: "claude-no-thinking", API: anthropic.API, Provider: "anthropic",
		ContextWindow: 200000, MaxTokens: 4096, Thinking: core.ThinkingKindNone}
	temp := 0.3
	req := core.Request{Messages: userTurn(), Effort: core.EffortHigh, Temperature: &temp}
	before := req
	w := wireOf(t, m, req)
	if _, ok := w["thinking"]; ok {
		t.Fatalf("thinking = %v on a model without thinking", w["thinking"])
	}
	if _, ok := w["output_config"]; ok {
		t.Fatalf("output_config = %v on a model without thinking", w["output_config"])
	}
	if !reflect.DeepEqual(before, req) || *req.Temperature != 0.3 || req.Effort != core.EffortHigh {
		t.Fatal("building the request modified the caller's request")
	}
}
