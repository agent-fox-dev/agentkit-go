package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// recordingClient wraps a client and records every request it is given.
type recordingClient struct {
	inner core.ProviderClient
	mu    sync.Mutex
	reqs  []core.Request
}

func (c *recordingClient) Stream(ctx context.Context, req core.Request) (<-chan core.StreamEvent, error) {
	c.mu.Lock()
	c.reqs = append(c.reqs, req)
	c.mu.Unlock()
	return c.inner.Stream(ctx, req)
}

func (c *recordingClient) requests() []core.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]core.Request(nil), c.reqs...)
}

// badToolset is a Toolset that NewRunner refuses.
var badToolset = Toolset{Tools: []core.Tool{{Name: "neither"}}}

// TS-29-19: New reports its errors in the order provider, model, effort,
// credentials, tool set, and the earliest of several problems wins.
func TestNewReportsErrorsInOrder_TS29_19(t *testing.T) {
	_, toolsErr := NewRunner(badToolset)
	if toolsErr == nil {
		t.Fatal("NewRunner accepted the bad toolset")
	}
	cases := []struct {
		name string
		cfg  Config
		is   error
		text string
	}{
		{"unknown provider", Config{Provider: "x", Model: "STANDARD"}, catalog.ErrUnknownProvider, ""},
		{"no model", Config{Provider: "openrouter"}, catalog.ErrNoModel, ""},
		{"unknown effort", Config{Provider: "ollama", Model: "STANDARD", Effort: "turbo"}, catalog.ErrUnknownEffort, ""},
		{"no OpenRouter key", Config{Provider: "openrouter", Model: "STANDARD"}, openrouter.ErrNoAPIKey, ""},
		{"no Anthropic credential", Config{Model: "STANDARD"}, anthropic.ErrNoCredentials, ""},
		{"only the toolset", Config{Provider: "ollama", Model: "STANDARD"}, nil, toolsErr.Error()},
		// Several problems at once: the earliest is the one returned.
		{"provider before model, effort and toolset", Config{Provider: "x", Effort: "turbo"}, catalog.ErrUnknownProvider, ""},
		{"model before effort and toolset", Config{Provider: "ollama", Effort: "turbo"}, catalog.ErrNoModel, ""},
		{"effort before credentials and toolset", Config{Provider: "openrouter", Model: "STANDARD", Effort: "turbo"}, catalog.ErrUnknownEffort, ""},
		{"credentials before toolset", Config{Provider: "openrouter", Model: "STANDARD"}, openrouter.ErrNoAPIKey, ""},
	}
	for _, c := range cases {
		cfg := c.cfg
		cfg.Toolset, cfg.Getenv = badToolset, noEnv
		a, err := New(cfg)
		if a != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want nil and an error", c.name, a, err)
			continue
		}
		if c.is != nil && !errors.Is(err, c.is) {
			t.Errorf("%s: error = %v, want one that wraps %v", c.name, err, c.is)
		}
		if c.text != "" && err.Error() != c.text {
			t.Errorf("%s: error = %q, want NewRunner's %q", c.name, err, c.text)
		}
	}
}

// TS-29-20: New returns the error of Resolve, or of NewRunner, as it is, with a
// nil agent, and sends no request.
func TestNewReturnsTheErrorUnchangedAndSendsNothing_TS29_20(t *testing.T) {
	anth, or, ol := anthropicServer(t), openRouterServer(t, ""), ollamaServer(t)
	env := map[string]string{
		"ANTHROPIC_API_KEY": "a", "ANTHROPIC_BASE_URL": anth.URL(),
		"OPENROUTER_BASE_URL": or.URL(), "OLLAMA_HOST": ol.URL(), // no OPENROUTER_API_KEY
	}
	cfg := Config{Provider: "openrouter", Model: "STANDARD", Env: env, Getenv: noEnv}
	a, err := New(cfg)
	_, want := Resolve(cfg)
	if a != nil || err == nil || want == nil || err.Error() != want.Error() || !errors.Is(err, openrouter.ErrNoAPIKey) {
		t.Errorf("New = %v, %v; want nil and Resolve's %v", a, err, want)
	}

	cfg = Config{Provider: "ollama", Model: "STANDARD", Env: env, Getenv: noEnv, Toolset: badToolset}
	a, err = New(cfg)
	_, want = NewRunner(badToolset)
	if a != nil || err == nil || want == nil || err.Error() != want.Error() {
		t.Errorf("New = %v, %v; want nil and NewRunner's %v", a, err, want)
	}
	for name, s := range map[string]*providerServer{"anthropic": anth, "openrouter": or, "ollama": ol} {
		if n := s.count(); n != 0 {
			t.Errorf("the %s server received %d requests, want none", name, n)
		}
	}
}

