package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// With the provider, a tier or a model id, and an effort taken from the
// environment, the example sends that model with that effort (the tier's when
// none is set, none for an id the catalog does not list), prints the reply and
// a summary that names what ran, and consults no other provider's variables.
func TestTheVariablesChooseTheProviderModelAndEffort(t *testing.T) {
	srv := newOllamaServer(t)
	simple, _ := tierModel(t, "SIMPLE")
	standard, standardEffort := tierModel(t, "STANDARD")
	foreign := map[string]string{"ANTHROPIC_AUTH_TOKEN": "decoy", "ANTHROPIC_OAUTH_TOKEN": "decoy", "CLAUDE_CODE_USE_VERTEX": "1",
		"ANTHROPIC_VERTEX_PROJECT_ID": "decoy", "CLAUDE_CODE_USE_BEDROCK": "1", "AWS_REGION": "decoy"}
	for _, c := range []struct {
		name, model, effort string
		think               any // nil: no think sent
		summary             []string
	}{
		{"SIMPLE", simple, "high", "high", []string{"ollama", simple, "effort high"}},
		{"", standard, "", standardEffort, []string{"ollama", standard, "effort " + standardEffort, "1 turns", "in 10 / out 5 tokens"}},
		{"some-other-model:1b", "some-other-model:1b", "", nil, []string{"some-other-model:1b", "effort none"}},
	} {
		set := map[string]string{"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": srv.URL, "AGENTKIT_MODEL": c.name, "AGENTKIT_EFFORT": c.effort}
		if c.name == "SIMPLE" {
			for k, v := range foreign {
				set[k] = v
			}
		}
		_, before := srv.requests()
		r, hits := runExample(t, set)
		_, chats := srv.requests()
		if chats = chats[len(before):]; r.Exit != 0 || !strings.Contains(r.Stdout, reply) || hits != 0 || len(chats) != 1 {
			t.Fatalf("AGENTKIT_MODEL=%q: exit %d, %d decoy connections, %d chats\nstdout:\n%s\nstderr:\n%s", c.name, r.Exit, hits, len(chats), r.Stdout, r.Stderr)
		}
		if think, sent := chats[0]["think"]; chats[0]["model"] != c.model || think != c.think || sent != (c.think != nil) {
			t.Errorf("AGENTKIT_MODEL=%q: the server got %v, want a chat for %q with think %v", c.name, chats[0], c.model, c.think)
		}
		for _, want := range c.summary {
			if line := summaryLine(r); !strings.Contains(line, want) {
				t.Errorf("AGENTKIT_MODEL=%q: the summary %q does not name %q", c.name, line, want)
			}
		}
		// The model answered in text and never called submit_answer; the example says so.
		if !strings.Contains(r.Stdout, "run ended without submit_answer (end_turn)") {
			t.Errorf("AGENTKIT_MODEL=%q: stdout does not say the run ended without submit_answer:\n%s", c.name, r.Stdout)
		}
	}
}

// A configuration that cannot run is agentkit.New's error, printed on a line
// that starts "error: ", with exit status 1 and no request sent anywhere.
func TestAConfigurationThatCannotRunFailsBeforeAnyRequest(t *testing.T) {
	srv := newOllamaServer(t)
	for _, c := range []struct {
		name  string
		set   map[string]string
		unset []string
		want  []string
	}{
		{"an unknown provider", map[string]string{"AGENTKIT_PROVIDER": "openai"}, nil, []string{"openai", "anthropic", "openrouter", "ollama"}},
		{"an unknown effort", map[string]string{"AGENTKIT_PROVIDER": "ollama", "AGENTKIT_EFFORT": "bogus"}, nil, []string{"bogus"}},
		{"no OpenRouter key", map[string]string{"AGENTKIT_PROVIDER": "openrouter"}, []string{"OPENROUTER_API_KEY"}, []string{"OPENROUTER_API_KEY"}},
		{"an unusable Ollama host", map[string]string{"AGENTKIT_PROVIDER": "ollama", "OLLAMA_HOST": "ftp://nowhere"}, nil, []string{"ftp"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, ok := c.set["OLLAMA_HOST"]; !ok {
				c.set["OLLAMA_HOST"] = srv.URL
			}
			r, hits := runExample(t, c.set, c.unset...)
			var line string
			for _, l := range strings.Split(r.Stdout+"\n"+r.Stderr, "\n") {
				if strings.HasPrefix(l, "error: ") {
					line = l
				}
			}
			for _, want := range c.want {
				if r.Exit != 1 || !strings.Contains(line, want) || hits != 0 {
					t.Errorf("exit %d, error line %q, %d decoy connections; want 1, a line naming %q, none\n%s\n%s", r.Exit, line, hits, want, r.Stdout, r.Stderr)
				}
			}
		})
	}
	t.Cleanup(func() {
		if n, _ := srv.requests(); n != 0 {
			t.Errorf("the server received %d requests, want none", n)
		}
	})
}

