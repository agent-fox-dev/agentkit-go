package ollama_test

import (
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
)

func intPtr(n int) *int           { return &n }
func floatPtr(f float64) *float64 { return &f }
func strPtr(s string) *string     { return &s }

// sentOptions streams req against a fresh server for m and returns the options of
// the request body, and the body.
func sentOptions(t *testing.T, m core.Model, req core.Request) (map[string]any, map[string]any) {
	t.Helper()
	srv := onePlainTurn(t)
	run(t, newClient(t, srv, m), req)
	body := sentBody(t, srv, 0)
	opts, _ := body["options"].(map[string]any)
	return opts, body
}

// TS-28-13: num_predict is the requested bound when it is positive and within
// the model's cap, else the cap, else the requested bound, else left out.
func TestNumPredict_TS28_13(t *testing.T) {
	tests := []struct {
		name      string
		requested *int
		cap       int
		want      any // nil means absent
	}{
		{"requested within the cap", intPtr(1000), 32768, float64(1000)},
		{"requested above the cap", intPtr(50000), 32768, float64(32768)},
		{"none requested", nil, 32768, float64(32768)},
		{"requested and no cap", intPtr(1000), 0, float64(1000)},
		{"none requested and no cap", nil, 0, nil},
		{"zero requested and no cap", intPtr(0), 0, nil},
		{"zero requested", intPtr(0), 32768, float64(32768)},
		{"requested at the cap", intPtr(32768), 32768, float64(32768)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := userRequest("hi")
			req.MaxTokens = tc.requested
			opts, _ := sentOptions(t, core.Model{ID: "m", ContextWindow: 8192, MaxOutputTokens: tc.cap}, req)
			got, present := opts["num_predict"]
			if tc.want == nil && present || tc.want != nil && got != tc.want {
				t.Errorf("num_predict = %v (present %v), want %v", got, present, tc.want)
			}
		})
	}
}

// TS-28-14: sampling options are sent only when the request sets them, a
// temperature of 0 is sent, and keep_alive, format and images never are.
func TestSamplingOptions_TS28_14(t *testing.T) {
	req := userRequest("hi")
	req.Temperature, req.TopP, req.StopSequences = floatPtr(0), floatPtr(0.9), []string{"X"}
	srv := onePlainTurn(t)
	run(t, newClient(t, srv, testModel()), req)
	opts, _ := sentBody(t, srv, 0)["options"].(map[string]any)
	stop, _ := opts["stop"].([]any)
	if v, ok := opts["temperature"]; !ok || v != float64(0) || opts["top_p"] != 0.9 || len(stop) != 1 || stop[0] != "X" {
		t.Errorf("options = %v, want temperature 0, top_p 0.9 and stop [X]", opts)
	}

	bare, body := sentOptions(t, testModel(), userRequest("hi"))
	for _, k := range []string{"temperature", "top_p", "stop"} {
		if _, ok := bare[k]; ok {
			t.Errorf("a request with no sampling fields sent options.%s", k)
		}
	}
	for _, b := range []string{string(srv.Requests()[0].Body), mustJSON(t, body)} {
		for _, k := range []string{"keep_alive", "format", "images"} {
			if strings.Contains(b, `"`+k+`"`) {
				t.Errorf("a request body carries %q: %s", k, b)
			}
		}
	}
}

// TS-28-23: an empty effort, or a model that takes no thinking, sends no think.
func TestNoThinkField_TS28_23(t *testing.T) {
	withEfforts := core.Model{ID: "m", ThinkingKind: core.ThinkingKindAdaptive,
		Efforts: map[core.Effort]*string{core.EffortHigh: strPtr("high")}}
	none := core.Model{ID: "m", ThinkingKind: core.ThinkingKindNone, Efforts: withEfforts.Efforts}
	for name, c := range map[string]struct {
		m      core.Model
		effort core.Effort
	}{"an empty effort": {withEfforts, ""}, "a model with no thinking": {none, core.EffortHigh}} {
		req := userRequest("hi")
		req.Effort = c.effort
		if _, body := sentOptions(t, c.m, req); body["think"] != nil {
			t.Errorf("%s: think = %v, want no think key", name, body["think"])
		} else if _, present := body["think"]; present {
			t.Errorf("%s: the body has a think key", name)
		}
	}
}

// TS-28-24: with no Efforts map the effort is sent by its own name, as a string.
func TestThinkByName_TS28_24(t *testing.T) {
	req := userRequest("hi")
	req.Effort = core.EffortHigh
	if _, body := sentOptions(t, core.Model{ID: "unlisted-model"}, req); body["think"] != "high" {
		t.Errorf("think = %v, want \"high\"", body["think"])
	}
}

// TS-28-25: with an Efforts map an absent or nil effort sends nothing and a listed
// one sends its value trimmed and lower-cased, never another level.
func TestThinkFromTheEffortsMap_TS28_25(t *testing.T) {
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	custom := core.Model{ID: "m", Efforts: map[core.Effort]*string{core.EffortMedium: strPtr(" Medium "), core.EffortLow: nil}}
	for _, c := range []struct {
		name   string
		m      core.Model
		effort core.Effort
		want   any // nil means no think key
	}{
		{"xhigh on the catalog row", sel.Model, core.EffortXHigh, nil},
		{"medium on the catalog row", sel.Model, core.EffortMedium, "medium"},
		{"max on the catalog row", sel.Model, core.EffortMax, "max"},
		{"a listed value is trimmed and lower-cased", custom, core.EffortMedium, "medium"},
		{"a nil level", custom, core.EffortLow, nil},
		{"an absent level", custom, core.EffortHigh, nil},
	} {
		req := userRequest("hi")
		req.Effort = c.effort
		_, body := sentOptions(t, c.m, req)
		got, present := body["think"]
		if c.want == nil && present || c.want != nil && got != c.want {
			t.Errorf("%s: think = %v (present %v), want %v", c.name, got, present, c.want)
		}
	}
}
