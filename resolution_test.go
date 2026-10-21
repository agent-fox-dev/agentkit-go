package agentkit

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// TS-29-26: Agent.Resolution equals what Resolve returns for the same Config,
// and with Config.Client set it holds that client.
func TestAgentResolutionEqualsResolve_TS29_26(t *testing.T) {
	for _, provider := range []string{"anthropic", "openrouter", "ollama"} {
		cfg := Config{Provider: provider, Model: "STANDARD", Env: allKeys, Getenv: noEnv}
		r, err := Resolve(cfg)
		if err != nil {
			t.Fatal(err)
		}
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got := a.Resolution()
		if !sameSelection(r, got) || r.Client == nil || got.Client == nil {
			t.Errorf("%s: Agent.Resolution = %+v, want Resolve's %+v and a client in both", provider, got, r)
		}
	}
	fp := faux.New()
	a, err := New(Config{Client: fp, Provider: "openrouter", Model: "STANDARD"})
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Resolution().Client; got != core.ProviderClient(fp) {
		t.Errorf("Resolution().Client = %v, want the given client", got)
	}
}

// rowWithEffortsAndCompat finds a catalog row that has both an effort map and
// capability bytes, so a test can write to both.
func rowWithEffortsAndCompat(t *testing.T) (provider, id string) {
	t.Helper()
	c := catalog.Default()
	for _, v := range c.Vendors() {
		for _, m := range c.Models(v) {
			sel, err := catalog.Select(v, m, "")
			if err == nil && len(sel.Model.Efforts) > 0 && len(sel.Model.Compat) > 0 {
				return v, m
			}
		}
	}
	t.Fatal("the catalog has no row with both Efforts and Compat")
	return "", ""
}

// TS-29-27: the Resolution an agent returns is a copy: writing to its row's
// effort map or capability bytes changes neither the agent's own row nor the
// next Resolution.
func TestResolutionIsACopy_TS29_27(t *testing.T) {
	provider, id := rowWithEffortsAndCompat(t)
	a, err := New(Config{Client: faux.New(), Provider: provider, Model: id})
	if err != nil {
		t.Fatal(err)
	}
	// The reference is a row of its own, from the catalog, not anything the
	// agent holds: a copy compared with the agent's row could not tell a shared
	// map from a copied one, because a write would change both.
	sel, err := catalog.Select(provider, id, "")
	if err != nil {
		t.Fatal(err)
	}
	pristine := sel.Model

	got := a.Resolution()
	for k, v := range got.Model.Efforts {
		if v != nil {
			*v = "tampered"
		}
		got.Model.Efforts[k] = nil
	}
	got.Model.Efforts["tampered"] = nil
	got.Model.Compat[0] ^= 0xff
	got.Model.ID = "tampered"

	if again := a.Resolution(); !reflect.DeepEqual(again.Model, pristine) {
		t.Errorf("Resolution() after a write = %+v, want the original %+v", again.Model, pristine)
	}
	if !reflect.DeepEqual(a.model, pristine) {
		t.Errorf("the agent's own row changed: %+v, want %+v", a.model, pristine)
	}
}

// TS-29-28: Resolution can be called while a run is in progress, from other
// goroutines, without a data race (go test -race).
func TestResolutionIsSafeDuringARun_TS29_28(t *testing.T) {
	fp := faux.New(faux.Turn{Blocks: []core.ContentBlock{faux.FauxText("done")}, Delay: 100 * time.Millisecond})
	a, err := New(Config{Client: fp, Provider: "openrouter", Model: "STANDARD"})
	if err != nil {
		t.Fatal(err)
	}
	first := a.Resolution()
	var (
		stop  atomic.Bool
		wg    sync.WaitGroup
		calls atomic.Int64
	)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				r := a.Resolution()
				calls.Add(1)
				if r.Provider != first.Provider || !reflect.DeepEqual(r.Model, first.Model) || r.Effort != first.Effort {
					t.Errorf("Resolution() = %+v during a run, want %+v", r, first)
					return
				}
			}
		}()
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Error(err)
	}
	stop.Store(true)
	wg.Wait()
	if calls.Load() == 0 {
		t.Error("Resolution was never called during the run")
	}
}

// TS-29-29: *Agent exports exactly seven methods.
func TestAgentExportsExactlySevenMethods_TS29_29(t *testing.T) {
	typ := reflect.TypeOf(&Agent{})
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	slices.Sort(got)
	want := []string{"Messages", "ReachableTools", "Resolution", "Run", "Runner", "Stream", "Usage"}
	if !slices.Equal(got, want) {
		t.Errorf("*Agent exports %v, want %v", got, want)
	}
}

// startEvent runs one prompt through Stream and returns the AgentStartEvent.
func startEvent(t *testing.T, a *Agent) core.AgentStartEvent {
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
	if start == nil {
		t.Fatal("the run pushed no AgentStartEvent")
	}
	return *start
}

// TS-29-30: a run on a client the agent built starts with an AgentStartEvent
// that names the provider, the API of its package and the model id.
func TestAgentStartEventNamesTheProviderAPIAndModel_TS29_30(t *testing.T) {
	anth, or, ol := anthropicServer(t), openRouterServer(t, ""), ollamaServer(t)
	env := map[string]string{
		"ANTHROPIC_API_KEY": "a", "ANTHROPIC_BASE_URL": anth.URL(),
		"OPENROUTER_API_KEY": "o", "OPENROUTER_BASE_URL": or.URL(), "OLLAMA_HOST": ol.URL(),
	}
	// The names are written as an embedder might: the event holds the canonical one.
	for _, c := range []struct {
		named, provider string
		api             core.API
	}{{"", "anthropic", anthropic.API}, {" OpenRouter", "openrouter", openrouter.API}, {"OLLAMA", "ollama", ollama.API}} {
		a, err := New(Config{Provider: c.named, Model: "STANDARD", Env: env, Getenv: noEnv})
		if err != nil {
			t.Fatal(err)
		}
		ev := startEvent(t, a)
		if ev.Provider != c.provider || ev.API != c.api || ev.API == "" || ev.Model != a.Resolution().Model.ID || ev.Model == "STANDARD" {
			t.Errorf("%s: AgentStartEvent = %+v, want provider %q, API %q and model %q", c.provider, ev, c.provider, c.api, a.Resolution().Model.ID)
		}
	}
}

// TS-29-31: with Config.Client given the event still names the provider and the
// model, and no API.
func TestAgentStartEventHasNoAPIForAGivenClient_TS29_31(t *testing.T) {
	a, err := New(Config{Client: faux.New(), Provider: "openrouter", Model: "qwen/qwen3.8-flash"})
	if err != nil {
		t.Fatal(err)
	}
	if ev := startEvent(t, a); ev.API != "" || ev.Provider != "openrouter" || ev.Model != "qwen/qwen3.8-flash" {
		t.Errorf("AgentStartEvent = %+v, want provider openrouter, model qwen/qwen3.8-flash and no API", ev)
	}
}
