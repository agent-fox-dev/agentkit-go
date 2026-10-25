package openrouter_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// The reasoning object follows the model's Efforts map: no map sends the
// effort by name; a level absent or nil sends nothing; a positive number is a
// token budget and anything else an effort name, read as written and never
// moved to another level; sampling is kept beside it.
func TestEffortIsSentAsTheModelsEffortMapSays(t *testing.T) {
	catalogMap := func() map[core.Effort]*string {
		return map[core.Effort]*string{core.EffortLow: ptr("low"), core.EffortMedium: ptr("medium"),
			core.EffortHigh: ptr("high"), core.EffortMax: ptr("max")}
	}
	for _, tc := range []struct {
		name    string
		efforts map[core.Effort]*string
		effort  core.Effort
		want    string // the reasoning object as JSON, "" for none
	}{
		{"no effort", catalogMap(), "", ""},
		{"no map", nil, core.EffortHigh, `{"effort":"high"}`},
		{"a level the map lacks", catalogMap(), core.EffortXHigh, ""},
		{"a nil entry", map[core.Effort]*string{core.EffortLow: nil}, core.EffortLow, ""},
		{"a blank entry", map[core.Effort]*string{core.EffortLow: ptr("  ")}, core.EffortLow, ""},
		{"a name", catalogMap(), core.EffortMax, `{"effort":"max"}`},
		{"another level's name", map[core.Effort]*string{core.EffortLow: ptr(" Medium ")}, core.EffortLow, `{"effort":"medium"}`},
		{"a budget", map[core.Effort]*string{core.EffortHigh: ptr("8192")}, core.EffortHigh, `{"max_tokens":8192}`},
		{"a number that is no budget", map[core.Effort]*string{core.EffortHigh: ptr("0")}, core.EffortHigh, `{"effort":"0"}`},
	} {
		m := testModel()
		m.Efforts = tc.efforts
		req := userRequest("hi")
		req.Effort, req.Temperature, req.TopP = tc.effort, ptr(0.3), ptr(0.9)
		body := sent(t, m, req)
		got, has := body["reasoning"]
		if tc.want == "" && has || tc.want != "" && mustJSON(got) != tc.want {
			t.Errorf("%s: reasoning = %v (present %v), want %s", tc.name, got, has, tc.want)
		}
		if mustJSON(body["temperature"]) != "0.3" || mustJSON(body["top_p"]) != "0.9" {
			t.Errorf("%s: temperature %v, top_p %v; want 0.3 and 0.9", tc.name, body["temperature"], body["top_p"])
		}
	}
}
