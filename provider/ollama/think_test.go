package ollama_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// think follows the model's Efforts map: none for an empty effort or a model
// that takes no thinking; the effort's own name with no map; with a map, the
// listed value trimmed and lower-cased, and none for a level absent or nil.
func TestThinkIsSentAsTheModelsEffortMapSays(t *testing.T) {
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	custom := map[core.Effort]*string{core.EffortMedium: ptr(" Medium "), core.EffortLow: nil, core.EffortHigh: ptr(" ")}
	for _, tc := range []struct {
		name   string
		m      core.Model
		effort core.Effort
		want   any // nil: no think key
	}{
		{"an empty effort", core.Model{ID: "m", ThinkingKind: core.ThinkingKindAdaptive, Efforts: custom}, "", nil},
		{"a model with no thinking", core.Model{ID: "m", ThinkingKind: core.ThinkingKindNone}, core.EffortHigh, nil},
		{"no map", core.Model{ID: "m"}, core.EffortHigh, "high"},
		{"xhigh on the catalog row", sel.Model, core.EffortXHigh, nil},
		{"medium on the catalog row", sel.Model, core.EffortMedium, "medium"},
		{"max on the catalog row", sel.Model, core.EffortMax, "max"},
		{"a listed value", core.Model{ID: "m", Efforts: custom}, core.EffortMedium, "medium"},
		{"a nil level", core.Model{ID: "m", Efforts: custom}, core.EffortLow, nil},
		{"a blank level", core.Model{ID: "m", Efforts: custom}, core.EffortHigh, nil},
		{"an absent level", core.Model{ID: "m", Efforts: custom}, core.EffortMax, nil},
	} {
		req := userRequest("hi")
		req.Effort = tc.effort
		got, has := sentBody(t, tc.m, req)["think"]
		if tc.want == nil && has || tc.want != nil && got != tc.want {
			t.Errorf("%s: think = %v (present %v), want %v", tc.name, got, has, tc.want)
		}
	}
}
