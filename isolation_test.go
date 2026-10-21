package agentkit

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// providers is every provider an agent can name, in the catalog's order.
var providers = []string{"anthropic", "openrouter", "ollama"}

// ownsVariable reports whether a variable belongs to the named provider: the
// set that provider's code is allowed to look up.
func ownsVariable(provider, name string) bool {
	var prefixes []string
	switch provider {
	case "anthropic":
		prefixes = []string{"ANTHROPIC_", "CLAUDE_CODE_", "GOOGLE_CLOUD_", "CLOUD_ML_REGION", "CLOUDSDK_", "AWS_"}
	case "openrouter":
		prefixes = []string{"OPENROUTER_"}
	case "ollama":
		prefixes = []string{"OLLAMA_"}
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// rig is the three recording servers and the environment of an embedder that
// holds every provider's secrets at once, each base URL pointing at its own
// server.
type rig struct {
	servers map[string]*providerServer
	secrets map[string]string
	env     *envRecorder
}

// fixedSecrets are the secrets every provider's variables hold.
var fixedSecrets = map[string]string{
	"ANTHROPIC_API_KEY":    "sk-ant-api-0f1e2d3c4b5a6978",
	"ANTHROPIC_AUTH_TOKEN": "auth-tok-8796a5b4c3d2e1f0",
	"OPENROUTER_API_KEY":   "sk-or-v1-1a2b3c4d5e6f7081",
	"OLLAMA_API_KEY":       "ollama-key-9182736455a4b3c2",
}

func newRig(t *testing.T, secrets map[string]string) *rig {
	t.Helper()
	r := &rig{
		servers: map[string]*providerServer{
			"anthropic": anthropicServer(t), "openrouter": openRouterServer(t, ""), "ollama": ollamaServer(t),
		},
		secrets: secrets,
	}
	vars := map[string]string{
		"ANTHROPIC_BASE_URL":  r.servers["anthropic"].URL(),
		"OPENROUTER_BASE_URL": r.servers["openrouter"].URL(),
		"OLLAMA_HOST":         r.servers["ollama"].URL(),
	}
	for k, v := range secrets {
		vars[k] = v
	}
	r.env = &envRecorder{vars: vars}
	return r
}

// config is the embedder's Config for the named provider: no variable but the
// recording Getenv.
func (r *rig) config(provider string) Config {
	return Config{Provider: provider, Model: "STANDARD", Getenv: r.env.Getenv}
}

// run resolves, builds an agent and runs one prompt on the named provider.
func (r *rig) run(t *testing.T, provider string) {
	t.Helper()
	if _, err := Resolve(r.config(provider)); err != nil {
		t.Fatalf("%s: Resolve: %v", provider, err)
	}
	a, err := New(r.config(provider))
	if err != nil {
		t.Fatalf("%s: New: %v", provider, err)
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("%s: Run: %v", provider, err)
	}
}

// mentions lists where, on which server, a request carries the value in a
// header or in its body.
func (r *rig) mentions(value string) []string {
	var where []string
	for name, s := range r.servers {
		for i, req := range s.requests() {
			if strings.Contains(fmt.Sprint(req.Header), value) || strings.Contains(string(req.Body), value) {
				where = append(where, fmt.Sprintf("the %s server's request %d", name, i))
			}
		}
	}
	return where
}

// TS-29-16: Resolve, New and a run ask Getenv only for the chosen provider's
// variables, and never for an AGENTKIT_ one.
func TestOnlyTheChosenProvidersVariablesAreAsked_TS29_16(t *testing.T) {
	for _, p := range providers {
		r := newRig(t, fixedSecrets)
		r.run(t, p)
		asked := r.env.asked()
		if len(asked) == 0 {
			t.Errorf("%s: Getenv was never asked for anything", p)
		}
		for _, name := range asked {
			if !ownsVariable(p, name) {
				t.Errorf("%s: Getenv was asked for %s, which is not one of its variables", p, name)
			}
			if strings.HasPrefix(name, "AGENTKIT_") {
				t.Errorf("%s: Getenv was asked for %s", p, name)
			}
		}
	}
}

// TS-29-17: with every secret set and each base URL pointing at its own server,
// a run reaches only the chosen provider's server.
func TestARunReachesOnlyTheChosenServer_TS29_17(t *testing.T) {
	for _, p := range providers {
		r := newRig(t, fixedSecrets)
		r.run(t, p)
		if n := r.servers[p].count(); n < 1 {
			t.Errorf("%s: its own server received %d requests, want at least one", p, n)
		}
		for q, s := range r.servers {
			if q != p && s.count() != 0 {
				t.Errorf("%s: the %s server received %d requests, want none", p, q, s.count())
			}
		}
	}
}

// secretsOf returns the secret variables a provider holds.
func secretsOf(provider string) []string {
	switch provider {
	case "anthropic":
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}
	case "openrouter":
		return []string{"OPENROUTER_API_KEY"}
	}
	return nil
}

// TS-29-18 (property): for any ordered pair of distinct providers and any
// distinct secrets, running on the first never puts a secret of the second in
// any header or body, and the Ollama key is never sent anywhere.
func TestNoSecretReachesAnotherProvider_TS29_18(t *testing.T) {
	rnd := rand.New(rand.NewSource(18))
	secret := func(prefix string) string { return fmt.Sprintf("%s%08x%08x", prefix, rnd.Uint32(), rnd.Uint32()) }
	for range 12 {
		secrets := map[string]string{
			"ANTHROPIC_API_KEY":    secret("sk-ant-api-"),
			"ANTHROPIC_AUTH_TOKEN": secret("auth-tok-"),
			"OPENROUTER_API_KEY":   secret("sk-or-v1-"),
			"OLLAMA_API_KEY":       secret("ollama-key-"),
		}
		for _, p := range providers {
			r := newRig(t, secrets)
			r.run(t, p)
			for _, q := range providers {
				if q == p {
					continue
				}
				for _, name := range secretsOf(q) {
					if where := r.mentions(secrets[name]); len(where) != 0 {
						t.Errorf("running on %s sent %s (the %s secret) in %v", p, name, q, where)
					}
				}
			}
			if where := r.mentions(secrets["OLLAMA_API_KEY"]); len(where) != 0 {
				t.Errorf("running on %s sent OLLAMA_API_KEY in %v", p, where)
			}
			// The scan is not vacuous: a provider that has a credential sends it.
			for _, name := range secretsOf(p) {
				if p == "anthropic" && name == "ANTHROPIC_AUTH_TOKEN" {
					continue // the API key is what Anthropic's SDK sends when both are set
				}
				if where := r.mentions(secrets[name]); len(where) == 0 {
					t.Errorf("running on %s did not send its own %s: the scan would find nothing in any case", p, name)
				}
			}
		}
	}
}
