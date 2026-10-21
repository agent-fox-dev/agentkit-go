package openrouter_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// effortModel is the catalog's shape: low, medium, high and max, each mapped to
// its own name, and no xhigh.
func effortModel() core.Model {
	m := testModel()
	m.Efforts = map[core.Effort]*string{
		core.EffortLow: ptr("low"), core.EffortMedium: ptr("medium"), core.EffortHigh: ptr("high"), core.EffortMax: ptr("max"),
	}
	return m
}

// reasoningOf streams one request with the effort and returns the sent body's
// reasoning object, and whether there was one.
func reasoningOf(t *testing.T, m core.Model, effort core.Effort) (map[string]any, bool) {
	t.Helper()
	srv := newReplay(t, okTurn())
	req := userRequest("hi")
	req.Effort = effort
	run(t, newProviderFor(t, srv, m), req)
	r, has := sentBody(t, srv, 0)["reasoning"]
	if !has {
		return nil, false
	}
	return r.(map[string]any), true
}

// TS-27-16: an empty effort sends no reasoning, a nil map sends the effort by
// its name, and a level the map lacks, or maps to nil, sends nothing.
func TestEffortFollowsTheModelsMap_TS27_16(t *testing.T) {
	if r, has := reasoningOf(t, effortModel(), ""); has {
		t.Errorf("an empty effort sent reasoning %v", r)
	}

	nilMap := testModel()
	if r, has := reasoningOf(t, nilMap, core.EffortHigh); !has || r["effort"] != "high" || len(r) != 1 {
		t.Errorf("a nil Efforts map sent %v (present %v), want effort high", r, has)
	}

	if r, has := reasoningOf(t, effortModel(), core.EffortXHigh); has {
		t.Errorf("a level the map lacks sent reasoning %v", r)
	}

	nilEntry := effortModel()
	nilEntry.Efforts[core.EffortLow] = nil
	if r, has := reasoningOf(t, nilEntry, core.EffortLow); has {
		t.Errorf("a nil map entry sent reasoning %v", r)
	}
}

// TS-27-17: a numeric map value is sent as reasoning.max_tokens, a name as
// reasoning.effort, no effort is moved to another level, and sampling stays.
func TestEffortValuesAndSampling_TS27_17(t *testing.T) {
	m := effortModel()
	m.Efforts[core.EffortHigh] = ptr("8192")
	m.Efforts[core.EffortLow] = ptr("medium")

	r, has := reasoningOf(t, m, core.EffortHigh)
	if !has || r["max_tokens"] != float64(8192) || len(r) != 1 {
		t.Errorf("a budget value sent %v (present %v), want max_tokens 8192 and no effort", r, has)
	}
	r, has = reasoningOf(t, m, core.EffortLow)
	if !has || r["effort"] != "medium" || len(r) != 1 {
		t.Errorf("a mapped name sent %v (present %v), want effort medium and no max_tokens (the map is not clamped, it is read)", r, has)
	}

	srv := newReplay(t, okTurn())
	req := userRequest("hi")
	req.Effort, req.Temperature, req.TopP = core.EffortMax, ptr(0.3), ptr(0.9)
	run(t, newProviderFor(t, srv, effortModel()), req)
	b := sentBody(t, srv, 0)
	if r, _ := b["reasoning"].(map[string]any); r["effort"] != "max" || b["temperature"] != 0.3 || b["top_p"] != 0.9 {
		t.Errorf("body reasoning = %v, temperature = %v, top_p = %v; want max, 0.3 and 0.9", b["reasoning"], b["temperature"], b["top_p"])
	}
}
