package agentkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// noEnv is a Getenv that knows nothing, so the process environment is never
// read.
func noEnv(string) string { return "" }

var keys = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "OPENROUTER_API_KEY": "sk-or-test"}

// envRecorder is a hermetic Getenv that remembers every name it is asked for.
type envRecorder struct {
	mu    sync.Mutex
	vars  map[string]string
	names []string
}

func (e *envRecorder) Getenv(name string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.names = append(e.names, name)
	return e.vars[name]
}

func (e *envRecorder) asked() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.names...)
}

func TestResolve_ChoosesTheCatalogRowForTheProviderModelAndEffort(t *testing.T) {
	for _, c := range []struct {
		provider, model string
		effort          Effort
		want            string
	}{
		{"", "STANDARD", "", "anthropic"},
		{"ANTHROPIC", "claude-3-5-sonnet", "", "anthropic"},
		{" OpenRouter ", "STANDARD", "", "openrouter"},
		{"openrouter", "qwen/qwen3.8-flash", "HIGH", "openrouter"},
		{"Ollama", "STANDARD", "low", "ollama"},
	} {
		r, err := Resolve(Config{Provider: c.provider, Model: c.model, Effort: c.effort, Env: keys, Getenv: noEnv})
		sel, serr := catalog.Select(c.provider, c.model, c.effort)
		if err != nil || serr != nil {
			t.Fatalf("%q %q: Resolve %v, Select %v", c.provider, c.model, err, serr)
		}
		if r.Provider != c.want || r.Provider != sel.Provider || r.Tier != sel.Tier || !reflect.DeepEqual(r.Model, sel.Model) ||
			r.Effort != sel.Effort || r.Listed != sel.Listed || r.Client == nil {
			t.Errorf("%q %q: Resolve = %+v, want catalog.Select's %+v and a client", c.provider, c.model, r, sel)
		}
	}
}

func TestResolve_ReturnsTheCatalogsErrorsWithOrWithoutAClient(t *testing.T) {
	for _, c := range []struct {
		cfg  Config
		want error
	}{
		{Config{Provider: "gemini", Model: "STANDARD"}, catalog.ErrUnknownProvider},
		{Config{Provider: "openrouter"}, catalog.ErrNoModel},
		{Config{Provider: "ollama", Model: "STANDARD", Effort: "turbo"}, catalog.ErrUnknownEffort},
	} {
		for _, client := range []core.ProviderClient{nil, faux.New()} {
			env := &envRecorder{vars: keys}
			cfg := c.cfg
			cfg.Client, cfg.Getenv = client, env.Getenv
			r, err := Resolve(cfg)
			if !errors.Is(err, c.want) || r.Client != nil || r.Provider != "" || len(env.asked()) != 0 {
				t.Errorf("%+v: Resolve = %+v, %v (asked %v); want the zero value, %v and nothing read", c.cfg, r, err, env.asked(), c.want)
			}
		}
	}
}

func TestResolve_ReturnsTheProviderConstructorsErrorUnchanged(t *testing.T) {
	row := func(p string) core.Model { s, _ := catalog.Select(p, "STANDARD", ""); return s.Model }
	vertex := map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}
	badURL := map[string]string{"OPENROUTER_API_KEY": "k", "OPENROUTER_BASE_URL": "not a url"}
	badHost := map[string]string{"OLLAMA_HOST": "ftp://example.com"}
	anthropicErr := func(env map[string]string) error { _, _, err := anthropic.Resolve(anthropic.MapEnv(env)); return err }
	openrouterErr := func(env map[string]string) error {
		_, err := openrouter.New(row("openrouter"), openrouter.Options{Env: env, Getenv: noEnv})
		return err
	}
	_, ollamaErr := ollama.New(row("ollama"), ollama.Options{Env: badHost, Getenv: noEnv})
	for _, c := range []struct {
		provider string
		env      map[string]string
		want, is error
	}{
		{"anthropic", nil, anthropicErr(nil), anthropic.ErrNoCredentials},
		{"anthropic", vertex, anthropicErr(vertex), nil},
		{"openrouter", nil, openrouterErr(nil), openrouter.ErrNoAPIKey},
		{"openrouter", badURL, openrouterErr(badURL), nil},
		{"ollama", badHost, ollamaErr, ollama.ErrInvalidHost},
	} {
		r, err := Resolve(Config{Provider: c.provider, Model: "STANDARD", Env: c.env, Getenv: noEnv})
		if err == nil || c.want == nil || err.Error() != c.want.Error() || (c.is != nil && !errors.Is(err, c.is)) || r.Client != nil {
			t.Errorf("%s %v: Resolve = %+v, %v; want the constructor's %v", c.provider, c.env, r, err, c.want)
		}
	}
}

