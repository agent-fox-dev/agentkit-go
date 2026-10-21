package agentkit

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// streamed runs one prompt on a through Stream and returns the AgentStartEvent
// and the run's result.
func streamed(t *testing.T, a *Agent) (core.AgentStartEvent, core.RunResult) {
	t.Helper()
	s, err := a.Stream(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	var start *core.AgentStartEvent
	for e := range s.Events() {
		if v, ok := e.(core.AgentStartEvent); ok && start == nil {
			start = &v
		}
	}
	res, err := s.RunResult()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if start == nil {
		t.Fatal("no AgentStartEvent")
	}
	return *start, res
}

// TS-29-45 (smoke, 29-PATH-1): an embedder names OpenRouter and a tier, and the
// run reaches a recording server with the tier's model and effort.
func TestSmokeOpenRouterByName_TS29_45(t *testing.T) {
	srv := openRouterServer(t, noCostUsage)
	env := &envRecorder{vars: map[string]string{"OPENROUTER_API_KEY": "sk-or-test", "OPENROUTER_BASE_URL": srv.URL()}}
	a, err := New(Config{Provider: "openrouter", Model: "STANDARD", Getenv: env.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	start, res := streamed(t, a)

	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].Path != "/chat/completions" {
		t.Fatalf("requests = %+v, want one POST /chat/completions", reqs)
	}
	body := requestBody(t, reqs[0])
	reasoning, _ := body["reasoning"].(map[string]any)
	if body["model"] != "qwen/qwen3.8-flash" || reasoning["effort"] != "medium" {
		t.Errorf("body model %v reasoning %v, want qwen/qwen3.8-flash and effort medium", body["model"], body["reasoning"])
	}
	if want := (core.AgentStartEvent{Provider: "openrouter", API: openrouter.API, Model: "qwen/qwen3.8-flash"}); start != want {
		t.Errorf("AgentStartEvent = %+v, want %+v", start, want)
	}
	if res.Usage.Unpriced || !res.Usage.Reported() {
		t.Errorf("usage: unpriced %v, reported %v; want a priced row's usage, reported", res.Usage.Unpriced, res.Usage.Reported())
	}
	if r := a.Resolution(); !r.Listed || r.Tier != catalog.TierStandard || r.Effort != EffortMedium {
		t.Errorf("Resolution = %+v, want a listed STANDARD row at medium", r)
	}
}

// TS-29-46 (smoke, 29-PATH-2): the same on Ollama, with no credential.
func TestSmokeOllamaByName_TS29_46(t *testing.T) {
	srv := ollamaServer(t)
	env := &envRecorder{vars: map[string]string{"OLLAMA_HOST": srv.URL()}}
	a, err := New(Config{Provider: "ollama", Model: "STANDARD", Getenv: env.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	start, res := streamed(t, a)

	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].Path != "/api/chat" {
		t.Fatalf("requests = %+v, want one POST /api/chat", reqs)
	}
	body := requestBody(t, reqs[0])
	options, _ := body["options"].(map[string]any)
	if body["model"] != "qwen3.8:27b" || body["think"] != "medium" || options["num_ctx"] != float64(32768) {
		t.Errorf("body model %v think %v options %v, want qwen3.8:27b, medium and num_ctx 32768", body["model"], body["think"], body["options"])
	}
	if res.Usage.Unpriced || start.Provider != "ollama" || start.API != ollama.API {
		t.Errorf("unpriced %v, start event %+v; want a run that is not Unpriced on provider ollama", res.Usage.Unpriced, start)
	}
}

// TS-29-47 (smoke, 29-PATH-3): Anthropic by name sends the body a direct provider
// sends, and an unlisted Claude id ends Unpriced with a cost of zero.
func TestSmokeAnthropicByteIdenticalAndUnlistedUnpriced_TS29_47(t *testing.T) {
	const listed = "claude-opus-5-5"
	row, _ := catalog.Lookup(listed)

	direct := anthropicServer(t)
	max := DefaultMaxTokens
	req := core.Request{
		System:    []core.ContentBlock{core.TextBlock{Text: prompt.Build("", nil)}},
		Messages:  core.Messages{userText("hi")},
		MaxTokens: &max,
	}
	vars := func(s *providerServer) map[string]string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "ANTHROPIC_BASE_URL": s.URL()}
	}
	ch, err := anthropic.Provider(row, anthropic.Options{Env: vars(direct), Getenv: noEnv}).Stream(context.Background(), req)
	for range core.EventStreamOf(ch, err).Events() {
	}

	srv := anthropicServer(t)
	a, err := New(Config{Model: listed, Env: vars(srv), Getenv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	_, res := streamed(t, a)
	if got, want := srv.requests()[0].Body, direct.requests()[0].Body; !bytes.Equal(got, want) {
		t.Errorf("the agent sent\n%s\nwant the direct provider's\n%s", got, want)
	}
	if res.Usage.Unpriced || res.Usage.CostUSD == 0 {
		t.Errorf("listed run: unpriced %v, cost %v; want false and a cost", res.Usage.Unpriced, res.Usage.CostUSD)
	}

	srv = anthropicServer(t)
	b, err := New(Config{Model: unlistedClaude, Env: vars(srv), Getenv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	_, res = streamed(t, b)
	if !res.Usage.Unpriced || res.Usage.CostUSD != 0 || !res.Usage.Has(core.UsageCostUSD) {
		t.Errorf("unlisted run: unpriced %v, cost %v, cost bit %v; want true, 0 and true",
			res.Usage.Unpriced, res.Usage.CostUSD, res.Usage.Has(core.UsageCostUSD))
	}
}

// TS-29-48 (smoke, 29-PATH-4): with every provider's secrets set, an agent run on
// each provider in turn reaches only that provider's server, sends none of the
// other providers' secrets, and asks Getenv for no variable that is not its own.
func TestSmokeProviderIsolation_TS29_48(t *testing.T) {
	r := newRig(t, fixedSecrets)
	for _, p := range providers {
		namesBefore := len(r.env.asked())
		before := map[string]int{}
		for name, s := range r.servers {
			before[name] = s.count()
		}
		r.run(t, p)

		for name, s := range r.servers {
			grew := s.count() - before[name]
			if (name == p) != (grew > 0) {
				t.Errorf("on %s the %s server received %d new requests", p, name, grew)
			}
		}
		for _, name := range r.env.asked()[namesBefore:] {
			if !ownsVariable(p, name) || strings.HasPrefix(name, "AGENTKIT_") {
				t.Errorf("on %s Getenv was asked for %s", p, name)
			}
		}
		for _, q := range providers {
			if q == p {
				continue
			}
			for _, name := range secretsOf(q) {
				for server, s := range r.servers {
					for _, req := range s.requests()[before[server]:] {
						if strings.Contains(fmt.Sprint(req.Header), r.secrets[name]) || bytes.Contains(req.Body, []byte(r.secrets[name])) {
							t.Errorf("on %s the %s server's request carried %s", p, server, name)
						}
					}
				}
			}
		}
	}
	if where := r.mentions(r.secrets["OLLAMA_API_KEY"]); len(where) != 0 {
		t.Errorf("OLLAMA_API_KEY was sent: %v", where)
	}
}

// countingClient counts the requests that pass through it.
type countingClient struct {
	inner core.ProviderClient
	n     atomic.Int32
}

func (c *countingClient) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	c.n.Add(1)
	return c.inner.Stream(ctx, req)
}

// TS-29-49 (smoke, 29-PATH-5): an embedder resolves, wraps the client and passes
// the wrapper back; New reads no variable and the wrapper carries the run.
func TestSmokeResolveWrapAndPassAsClient_TS29_49(t *testing.T) {
	srv := openRouterServer(t, "")
	env := &envRecorder{vars: map[string]string{"OPENROUTER_API_KEY": "sk-or-test", "OPENROUTER_BASE_URL": srv.URL()}}
	cfg := Config{Provider: "openrouter", Model: "qwen/qwen3.8-flash", Getenv: env.Getenv}

	res, err := Resolve(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if srv.count() != 0 {
		t.Errorf("Resolve sent %d requests, want none", srv.count())
	}
	wrapper := &countingClient{inner: res.Client}
	cfg.Client = wrapper
	asked := len(env.asked())
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(env.asked()); got != asked {
		t.Errorf("New asked Getenv %d more times for %v, want none: the client was given", got-asked, env.asked()[asked:])
	}
	start, _ := streamed(t, a)
	if wrapper.n.Load() == 0 || srv.count() != 1 {
		t.Errorf("wrapper calls %d, server requests %d; want the wrapper called and one request", wrapper.n.Load(), srv.count())
	}
	if start.Provider != "openrouter" || start.API != "" || start.Model != "qwen/qwen3.8-flash" {
		t.Errorf("AgentStartEvent = %+v, want provider openrouter, model qwen/qwen3.8-flash and no API", start)
	}
}
