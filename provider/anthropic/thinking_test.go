package anthropic_test

import (
	"fmt"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// thinkingOf renders a body's thinking control: "-" when it sends none,
// "budget N" or "adaptive EFFORT", and whether the temperature survived.
func thinkingOf(body map[string]any) string {
	th, _ := body["thinking"].(map[string]any)
	oc, _ := body["output_config"].(map[string]any)
	out := "-"
	switch {
	case th != nil && th["type"] == "enabled":
		out = fmt.Sprintf("budget %v", th["budget_tokens"])
	case th != nil && th["type"] == "adaptive" && oc != nil:
		out = fmt.Sprintf("adaptive %v", oc["effort"])
	case th != nil || oc != nil:
		out = fmt.Sprintf("thinking %v output_config %v", th, oc)
	}
	if _, ok := body["temperature"]; ok {
		out += " +temperature"
	}
	return out
}

func efforts(kv ...string) map[core.Effort]*string {
	m := map[core.Effort]*string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[core.Effort(kv[i])] = new(kv[i+1])
	}
	return m
}

// An effort is sent as the row says the model takes it (a budget held below
// max_tokens and never under 1024, or an adaptive effort, dropping the
// sampling knobs either way), and not at all for a level the row lacks.
func TestEffortIsSentTheWayTheModelTakesIt(t *testing.T) {
	cases := []struct {
		name    string
		efforts map[core.Effort]*string
		kind    core.ThinkingKind
		maxOut  int
		effort  core.Effort
		want    string
	}{
		{"no effort", efforts("high", "2048"), "", 4096, "", "- +temperature"},
		{"a budget row", efforts("high", "2048"), "", 4096, "high", "budget 2048"},
		{"a level the row does not list", efforts("high", "2048"), "", 4096, "max", "- +temperature"},
		{"a budget at max_tokens is held below it", efforts("high", "4096"), "", 2048, "high", "budget 2047"},
		{"the minimum budget", efforts("high", "4096"), "", 1025, "high", "budget 1024"},
		{"a budget below the minimum sends none", efforts("high", "4096"), "", 1024, "high", "- +temperature"},
		{"a budget row with a non-number", efforts("high", "lots"), core.ThinkingKindBudget, 4096, "high", "- +temperature"},
		{"an effort row", efforts("high", " HIGH "), "", 64000, "high", "adaptive high"},
		{"minimal is sent as low", efforts("low", "minimal"), "", 64000, "low", "adaptive low"},
		{"an effort the API does not know", efforts("high", "turbo"), "", 64000, "high", "- +temperature"},
		{"an adaptive model with no row takes every level", nil, core.ThinkingKindAdaptive, 64000, "max", "adaptive max"},
		{"no row and no kind", nil, "", 64000, "high", "- +temperature"},
		{"a model without thinking", efforts("high", "2048"), core.ThinkingKindNone, 4096, "high", "- +temperature"},
	}
	for _, c := range cases {
		m := testModel()
		m.Efforts, m.ThinkingKind, m.MaxOutputTokens = c.efforts, c.kind, c.maxOut
		req := core.Request{Effort: c.effort, Temperature: new(0.3), TopP: new(0.9)}
		body := wireBody(t, m, req, anthropic.Options{})
		if got := thinkingOf(body); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
		if *req.Temperature != 0.3 || req.Effort != c.effort {
			t.Errorf("%s: building the request changed the caller's request", c.name)
		}
	}
}

// The shipped rows reach the wire as their models expect: the current
// generation takes adaptive effort and no sampling, the budget generation
// takes its row's budget, and a level a row lacks is never clamped to another.
func TestShippedRowsSendTheirModelsThinkingControl(t *testing.T) {
	for _, c := range []struct {
		id     string
		effort core.Effort
		want   string
	}{
		{"anthropic/claude-opus-5", core.EffortXHigh, "adaptive xhigh"},
		{"anthropic/claude-fable-5-1", core.EffortMax, "adaptive max"},
		{"claude-sonnet-5-5", core.EffortHigh, "adaptive high"},
		{"anthropic/claude-sonnet-4-6", core.EffortXHigh, "- +temperature"},
		{"anthropic/claude-haiku-4-5", core.EffortHigh, "budget 32768"},
		{"claude-sonnet-4-5", core.EffortLow, "budget 4096"},
	} {
		row, ok := catalog.Lookup(c.id)
		if !ok {
			t.Fatalf("%s is not in the catalog", c.id)
		}
		body := wireBody(t, &row, core.Request{Effort: c.effort, Temperature: new(0.5)}, anthropic.Options{})
		if got := thinkingOf(body); got != c.want {
			t.Errorf("%s at %s: %s, want %s", c.id, c.effort, got, c.want)
		}
	}
}
