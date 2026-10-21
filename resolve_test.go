package agentkit

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// envRecorder is a hermetic Getenv: it answers from vars, never from the
// process environment, and remembers every name it was asked for.
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

// asked is the names Getenv was asked for, in order.
func (e *envRecorder) asked() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.names...)
}

// noEnv is a Getenv that knows nothing: with it set, the process environment is
// never read.
func noEnv(string) string { return "" }

// allKeys are the credentials of every provider, for tests that resolve one of
// them and must not depend on the machine's own environment.
var allKeys = map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "OPENROUTER_API_KEY": "sk-or-test"}

// TS-29-1: Config declares the fourteen fields in order, with their types.
func TestConfigDeclaresFourteenFields_TS29_1(t *testing.T) {
	want := []struct{ name, typ string }{
		{"Client", "core.ProviderClient"}, {"Provider", "string"}, {"Model", "string"},
		{"Effort", "core.Effort"}, {"Env", "map[string]string"}, {"Getenv", "func(string) string"},
		{"System", "string"}, {"Prefix", "[]core.Message"}, {"Toolset", "agentkit.Toolset"},
		{"MaxTurns", "int"}, {"MaxCostUSD", "float64"}, {"Timeout", "time.Duration"},
		{"Prune", "agentkit.PruneOptions"}, {"MaxTokens", "int"},
	}
	typ := reflect.TypeOf(Config{})
	if typ.NumField() != len(want) {
		t.Fatalf("Config has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, w := range want {
		if f := typ.Field(i); f.Name != w.name || f.Type.String() != w.typ {
			t.Errorf("field %d = %s %s, want %s %s", i, f.Name, f.Type, w.name, w.typ)
		}
	}
}

// TS-29-2: a provider name is trimmed and matched without regard to case, and an
// empty one is anthropic.
func TestResolveNormalizesTheProviderName_TS29_2(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{" OpenRouter ", "openrouter"}, {"ANTHROPIC", "anthropic"}, {"", "anthropic"}, {"Ollama", "ollama"},
	} {
		r, err := Resolve(Config{Provider: c.in, Model: "STANDARD", Env: allKeys, Getenv: noEnv})
		if err != nil || r.Provider != c.want {
			t.Errorf("Resolve(Provider %q) = %q, %v; want %q", c.in, r.Provider, err, c.want)
		}
	}
}

// TS-29-4: the root package's non-test code imports no anthropic-sdk-go
// package.
func TestRootImportsNoAnthropicSDK_TS29_4(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	lines := strings.Fields(string(out))
	for _, p := range lines {
		if strings.HasPrefix(p, "github.com/anthropics/anthropic-sdk-go") {
			t.Errorf("the root package imports %s", p)
		}
	}
	if len(lines) == 0 {
		t.Error("go list printed no imports")
	}
}

// sameSelection reports whether a and b resolved to the same provider, tier, row,
// effort and listing.
func sameSelection(a, b Resolution) bool {
	return a.Provider == b.Provider && a.Tier == b.Tier && reflect.DeepEqual(a.Model, b.Model) &&
		a.Effort == b.Effort && a.Listed == b.Listed
}

// TS-29-5: Resolve returns the catalog's selection and a client, for a tier and
// for a model id.
func TestResolveReturnsTheSelectionAndAClient_TS29_5(t *testing.T) {
	for _, c := range []struct {
		model  string
		effort Effort
		tier   catalog.Tier
		id     string
		want   Effort
	}{
		{"STANDARD", "", catalog.TierStandard, "qwen/qwen3.8-flash", EffortMedium},
		{"qwen/qwen3.8-flash", "HIGH", "", "qwen/qwen3.8-flash", EffortHigh},
	} {
		cfg := Config{Provider: "openrouter", Model: c.model, Effort: c.effort, Env: allKeys, Getenv: noEnv}
		r, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", c.model, err)
		}
		sel, err := catalog.Select("openrouter", c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		if r.Provider != sel.Provider || r.Tier != sel.Tier || !reflect.DeepEqual(r.Model, sel.Model) ||
			r.Effort != sel.Effort || r.Listed != sel.Listed || r.Client == nil {
			t.Errorf("Resolve(%q) = %+v, want catalog.Select's %+v and a client", c.model, r, sel)
		}
		if r.Tier != c.tier || r.Model.ID != c.id || r.Effort != c.want || !r.Listed {
			t.Errorf("Resolve(%q) = tier %q id %q effort %q listed %v; want %q %q %q true",
				c.model, r.Tier, r.Model.ID, r.Effort, r.Listed, c.tier, c.id, c.want)
		}
	}
}

