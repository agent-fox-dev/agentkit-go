package agentkit

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
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