func TestResolve_ReturnsAGivenClientAndReadsNoVariable(t *testing.T) {
	fp := faux.New()
	env := &envRecorder{vars: keys}
	r, err := Resolve(Config{Client: fp, Provider: "openrouter", Model: "STANDARD", Effort: "LOW", Getenv: env.Getenv})
	if err != nil || r.Client != core.ProviderClient(fp) || len(env.asked()) != 0 {
		t.Fatalf("Resolve = %+v, %v (asked %v); want the given client and nothing read", r, err, env.asked())
	}
	if r.Model.ID != "qwen/qwen3.8-flash" || r.Effort != EffortLow {
		t.Errorf("model %q effort %q, want the provider's row for the tier, at low", r.Model.ID, r.Effort)
	}
}

func TestLookupEnv_ReadsEnvThenGetenvThenTheProcess(t *testing.T) {
	t.Setenv("AGENTKIT_TEST_VAR", "proc")
	getenv := func(string) string { return "get" }
	for _, c := range []struct {
		name   string
		e      lookupEnv
		want   string
		wantOK bool
	}{
		{"a non-empty Env value wins", lookupEnv{set: map[string]string{"AGENTKIT_TEST_VAR": "env"}, getenv: getenv}, "env", true},
		{"an empty Env value falls through to Getenv", lookupEnv{set: map[string]string{"AGENTKIT_TEST_VAR": ""}, getenv: getenv}, "get", true},
		{"a Getenv hides the process", lookupEnv{getenv: noEnv}, "", false},
		{"no Getenv reads the process", lookupEnv{}, "proc", true},
	} {
		if v, ok := c.e.Lookup("AGENTKIT_TEST_VAR"); v != c.want || ok != c.wantOK {
			t.Errorf("%s: Lookup = %q, %v; want %q, %v", c.name, v, ok, c.want, c.wantOK)
		}
	}
}

func TestNew_ReturnsTheFirstErrorInOrder(t *testing.T) {
	bad := Toolset{Tools: []core.Tool{{Name: "neither"}}}
	_, toolsErr := NewRunner(bad)
	for _, c := range []struct {
		name string
		cfg  Config
		is   error
	}{
		{"the provider first", Config{Provider: "x", Effort: "turbo"}, catalog.ErrUnknownProvider},
		{"then the model", Config{Provider: "ollama", Effort: "turbo"}, catalog.ErrNoModel},
		{"then the effort", Config{Provider: "openrouter", Model: "STANDARD", Effort: "turbo"}, catalog.ErrUnknownEffort},
		{"then the credentials", Config{Provider: "openrouter", Model: "STANDARD"}, openrouter.ErrNoAPIKey},
		{"then the Anthropic credentials", Config{Model: "STANDARD"}, anthropic.ErrNoCredentials},
		{"then the toolset", Config{Provider: "ollama", Model: "STANDARD"}, nil},
	} {
		cfg := c.cfg
		cfg.Toolset, cfg.Getenv = bad, noEnv
		a, err := New(cfg)
		if a != nil || err == nil || (c.is != nil && !errors.Is(err, c.is)) || (c.is == nil && err.Error() != toolsErr.Error()) {
			t.Errorf("%s: New = %v, %v; want nil and %v", c.name, a, err, c.is)
		}
	}
}

// providerServer stands in for one provider's API: it records each request
// and answers with one text turn in that provider's wire format.
type providerServer struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	header []http.Header
	bodies [][]byte
}