// TS-29-6: Resolve returns the catalog's errors unchanged, with or without
// Config.Client, builds nothing and reads no variable.
func TestResolveReturnsCatalogErrorsUnchanged_TS29_6(t *testing.T) {
	cases := []struct {
		name   string
		cfg    Config
		want   error
		in     string
		choice string
	}{
		{"unknown provider", Config{Provider: "gemini", Model: "STANDARD"}, catalog.ErrUnknownProvider, "gemini", "openrouter"},
		{"no model", Config{Provider: "openrouter"}, catalog.ErrNoModel, "", "STANDARD"},
		{"unknown effort", Config{Provider: "ollama", Model: "STANDARD", Effort: "turbo"}, catalog.ErrUnknownEffort, "turbo", "xhigh"},
	}
	for _, c := range cases {
		for _, client := range []core.ProviderClient{nil, faux.New()} {
			env := &envRecorder{vars: allKeys}
			cfg := c.cfg
			cfg.Client, cfg.Getenv, cfg.Env = client, env.Getenv, allKeys
			r, err := Resolve(cfg)
			label := c.name + " (client set: " + map[bool]string{true: "yes", false: "no"}[client != nil] + ")"
			if !errors.Is(err, c.want) {
				t.Errorf("%s: error = %v, want one that wraps %v", label, err, c.want)
				continue
			}
			if r.Client != nil || r.Provider != "" || !reflect.DeepEqual(r.Model, Resolution{}.Model) {
				t.Errorf("%s: Resolution = %+v, want the zero value", label, r)
			}
			if !strings.Contains(err.Error(), c.in) || !strings.Contains(err.Error(), c.choice) {
				t.Errorf("%s: error %q should name %q and the choices (%q)", label, err, c.in, c.choice)
			}
			if got := env.asked(); len(got) != 0 {
				t.Errorf("%s: Getenv was asked for %v, want nothing", label, got)
			}
		}
	}
}

// TS-29-7 (property): the selection fields of a Resolution depend only on
// Client, Provider, Model, Effort, Env and Getenv.
func TestResolutionSelectionIgnoresTheOtherFields_TS29_7(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	pick := func(xs ...string) string { return xs[r.Intn(len(xs))] }
	ids := map[string][]string{"": nil, "anthropic": nil, "openrouter": nil, "ollama": nil}
	for _, v := range catalog.Default().Vendors() {
		if _, ok := ids[v]; ok {
			ids[v] = catalog.Default().Models(v)
		}
	}
	ids[""] = ids["anthropic"]
	for range 300 {
		provider := pick("", "anthropic", " OpenRouter", "OLLAMA ")
		key := strings.ToLower(strings.TrimSpace(provider))
		model := pick("STANDARD", "simple", " Advanced ", "an-unlisted-id", "x/y:z")
		if known := ids[key]; len(known) > 0 && r.Intn(2) == 0 {
			model = known[r.Intn(len(known))]
		}
		base := Config{Provider: provider, Model: model, Effort: Effort(pick("", "low", "MEDIUM", "high", "xhigh", "max")),
			Env: allKeys, Getenv: noEnv}
		noisy := base
		noisy.System = pick("", "be brief")
		noisy.Prefix = []core.Message{userText("hi")}
		noisy.Toolset = Toolset{Tools: []core.Tool{{Name: "neither"}}} // Resolve does not validate it
		noisy.MaxTurns, noisy.MaxCostUSD, noisy.Timeout = r.Intn(9), float64(r.Intn(9)), time.Duration(r.Intn(9))
		noisy.Prune, noisy.MaxTokens = PruneOptions{Threshold: 0.5, KeepTurns: 1}, r.Intn(9)

		a, errA := Resolve(base)
		b, errB := Resolve(noisy)
		if errA != nil || errB != nil {
			t.Fatalf("Resolve(%+v) = %v, with noise %v", base, errA, errB)
		}
		if !sameSelection(a, b) {
			t.Fatalf("Resolve changed with the other fields: %+v vs %+v", a, b)
		}
		if c, _ := Resolve(base); !sameSelection(a, c) {
			t.Fatalf("Resolve is not repeatable: %+v vs %+v", a, c)
		}
	}
}

