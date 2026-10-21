package catalog

import (
	"errors"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// The providers this build can run. The set is fixed in code, not taken from
// the catalog's vendors: a vendor section that names a provider with no client
// behind it must not be selectable. Each name equals the catalog vendor id of
// the same spelling.
const (
	ProviderAnthropic  = "anthropic"
	ProviderOpenRouter = "openrouter"
	ProviderOllama     = "ollama"
)

// Providers lists the providers in the order anthropic, openrouter, ollama.
// Every call returns a new slice.
func Providers() []string {
	return []string{ProviderAnthropic, ProviderOpenRouter, ProviderOllama}
}

// Tier is a model preset: a name that picks a model id and an effort for the
// chosen provider from the catalog's tier table.
type Tier string

// The three tiers. The table is fixed per release; an embedder that wants
// another mapping names the model and the effort directly.
const (
	TierSimple   Tier = "SIMPLE"
	TierStandard Tier = "STANDARD"
	TierAdvanced Tier = "ADVANCED"
)

// Selection is what Select resolves a (provider, model, effort) triple to.
type Selection struct {
	// Provider is the canonical provider name: one of Providers().
	Provider string
	// Tier is the canonical tier when the model string named one, else "".
	Tier Tier
	// Model is a deep copy of the catalog row, or the provider's fallback
	// descriptor for an id the catalog does not list. Model.ID is the id the
	// vendor will receive.
	Model core.Model
	// Effort is the effort to request: the explicit one, else the tier's, else
	// "". Whether it is sent, and in what form, is the provider's reading of
	// Model.Efforts, as on Anthropic today; Select never clamps it.
	Effort core.Effort
	// Listed reports whether Model is a row of the provider's vendor section.
	Listed bool
}

// The errors Select returns for bad input. Each is wrapped with the value
// given and the valid choices, so errors.Is classifies it and the message
// still says what to type instead.
var (
	// ErrUnknownProvider is the provider name Select does not know.
	ErrUnknownProvider = errors.New("agentkit: unknown provider")
	// ErrUnknownEffort is an effort outside low, medium, high, xhigh and max.
	ErrUnknownEffort = errors.New("agentkit: unknown effort")
	// ErrNoModel is an empty model string: neither a tier nor a model id.
	ErrNoModel = errors.New("agentkit: no model")
)
