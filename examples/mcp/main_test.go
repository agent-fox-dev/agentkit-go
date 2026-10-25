package main

import (
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
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
		// The MCP half needs nothing installed: the in-process server's tools come back qualified.
		for _, want := range []string{"tools discovered over MCP: [", "docs__list_topics", "direct call to docs__list_topics: ok=true", "shadowing refused"} {
			if !strings.Contains(r.Stdout, want) {
				t.Errorf("AGENTKIT_MODEL=%q: stdout lacks %q:\n%s", c.name, want, r.Stdout)
			}
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