// TS-29-40: an unlisted Claude id resolves to the row catalog.Lookup returns,
// unlisted.
func TestAnUnlistedClaudeIDKeepsLookupsRow_TS29_40(t *testing.T) {
	const id = "claude-3-5-sonnet"
	row, listed := catalog.Lookup(id)
	if listed {
		t.Fatalf("%s is listed; pick an id the catalog does not list", id)
	}
	for _, provider := range []string{"", "anthropic"} {
		r, err := Resolve(Config{Provider: provider, Model: id, Env: allKeys, Getenv: noEnv})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r.Model, row) || r.Listed {
			t.Errorf("Provider %q: row %+v listed %v, want catalog.Lookup's %+v unlisted", provider, r.Model, r.Listed, row)
		}
	}
}

// TS-29-41: a test double passed as Config.Client is called for every request,
// and no credential variable is read.
func TestAClientIsCalledForEveryRequest_TS29_41(t *testing.T) {
	fp := faux.New(faux.ToolCall("c1", "echo", `{}`), faux.Text("done"))
	var ran atomic.Int32
	env := &envRecorder{vars: allKeys}
	a, err := New(Config{Client: fp, Model: testModelID, Getenv: env.Getenv,
		Toolset: Toolset{Tools: []core.Tool{echoTool("echo", &ran)}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if fp.Calls() != 2 || ran.Load() != 1 || res.FinalText() != "done" || res.Usage.Unpriced {
		t.Errorf("client calls %d, tool runs %d, text %q, unpriced %v; want 2, 1, done, false",
			fp.Calls(), ran.Load(), res.FinalText(), res.Usage.Unpriced)
	}
	if got := env.asked(); len(got) != 0 {
		t.Errorf("Getenv was asked for %v, want nothing: a given client needs no credential", got)
	}
}

// TS-29-42: the three examples that used to resolve an SDK client fail from
// agentkit.New, before any request, naming ANTHROPIC_API_KEY, and import no SDK
// package.
func TestExamplesFailFromNewWithoutACredential_TS29_42(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	examples := []string{"codingagent", "customtools", "mcp"}
	bin := t.TempDir()
	args := []string{"build", "-o", bin}
	for _, e := range examples {
		args = append(args, "./examples/"+e)
	}
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for _, e := range examples {
		src, err := filepath.Glob(filepath.Join("examples", e, "*.go"))
		if err != nil || len(src) == 0 {
			t.Fatalf("no sources for %s: %v", e, err)
		}
		for _, f := range src {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(b, []byte("anthropics/anthropic-sdk-go")) {
				t.Errorf("%s imports the Anthropic SDK", f)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		cmd := exec.CommandContext(ctx, filepath.Join(bin, e))
		cmd.Dir = t.TempDir()
		// Only what a program needs to start: no ANTHROPIC_*, no cloud selection.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + cmd.Dir}
		out, err := cmd.CombinedOutput()
		cancel()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() == 0 {
			t.Errorf("%s exited with %v, want a non-zero exit\n%s", e, err, out)
		}
		if !strings.Contains(string(out), "ANTHROPIC_API_KEY") {
			t.Errorf("%s printed %q, want a message naming ANTHROPIC_API_KEY", e, out)
		}
	}
}

// ------------------------------------------------------------ client building

// authOf is the Authorization header of the last request a server got.
func authOf(t *testing.T, s *providerServer) string {
	t.Helper()
	reqs := s.requests()
	if len(reqs) == 0 {
		t.Fatal("the server received no request")
	}
	return reqs[len(reqs)-1].Header.Get("Authorization")
}

// TS-29-3: a variable is read from a non-empty Env value first, then from
// Getenv, and from the process environment only when there is no Getenv; an
// empty value is unset at every step.
func TestVariablesAreReadEnvThenGetenvThenProcess_TS29_3(t *testing.T) {
	srv := openRouterServer(t, "")
	t.Setenv("OPENROUTER_API_KEY", "proc")
	t.Setenv("OPENROUTER_BASE_URL", srv.URL())
	base := map[string]string{"OPENROUTER_BASE_URL": srv.URL()}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	run := func(cfg Config) string {
		t.Helper()
		cfg.Provider, cfg.Model = "openrouter", "STANDARD"
		r, err := Resolve(cfg)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		streamOnce(t, r.Client)
		return authOf(t, srv)
	}

	// An empty Env value falls through to Getenv.
	env := &envRecorder{vars: map[string]string{"OPENROUTER_API_KEY": "k"}}
	if got := run(Config{Env: with(map[string]string{"OPENROUTER_API_KEY": ""}), Getenv: env.Getenv}); got != "Bearer k" {
		t.Errorf("an empty Env value: Authorization = %q, want Bearer k from Getenv", got)
	}
	// A non-empty Env value wins, and Getenv is not asked for the key.
	env = &envRecorder{vars: map[string]string{"OPENROUTER_API_KEY": "k"}}
	if got := run(Config{Env: with(map[string]string{"OPENROUTER_API_KEY": "e"}), Getenv: env.Getenv}); got != "Bearer e" {
		t.Errorf("a non-empty Env value: Authorization = %q, want Bearer e", got)
	}
	if slices.Contains(env.asked(), "OPENROUTER_API_KEY") {
		t.Errorf("Getenv was asked for the key %v although Env held one", env.asked())
	}
	// With a Getenv, the process environment is never read, even when Getenv
	// has nothing.
	_, err := Resolve(Config{Provider: "openrouter", Model: "STANDARD", Env: base, Getenv: noEnv})
	if !errors.Is(err, openrouter.ErrNoAPIKey) {
		t.Errorf("Getenv set and empty: error = %v, want openrouter.ErrNoAPIKey although the process holds a key", err)
	}
	// With no Getenv, the process environment is read.
	if got := run(Config{}); got != "Bearer proc" {
		t.Errorf("no Env and no Getenv: Authorization = %q, want Bearer proc from the process", got)
	}
}

// TS-29-3, for the Anthropic provider, whose variables Resolve checks through an
// adapter of its own (lookupEnv): the same order, with an empty value unset at
// every step.
func TestAnthropicLookupOrderIsTheSame_TS29_3(t *testing.T) {
	// A machine that selects a cloud deployment or holds another credential must
	// not change what these cases see.
	for _, name := range []string{"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_BEDROCK", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN"} {
		t.Setenv(name, "")
	}
	t.Setenv("ANTHROPIC_API_KEY", "proc")
	resolves := func(cfg Config) error {
		cfg.Provider, cfg.Model = "anthropic", "STANDARD"
		_, err := Resolve(cfg)
		return err
	}
	key := map[string]string{"ANTHROPIC_API_KEY": "k"}
	get := func(vars map[string]string) func(string) string {
		return (&envRecorder{vars: vars}).Getenv
	}

	if err := resolves(Config{Env: map[string]string{"ANTHROPIC_API_KEY": "e"}, Getenv: noEnv}); err != nil {
		t.Errorf("a non-empty Env value: %v, want the credential found", err)
	}
	if err := resolves(Config{Env: map[string]string{"ANTHROPIC_API_KEY": ""}, Getenv: get(key)}); err != nil {
		t.Errorf("an empty Env value over a Getenv that has the key: %v, want it to fall through", err)
	}
	if err := resolves(Config{Getenv: get(key)}); err != nil {
		t.Errorf("a key from Getenv: %v, want the credential found", err)
	}
	if err := resolves(Config{Getenv: noEnv}); !errors.Is(err, anthropic.ErrNoCredentials) {
		t.Errorf("Getenv set and empty: %v, want anthropic.ErrNoCredentials although the process holds a key", err)
	}
	if err := resolves(Config{Env: map[string]string{"ANTHROPIC_API_KEY": ""}, Getenv: noEnv}); !errors.Is(err, anthropic.ErrNoCredentials) {
		t.Errorf("an empty Env value and an empty Getenv: %v, want anthropic.ErrNoCredentials", err)
	}
	if err := resolves(Config{}); err != nil {
		t.Errorf("no Env and no Getenv: %v, want the process environment's key found", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if err := resolves(Config{}); !errors.Is(err, anthropic.ErrNoCredentials) {
		t.Errorf("an empty value in the process environment: %v, want anthropic.ErrNoCredentials", err)
	}
}

// distinct reports whether a and b are different clients: two pointers that are
// not the same one.
func distinct(a, b core.ProviderClient) bool {
	if reflect.ValueOf(a).Kind() == reflect.Pointer && reflect.ValueOf(b).Kind() == reflect.Pointer {
		return a != b
	}
	return !reflect.DeepEqual(a, b)
}

// TS-29-8: Resolve sends no request and keeps nothing between calls: each call
// builds a new client, from the variables it was given.
func TestResolveSendsNothingAndKeepsNoState_TS29_8(t *testing.T) {
	anth, or, ol := anthropicServer(t), openRouterServer(t, ""), ollamaServer(t)
	env := map[string]string{
		"ANTHROPIC_API_KEY": "a", "ANTHROPIC_BASE_URL": anth.URL(),
		"OPENROUTER_API_KEY": "o", "OPENROUTER_BASE_URL": or.URL(), "OLLAMA_HOST": ol.URL(),
	}
	for _, p := range []string{"anthropic", "openrouter", "ollama"} {
		cfg := Config{Provider: p, Model: "STANDARD", Env: env, Getenv: noEnv}
		var clients []core.ProviderClient
		for range 3 {
			r, err := Resolve(cfg)
			if err != nil || r.Client == nil {
				t.Fatalf("Resolve(%s) = %v, %v", p, r.Client, err)
			}
			clients = append(clients, r.Client)
		}
		for i := range clients {
			for j := i + 1; j < len(clients); j++ {
				if !distinct(clients[i], clients[j]) {
					t.Errorf("%s: Resolve calls %d and %d returned the same client", p, i, j)
				}
			}
		}
	}
	for name, s := range map[string]*providerServer{"anthropic": anth, "openrouter": or, "ollama": ol} {
		if n := s.count(); n != 0 {
			t.Errorf("the %s server received %d requests from Resolve, want none", name, n)
		}
	}

	// A second call sees only the variables it is given, not the first call's.
	other := openRouterServer(t, "")
	if _, err := Resolve(Config{Provider: "openrouter", Model: "STANDARD", Getenv: noEnv,
		Env: map[string]string{"OPENROUTER_API_KEY": "o", "OPENROUTER_BASE_URL": or.URL()}}); err != nil {
		t.Fatal(err)
	}
	second, err := Resolve(Config{Provider: "openrouter", Model: "STANDARD", Getenv: noEnv,
		Env: map[string]string{"OPENROUTER_API_KEY": "o", "OPENROUTER_BASE_URL": other.URL()}})
	if err != nil {
		t.Fatal(err)
	}
	streamOnce(t, second.Client)
	if other.count() != 1 || or.count() != 0 {
		t.Errorf("the second client sent %d requests to its server and %d to the first's, want 1 and 0", other.count(), or.count())
	}
}

// TS-29-9: the Anthropic provider is built from Env and Getenv, not from a
// fixed SDK client: it reads its variables again on every request.
func TestAnthropicResolvesPerRequestFromEnv_TS29_9(t *testing.T) {
	srv := anthropicServer(t)
	env := &envRecorder{vars: map[string]string{"ANTHROPIC_API_KEY": "k1", "ANTHROPIC_BASE_URL": srv.URL()}}
	r, err := Resolve(Config{Provider: "anthropic", Model: "claude-opus-5-5", Getenv: env.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	streamOnce(t, r.Client)
	env.mu.Lock()
	env.vars["ANTHROPIC_API_KEY"] = "k2"
	env.mu.Unlock()
	streamOnce(t, r.Client)
	reqs := srv.requests()
	if len(reqs) != 2 || reqs[0].Header.Get("X-Api-Key") != "k1" || reqs[1].Header.Get("X-Api-Key") != "k2" {
		var keys []string
		for _, q := range reqs {
			keys = append(keys, q.Header.Get("X-Api-Key"))
		}
		t.Errorf("the requests carried keys %v, want [k1 k2]: the key is read again on each request", keys)
	}
}

// TS-29-10: a missing Anthropic credential, and a Vertex selection without a
// project, are errors of Resolve: the error anthropic.Resolve returns over the
// same lookup.
func TestAnthropicCredentialFailuresSurfaceFromResolve_TS29_10(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want func(error) bool
		need []string
	}{
		{"no credential", map[string]string{}, func(err error) bool { return errors.Is(err, anthropic.ErrNoCredentials) }, nil},
		{"Vertex without a project", map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}, func(error) bool { return true },
			[]string{"ANTHROPIC_VERTEX_PROJECT_ID", "GOOGLE_CLOUD_PROJECT"}},
	}
	for _, c := range cases {
		env := &envRecorder{vars: c.vars}
		r, err := Resolve(Config{Provider: "anthropic", Model: "STANDARD", Getenv: env.Getenv})
		_, _, want := anthropic.Resolve(anthropic.MapEnv(c.vars))
		if err == nil || want == nil || err.Error() != want.Error() || !c.want(err) {
			t.Errorf("%s: Resolve error = %v, want what anthropic.Resolve returns: %v", c.name, err, want)
		}
		for _, name := range c.need {
			if err != nil && !strings.Contains(err.Error(), name) {
				t.Errorf("%s: error %q does not name %s", c.name, err, name)
			}
		}
		if r.Client != nil || r.Provider != "" {
			t.Errorf("%s: Resolution = %+v, want the zero value", c.name, r)
		}
	}
}

// TS-29-11: the OpenRouter client is built from Env and Getenv and sends to the
// base URL they name.
func TestOpenRouterClientIsBuiltFromEnvAndGetenv_TS29_11(t *testing.T) {
	srv := openRouterServer(t, "")
	env := &envRecorder{vars: map[string]string{"OPENROUTER_API_KEY": "sk-or-getenv", "OPENROUTER_BASE_URL": srv.URL()}}
	r, err := Resolve(Config{Provider: "openrouter", Model: "STANDARD", Getenv: env.Getenv})
	if err != nil || r.Client == nil {
		t.Fatalf("Resolve = %v, %v", r.Client, err)
	}
	streamOnce(t, r.Client)
	reqs := srv.requests()
	if len(reqs) != 1 || reqs[0].Path != "/chat/completions" || reqs[0].Header.Get("Authorization") != "Bearer sk-or-getenv" {
		t.Errorf("requests = %+v, want one POST /chat/completions with the key from Getenv", reqs)
	}
}

// TS-29-12: OpenRouter's constructor errors come back from Resolve as they are.
func TestOpenRouterConstructorErrorsAreReturnedUnchanged_TS29_12(t *testing.T) {
	sel, err := catalog.Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		env  map[string]string
		is   error
	}{
		{"no key", map[string]string{}, openrouter.ErrNoAPIKey},
		{"a base URL that is not one", map[string]string{"OPENROUTER_API_KEY": "k", "OPENROUTER_BASE_URL": "not a url"}, nil},
	} {
		_, want := openrouter.New(sel.Model, openrouter.Options{Env: c.env, Getenv: noEnv})
		r, got := Resolve(Config{Provider: "openrouter", Model: "STANDARD", Env: c.env, Getenv: noEnv})
		if want == nil || got == nil || got.Error() != want.Error() || (c.is != nil && !errors.Is(got, c.is)) {
			t.Errorf("%s: Resolve error = %v, want openrouter.New's %v", c.name, got, want)
		}
		if r.Client != nil || r.Provider != "" {
			t.Errorf("%s: Resolution = %+v, want the zero value", c.name, r)
		}
	}
}

// TS-29-13: Ollama needs no credential, and its constructor's error comes back
// as it is.
func TestOllamaNeedsNoCredential_TS29_13(t *testing.T) {
	r, err := Resolve(Config{Provider: "ollama", Model: "STANDARD", Getenv: noEnv})
	if err != nil || r.Client == nil || r.Model.ID != "qwen3.8:27b" {
		t.Fatalf("Resolve = %+v, %v; want a client for qwen3.8:27b and no error", r, err)
	}
	env := map[string]string{"OLLAMA_HOST": "ftp://example.com"}
	sel, err := catalog.Select("ollama", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	_, want := ollama.New(sel.Model, ollama.Options{Env: env, Getenv: noEnv})
	r, got := Resolve(Config{Provider: "ollama", Model: "STANDARD", Env: env, Getenv: noEnv})
	if want == nil || got == nil || got.Error() != want.Error() || !errors.Is(got, ollama.ErrInvalidHost) {
		t.Errorf("Resolve error = %v, want ollama.New's %v", got, want)
	}
	if r.Client != nil || r.Provider != "" {
		t.Errorf("Resolution = %+v, want the zero value", r)
	}
}

// TS-29-14: a given client is returned as it is and no variable is read; the
// provider and model still choose the row and the effort.
func TestAGivenClientIsReturnedAndNothingIsRead_TS29_14(t *testing.T) {
	fp := faux.New()
	env := &envRecorder{vars: allKeys}
	r, err := Resolve(Config{Client: fp, Provider: "openrouter", Model: "STANDARD", Effort: "LOW", Getenv: env.Getenv})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Client != core.ProviderClient(fp) {
		t.Errorf("Resolution.Client = %v, want the given client", r.Client)
	}
	if got := env.asked(); len(got) != 0 {
		t.Errorf("Getenv was asked for %v, want nothing", got)
	}
	if r.Model.ID != "qwen/qwen3.8-flash" || r.Effort != EffortLow {
		t.Errorf("model %q effort %q, want qwen/qwen3.8-flash and low", r.Model.ID, r.Effort)
	}
}

// TS-29-15: a host that cannot be reached is found by the first request, not by
// Resolve or New.
func TestAnUnreachableHostIsFoundByTheFirstRequest_TS29_15(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Provider: "openrouter", Model: "STANDARD", Getenv: noEnv,
		Env: map[string]string{"OPENROUTER_API_KEY": "k", "OPENROUTER_BASE_URL": "http://" + addr}}
	if _, err := Resolve(cfg); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := a.Run(ctx, "hi")
	if err == nil || res.StopReason != core.RunStopError || !strings.Contains(err.Error(), addr) {
		t.Errorf("Run = %v (stop %q), want an error carrying the transport error for %s", err, res.StopReason, addr)
	}
}

// TS-29-39: an agent that picks Anthropic by name, or by default, sends the
// request body a provider built directly with the same environment sends.
func TestAnthropicByNameSendsTheSameBodyAsADirectProvider_TS29_39(t *testing.T) {
	const model = "claude-opus-5-5"
	row, listed := catalog.Lookup(model)
	if !listed {
		t.Fatalf("%s is not listed", model)
	}
	body := func(srv *providerServer) []byte {
		t.Helper()
		reqs := srv.requests()
		if len(reqs) != 1 {
			t.Fatalf("the server received %d requests, want 1", len(reqs))
		}
		return reqs[0].Body
	}
	vars := func(srv *providerServer) map[string]string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-ant-test", "ANTHROPIC_BASE_URL": srv.URL()}
	}

	// The request the agent builds for a first prompt with no tools.
	max := DefaultMaxTokens
	req := core.Request{
		System:    []core.ContentBlock{core.TextBlock{Text: prompt.Build("", nil)}},
		Messages:  core.Messages{userText("hi")},
		MaxTokens: &max,
	}
	directSrv := anthropicServer(t)
	direct := anthropic.Provider(row, anthropic.Options{Env: vars(directSrv), Getenv: noEnv})
	ch, err := direct.Stream(context.Background(), req)
	for range core.EventStreamOf(ch, err).Events() {
	}
	want := body(directSrv)

	for _, provider := range []string{"", "anthropic"} {
		srv := anthropicServer(t)
		a, err := New(Config{Provider: provider, Model: model, Env: vars(srv), Getenv: noEnv})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		if got := body(srv); !bytes.Equal(got, want) {
			t.Errorf("Provider %q sent\n%s\nwant the direct provider's\n%s", provider, got, want)
		}
	}
}
