package agentkit

import (
	"os"
	"slices"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// Resolution is what a Config resolves to: the provider, the model and the
// effort an agent runs with, and the client it calls. Resolve returns one
// without building an agent, and Agent.Resolution reports the one an agent was
// built with.
type Resolution struct {
	// Provider is the canonical provider name: one of catalog.Providers.
	Provider string
	// Tier is the tier Config.Model named, or empty when it named a model id.
	Tier catalog.Tier
	// Model is the catalog row, or the provider's fallback row for an id the
	// catalog does not list. Model.ID is the id the vendor receives.
	Model core.Model
	// Effort is the effort requested, normalized: the explicit one, else the
	// tier's, else empty, which sends none.
	Effort Effort
	// Listed reports whether Model is a catalog row and not a fallback.
	Listed bool
	// Client is the client a run calls: Config.Client when it was set, else the
	// one built for Provider.
	Client core.ProviderClient
}

// Resolve turns the selection fields of cfg (Client, Provider, Model, Effort,
// Env and Getenv) into what New would run: the catalog's choice for the
// provider, model and effort, and the client to call.
//
// It makes no request and keeps no state, and every call builds a new client.
// It reads no variable itself: with Config.Client nil, the chosen provider's
// own constructor looks up that provider's credential and endpoint (Env first,
// then Getenv, or the process environment when Getenv is nil; an empty value is
// unset), and no other provider's code runs. A credential that is missing, or
// an endpoint the provider cannot use, is an error here, before any run. A
// host that cannot be reached is not: that is found by the first request.
//
// The errors of catalog.Select and of the provider's constructor are returned
// unchanged, so errors.Is matches catalog.ErrUnknownProvider, catalog.ErrNoModel,
// catalog.ErrUnknownEffort, openrouter.ErrNoAPIKey and anthropic.ErrNoCredentials,
// and the message names the value or the variable. The catalog's are returned
// even when Config.Client is set, which Resolve then returns as it is, reading
// no variable.
func Resolve(cfg Config) (Resolution, error) {
	sel, err := catalog.Select(cfg.Provider, cfg.Model, cfg.Effort)
	if err != nil {
		return Resolution{}, err
	}
	res := Resolution{Provider: sel.Provider, Tier: sel.Tier, Model: sel.Model,
		Effort: sel.Effort, Listed: sel.Listed, Client: cfg.Client}
	if res.Client != nil {
		return res, nil
	}
	if res.Client, err = buildClient(sel, cfg); err != nil {
		return Resolution{}, err
	}
	return res, nil
}

// buildClient builds the client of the selected provider from the cfg's
// variables, and returns the constructor's error as it is.
func buildClient(sel catalog.Selection, cfg Config) (core.ProviderClient, error) {
	switch sel.Provider {
	case catalog.ProviderOpenRouter:
		return openrouter.New(sel.Model, openrouter.Options{Env: cfg.Env, Getenv: cfg.Getenv})
	case catalog.ProviderOllama:
		c, err := ollama.New(sel.Model, ollama.Options{Env: cfg.Env, Getenv: cfg.Getenv})
		if err != nil {
			return nil, err
		}
		return c, nil
	}
	// The provider resolves its deployment on every request, so a credential
	// that is missing is only found on the first one. Resolving once here, over
	// the same lookup, finds it at construction instead; the SDK client it
	// builds is not kept, and the provider is built from the variables, as an
	// embedder's own would be.
	if _, _, err := anthropic.Resolve(lookupEnv{set: cfg.Env, getenv: cfg.Getenv}); err != nil {
		return nil, err
	}
	return anthropic.Provider(sel.Model, anthropic.Options{Env: cfg.Env, Getenv: cfg.Getenv}), nil
}

// providerAPI is the API constant of the package buildClient builds a client
// from for the named provider; a run on such a client reports it.
func providerAPI(provider string) core.API {
	switch provider {
	case catalog.ProviderOpenRouter:
		return openrouter.API
	case catalog.ProviderOllama:
		return ollama.API
	}
	return anthropic.API
}

// lookupEnv is the anthropic.Env of a Config: the order every provider reads
// its variables in. A non-empty value in set wins; then getenv, or the process
// environment when it is nil; an empty value is unset at every step.
type lookupEnv struct {
	set    map[string]string
	getenv func(string) string
}

func (e lookupEnv) Lookup(name string) (string, bool) {
	if v := e.set[name]; v != "" {
		return v, true
	}
	get := os.Getenv
	if e.getenv != nil {
		get = e.getenv
	}
	v := get(name)
	return v, v != ""
}

// cloneModel returns m sharing nothing with it: the effort map, the values it
// points to and the capability bytes are copied.
func cloneModel(m core.Model) core.Model {
	if m.Efforts != nil {
		efforts := make(map[core.Effort]*string, len(m.Efforts))
		for k, v := range m.Efforts {
			if v != nil {
				s := *v
				v = &s
			}
			efforts[k] = v
		}
		m.Efforts = efforts
	}
	m.Compat = slices.Clone(m.Compat)
	return m
}