func newServer(t *testing.T, reply func(w http.ResponseWriter, model string)) *providerServer {
	s := &providerServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.paths, s.header, s.bodies = append(s.paths, r.URL.Path), append(s.header, r.Header.Clone()), append(s.bodies, b)
		s.mu.Unlock()
		model, _ := testutil.Object(t, b)["model"].(string)
		reply(w, model)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *providerServer) received() ([]string, []http.Header, [][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths, s.header, s.bodies
}

var providerReplies = map[string]func(w http.ResponseWriter, model string){
	"anthropic": func(w http.ResponseWriter, model string) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range [][2]string{
			{"message_start", `{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"` + model + `","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`},
			{"message_stop", `{"type":"message_stop"}`},
		} {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e[0], e[1])
		}
	},
	"openrouter": func(w http.ResponseWriter, model string) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := `data: {"id":"g","object":"chat.completion.chunk","created":1,"model":"` + model + `","choices":`
		fmt.Fprint(w, chunk+`[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}]}`+"\n\n"+
			chunk+`[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	},
	"ollama": func(w http.ResponseWriter, model string) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"model":%q,"message":{"role":"assistant","content":"ok"},"done":false}`+"\n", model)
		fmt.Fprintf(w, `{"model":%q,"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":10,"eval_count":5}`+"\n", model)
	},
}

// ownPrefixes are the variables each provider may read.
var ownPrefixes = map[string][]string{
	"anthropic":  {"ANTHROPIC_", "CLAUDE_CODE_", "GOOGLE_CLOUD_", "CLOUD_ML_REGION", "CLOUDSDK_", "AWS_"},
	"openrouter": {"OPENROUTER_"},
	"ollama":     {"OLLAMA_"},
}

// TestNew_RunsOnTheChosenProviderAlone builds an agent by provider name over
// three live servers: only the chosen one is reached, and only after New.
func TestNew_RunsOnTheChosenProviderAlone(t *testing.T) {
	paths := map[string]string{"anthropic": "/v1/messages", "openrouter": "/chat/completions", "ollama": "/api/chat"}
	apis := map[string]core.API{"anthropic": anthropic.API, "openrouter": openrouter.API, "ollama": ollama.API}
	secrets := map[string]string{"anthropic": "sk-ant-secret-1", "openrouter": "sk-or-secret-2", "ollama": "ollama-secret-3"}
	for _, p := range []string{"anthropic", "openrouter", "ollama"} {
		servers := map[string]*providerServer{}
		for name, reply := range providerReplies {
			servers[name] = newServer(t, reply)
		}
		env := &envRecorder{vars: map[string]string{
			"ANTHROPIC_BASE_URL": servers["anthropic"].URL, "OPENROUTER_BASE_URL": servers["openrouter"].URL,
			"OLLAMA_HOST": servers["ollama"].URL, "ANTHROPIC_API_KEY": secrets["anthropic"],
			"OPENROUTER_API_KEY": secrets["openrouter"], "OLLAMA_API_KEY": secrets["ollama"],
		}}
		cfg := Config{Provider: p, Model: "STANDARD", Getenv: env.Getenv}
		if _, err := Resolve(cfg); err != nil {
			t.Fatal(err)
		}
		a := mustNew(t, cfg)
		id := a.Resolution().Model.ID
		for name, s := range servers {
			if got, _, _ := s.received(); len(got) != 0 {
				t.Errorf("%s: Resolve and New sent %d requests to %s, want none", p, len(got), name)
			}
		}
		evs, res, err := drain(t, context.Background(), a, "hi")
		if err != nil || res.FinalText() != "ok" {
			t.Fatalf("%s: Run = %q, %v", p, res.FinalText(), err)
		}
		if start := evs[0].(core.AgentStartEvent); start != (core.AgentStartEvent{Provider: p, API: apis[p], Model: id}) {
			t.Errorf("%s: AgentStartEvent = %+v", p, start)
		}
		for name, s := range servers {
			got, headers, bodies := s.received()
			if name != p {
				if len(got) != 0 {
					t.Errorf("%s: the %s server received %d requests", p, name, len(got))
				}
				continue
			}
			if len(got) != 1 || got[0] != paths[p] || testutil.Object(t, bodies[0])["model"] != id {
				t.Fatalf("%s: requests to %v, want one to %s naming %s", p, got, paths[p], id)
			}
			for other, secret := range secrets {
				if other != p && (strings.Contains(fmt.Sprint(headers[0]), secret) || bytes.Contains(bodies[0], []byte(secret))) {
					t.Errorf("%s: the request carried %s's secret", p, other)
				}
			}
		}
		for _, name := range env.asked() {
			if !hasAnyPrefix(name, ownPrefixes[p]) {
				t.Errorf("%s: Getenv was asked for %s", p, name)
			}
		}
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