// TS-29-21: the agent calls the resolved client for every request and uses the
// resolved row for stamping, pricing and the context-window check.
func TestAgentUsesTheResolvedClientAndRow_TS29_21(t *testing.T) {
	const id = "claude-opus-5-5"
	row, listed := catalog.Lookup(id)
	if !listed || row.InputCostPerMillion == 0 {
		t.Fatalf("%s is not a priced row", id)
	}
	usage := core.Usage{InputTokens: 100_000, OutputTokens: 20_000}
	rec := &recordingClient{inner: faux.New(
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("one")}, Usage: usage},
		faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("two")}, Usage: usage},
	)}
	a, err := New(Config{Client: rec, Provider: "anthropic", Model: id})
	if err != nil {
		t.Fatal(err)
	}
	var cost float64
	for _, p := range []string{"first", "second"} {
		res, err := a.Run(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		cost = res.Usage.CostUSD
		if am := lastAssistant(res.Messages); am == nil || am.Model != id {
			t.Errorf("%s: last message stamped %v, want the resolved id %q", p, am, id)
		}
	}
	if n := len(rec.requests()); n != 2 {
		t.Errorf("the client received %d requests, want 2", n)
	}
	if want := provider.ComputeCost(&row, usage); cost != want || want == 0 {
		t.Errorf("the second run cost %v, want %v: the resolved row's rates", cost, want)
	}

	// The window is the resolved row's, not the default one: an Ollama row has
	// a 32,768-token window, and a prompt past it is refused before a request.
	small := &recordingClient{inner: faux.New()}
	b, err := New(Config{Client: small, Provider: "ollama", Model: "STANDARD"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Run(context.Background(), strings.Repeat("x", 4*40_000))
	if err == nil || !strings.Contains(err.Error(), "32768-token context window") || len(small.requests()) != 0 {
		t.Errorf("Run = %v after %d requests, want the context-window error of the 32768-token row and no request", err, len(small.requests()))
	}
}

// TS-29-22: every request carries the resolved effort, not the raw one.
func TestEveryRequestCarriesTheResolvedEffort_TS29_22(t *testing.T) {
	for _, c := range []struct {
		name string
		cfg  Config
		want Effort
	}{
		{"the tier's effort", Config{Provider: "openrouter", Model: "STANDARD"}, EffortMedium},
		{"an explicit effort, normalized", Config{Provider: "openrouter", Model: "STANDARD", Effort: "HIGH"}, EffortHigh},
		{"no effort", Config{Provider: "openrouter", Model: "an-unlisted/model"}, ""},
	} {
		rec := &recordingClient{inner: faux.New(callTurn(faux.FauxToolCall("c1", "echo", "{}")), finalTurn("done"))}
		cfg := c.cfg
		cfg.Client, cfg.Toolset = rec, Toolset{Tools: []core.Tool{echoTool("echo", nil)}}
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		reqs := rec.requests()
		if len(reqs) != 2 {
			t.Fatalf("%s: %d requests, want 2 (a tool turn and the answer)", c.name, len(reqs))
		}
		for i, r := range reqs {
			if r.Effort != c.want || r.Effort != a.Resolution().Effort {
				t.Errorf("%s: request %d carries effort %q, want %q", c.name, i, r.Effort, c.want)
			}
		}
	}
}

// TS-29-23: a tier named as the model never appears where the agent names its
// model: not in the start event, not on a message, not in the window error.
func TestATierNeverAppearsWhereTheAgentNamesItsModel_TS29_23(t *testing.T) {
	const id = "qwen/qwen3.8-flash"
	a, err := New(Config{Client: faux.New(), Provider: "openrouter", Model: "STANDARD"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Stream(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	var start *core.AgentStartEvent
	for e := range s.Events() {
		switch v := e.(type) {
		case core.AgentStartEvent:
			start = &v
		case core.MessageEndEvent:
			if v.Message.Model != id {
				t.Errorf("a message was stamped %q, want %q", v.Message.Model, id)
			}
		}
	}
	res, err := s.RunResult()
	if err != nil {
		t.Fatal(err)
	}
	if start == nil || start.Model != id {
		t.Errorf("AgentStartEvent = %+v, want model %q", start, id)
	}
	if am := lastAssistant(res.Messages); am == nil || am.Model != id {
		t.Errorf("the message in the transcript is stamped %v, want %q", am, id)
	}

	tiny, err := New(Config{Client: faux.New(), Provider: "ollama", Model: "STANDARD"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = tiny.Run(context.Background(), strings.Repeat("x", 4*40_000))
	if err == nil || strings.Contains(err.Error(), "STANDARD") || !strings.Contains(err.Error(), "qwen3.8:27b") {
		t.Errorf("window error = %v, want one that names qwen3.8:27b and not STANDARD", err)
	}
}

// requestBody decodes the JSON body of a recorded request.
func requestBody(t *testing.T, r recorded) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("request body %q: %v", r.Body, err)
	}
	return m
}

// TS-29-24: an agent that names OpenRouter and a tier sends the tier's model
// and effort to /chat/completions.
func TestOpenRouterAgentSendsTheTiersModelAndEffort_TS29_24(t *testing.T) {
	srv := openRouterServer(t, "")
	a, err := New(Config{Provider: "openrouter", Model: "STANDARD", Getenv: (&envRecorder{vars: map[string]string{
		"OPENROUTER_API_KEY": "sk-or-test", "OPENROUTER_BASE_URL": srv.URL()}}).Getenv})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].Path != "/chat/completions" {
		t.Fatalf("requests = %+v, want one POST /chat/completions", reqs)
	}
	body := requestBody(t, reqs[0])
	reasoning, _ := body["reasoning"].(map[string]any)
	if body["model"] != "qwen/qwen3.8-flash" || reasoning["effort"] != "medium" {
		t.Errorf("body model %v reasoning %v, want qwen/qwen3.8-flash and effort medium", body["model"], body["reasoning"])
	}
	r := a.Resolution()
	if r.Provider != "openrouter" || r.Tier != catalog.TierStandard || r.Model.ID != "qwen/qwen3.8-flash" || r.Effort != EffortMedium || !r.Listed {
		t.Errorf("Resolution = %+v, want openrouter STANDARD qwen/qwen3.8-flash medium listed", r)
	}
}

// TS-29-25: an agent that names Ollama and a tier sends the tier's model, think
// and num_ctx to /api/chat.
func TestOllamaAgentSendsTheTiersModelThinkAndNumCtx_TS29_25(t *testing.T) {
	srv := ollamaServer(t)
	a, err := New(Config{Provider: "ollama", Model: "STANDARD", Getenv: (&envRecorder{vars: map[string]string{
		"OLLAMA_HOST": srv.URL()}}).Getenv})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].Path != "/api/chat" {
		t.Fatalf("requests = %+v, want one POST /api/chat", reqs)
	}
	body := requestBody(t, reqs[0])
	options, _ := body["options"].(map[string]any)
	if body["model"] != "qwen3.8:27b" || body["think"] != "medium" || options["num_ctx"] != float64(32768) {
		t.Errorf("body model %v think %v options %v, want qwen3.8:27b, medium and num_ctx 32768", body["model"], body["think"], body["options"])
	}
	r := a.Resolution()
	if r.Provider != "ollama" || r.Tier != catalog.TierStandard || r.Model.ID != "qwen3.8:27b" || r.Effort != EffortMedium || !r.Listed {
		t.Errorf("Resolution = %+v, want ollama STANDARD qwen3.8:27b medium listed", r)
	}
}