// A run whose usage is flagged unpriced, even with part of a cost, shows "cost
// unknown" and no dollar figure; a priced one shows its figure.
func TestTheSummaryNamesWhatRanAndItsCostOrThatTheCostIsUnknown(t *testing.T) {
	r := agentkit.Resolution{Provider: "openrouter", Model: core.Model{ID: "vendor/some-model"}, Effort: core.EffortHigh}
	for _, c := range []struct {
		usage      core.Usage
		cost, none string
	}{
		{core.Usage{InputTokens: 100, OutputTokens: 20, Unpriced: true}, "cost unknown", "$"},
		{core.Usage{InputTokens: 100, OutputTokens: 20, Unpriced: true, CostUSD: 0.5}, "cost unknown", "$"},
		{core.Usage{InputTokens: 100, OutputTokens: 20, CostUSD: 0.0123}, "$0.0123", "cost unknown"},
	} {
		s := summary(r, core.RunResult{TurnCount: 2, StopReason: core.RunStopEndTurn, Usage: c.usage})
		for _, want := range []string{"openrouter", "vendor/some-model", "effort high", "2 turns", "in 100 / out 20 tokens", c.cost} {
			if !strings.Contains(s, want) || strings.Contains(s, c.none) {
				t.Errorf("summary %q: want %q and no %q", s, want, c.none)
			}
		}
	}
	if s := summary(agentkit.Resolution{Provider: "ollama", Model: core.Model{ID: "m"}}, core.RunResult{}); !strings.Contains(s, "effort none") {
		t.Errorf("the summary %q of a run with no effort, want \"effort none\"", s)
	}
}

func TestThePartsDeskToolsAnswerAndRefuseAsTheModelIsTold(t *testing.T) {
	var submitted answer
	r, err := agentkit.NewRunner(agentkit.Toolset{Tools: []core.Tool{lookupPartTool(), convertUnitsTool(), reserveStockTool(), submitAnswerTool(&submitted)}})
	if err != nil {
		t.Fatal(err)
	}
	stock := inventory["BRKT-90"].OnHand
	t.Cleanup(func() { inventory["BRKT-90"].OnHand = stock })
	for _, c := range []struct{ tool, input, want string }{
		// lookup_part takes ids as a list, a comma-separated string or one {"id": ...} object.
		{"lookup_part", `{"ids":"bolt-m6, NUT-M6,nope","include_specs":true}`,
			`{"known_ids":["BOLT-M6","BRKT-90","NUT-M6","WSHR-M6"],"missing":["nope"],"parts":[{"id":"bolt-m6","mass_grams":12.4,"material":"stainless","name":"M6 hex bolt, 30mm","on_hand":480},{"id":"NUT-M6","mass_grams":3.1,"material":"stainless","name":"M6 nylon-insert nut","on_hand":512}]}`},
		{"lookup_part", `{"ids":{"id":"WSHR-M6"}}`,
			`{"known_ids":["BOLT-M6","BRKT-90","NUT-M6","WSHR-M6"],"missing":null,"parts":[{"id":"WSHR-M6","name":"M6 flat washer","on_hand":1500}]}`},
		{"convert_units", `{"value":1,"from":"lb","to":"oz","decimals":2}`, `{"dimension":"mass","unit":"oz","value":16}`},
		{"convert_units", `{"value":1,"from":"g","to":"mm"}`, "dimension_mismatch"},
		{"reserve_stock", `{"id":"brkt-90","quantity":2}`, `{"id":"brkt-90","remaining":` + fmt.Sprint(stock-2) + `,"reserved":2,"reserved_mass_grams":172}`},
		{"reserve_stock", `{"id":"BRKT-90","quantity":1000}`, "insufficient_stock"},
		{"reserve_stock", `{"id":"NOPE","quantity":1}`, "unknown_part"},
		{"submit_answer", `{"answer":"42 g","parts":["BOLT-M6"]}`, `{"received":true}`},
	} {
		res := r.Call(context.Background(), c.tool, json.RawMessage(c.input))
		if got := testutil.MustJSON(t, res.Data); res.OK && !testutil.SameJSON([]byte(got), []byte(c.want)) || !res.OK && res.Error != c.want {
			t.Errorf("%s %s: %+v (data %s), want %s", c.tool, c.input, res, got, c.want)
		}
	}
	if submitted.Text != "42 g" || len(submitted.Parts) != 1 {
		t.Errorf("submit_answer kept %+v", submitted)
	}
	if got := compact(json.RawMessage("{\n  \"a\":  1\n}")); got != `{ "a": 1 }` {
		t.Errorf("compact = %q", got)
	}
	if got := compact(json.RawMessage(strings.Repeat("x", 100))); len(got) != 90 || !strings.HasSuffix(got, "...") {
		t.Errorf("a long input compacts to %q", got)
	}
}
