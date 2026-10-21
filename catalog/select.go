package catalog

import (
	"errors"
	"fmt"
	"strings"

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

// Select resolves a (provider, model, effort) triple against the embedded
// catalog; see (*Catalog).Select.
func Select(provider, model string, effort core.Effort) (Selection, error) {
	return Default().Select(provider, model, effort)
}

// Select turns a provider, a model and an effort into the catalog row and the
// effort a run will use. It is offline: it reads no environment variable and
// makes no request, and every Model it returns is a deep copy.
//
// The provider is trimmed and matched without regard to case; an empty one
// means anthropic, and a name that is not one of Providers is refused
// (ErrUnknownProvider), whatever vendors the catalog holds.
//
// The model is a tier name or a model id. A string that, trimmed, is SIMPLE,
// STANDARD or ADVANCED in any case is always the tier, never an id: it picks
// the model and the effort from the provider's tier table, and Selection.Tier
// is the canonical tier. A tiers block always names a row, so Listed is true;
// a provider whose section in a parsed catalog has no tiers block is an error
// naming the provider and the tier. Anything else is an id. An id is only
// trimmed, never case-folded, and is looked up among the rows of the
// provider's own vendor, so a row of one provider is never returned for
// another. On anthropic the id goes through Lookup, which accepts an optional
// "anthropic/" prefix. An id the catalog does not list is served, not refused,
// with the provider's fallback limits, no price and Listed false.
//
// The effort is trimmed and folded to one of the five levels, or empty for none
// (ErrUnknownEffort otherwise). Selection.Effort is the explicit effort, else
// the tier's, else empty. It is reported as asked: Select neither filters nor
// clamps it against Model.Efforts, which is the provider's reading.
//
// The inputs are checked in the order provider, model, effort, and the first
// that fails is the error returned, with the zero Selection.
func (c *Catalog) Select(provider, model string, effort core.Effort) (Selection, error) {
	p, err := providerOf(provider)
	if err != nil {
		return Selection{}, err
	}
	id := strings.TrimSpace(model)
	if id == "" {
		return Selection{}, fmt.Errorf("%w given; name a tier (%s); a model id is also accepted",
			ErrNoModel, joinWords(tierNames, "or"))
	}
	e, err := effortOf(effort)
	if err != nil {
		return Selection{}, err
	}
	if tier, ok := tierOf(id); ok {
		return c.selectTier(p, tier, e)
	}
	m, listed := c.resolve(p, id)
	return Selection{Provider: p, Model: m, Effort: e, Listed: listed}, nil
}

// providerOf returns the canonical provider named by name: trimmed, matched
// without regard to case, and anthropic when empty.
func providerOf(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return ProviderAnthropic, nil
	}
	known := Providers()
	for _, p := range known {
		if strings.EqualFold(n, p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w %q; known providers are %s", ErrUnknownProvider, name, joinWords(known, "and"))
}

// effortOf returns the core.Effort constant that e names, trimmed and matched
// without regard to case, or "" for none.
func effortOf(e core.Effort) (core.Effort, error) {
	n := strings.TrimSpace(string(e))
	if n == "" {
		return "", nil
	}
	for _, known := range allEfforts {
		if strings.EqualFold(n, string(known)) {
			return known, nil
		}
	}
	return "", fmt.Errorf("%w %q; known efforts are %s", ErrUnknownEffort, string(e), joinWords(allEfforts, "and"))
}

// tierOf reports whether s, already trimmed, is a tier name in any case. A
// string that merely contains a tier name is a model id.
func tierOf(s string) (Tier, bool) {
	for _, t := range tierNames {
		if strings.EqualFold(s, string(t)) {
			return t, true
		}
	}
	return "", false
}

// selectTier resolves a tier of the provider to the row the tier's table entry
// names. The effort is the explicit one when given and otherwise the tier's;
// it is not checked against the row, because Parse already did for the tier's
// own and an explicit one is the caller's to request.
//
// A vendor section a parsed catalog lacks, or one without a tiers block, has no
// tiers to resolve.
func (c *Catalog) selectTier(provider string, tier Tier, effort core.Effort) (Selection, error) {
	var pick tierRow
	if v := c.vendors[provider]; v != nil {
		pick = v.tierTable[tier]
	}
	if pick.Model == "" {
		return Selection{}, fmt.Errorf("agentkit: provider %q has no %s tier in this catalog", provider, tier)
	}
	if effort == "" {
		effort = core.Effort(pick.Effort)
	}
	// Parse checked that the tier names a row of this vendor.
	row := c.byCanonical[provider+"/"+pick.Model]
	return Selection{Provider: provider, Tier: tier, Model: cloneModel(row.model), Effort: effort, Listed: true}, nil
}

// resolve returns the descriptor for a trimmed model id of the provider and
// whether the provider's vendor lists it.
func (c *Catalog) resolve(provider, id string) (core.Model, bool) {
	var window, maxTokens int
	switch provider {
	case ProviderOpenRouter:
		window, maxTokens = OpenRouterDefaultContextWindow, OpenRouterDefaultMaxTokens
	case ProviderOllama:
		window, maxTokens = OllamaDefaultContextWindow, OllamaDefaultMaxTokens
	default:
		// anthropic: Lookup strips an optional "anthropic/" prefix and serves
		// the Anthropic fallback.
		return c.Lookup(id)
	}
	// A vendor id holds no slash, so "<provider>/<id>" names one row even when
	// the id has slashes of its own.
	if e, ok := c.byCanonical[provider+"/"+id]; ok {
		return cloneModel(e.model), true
	}
	return core.Model{
		ID:            id,
		ContextWindow: window, MaxOutputTokens: maxTokens,
		ThinkingKind: core.ThinkingKindAdaptive,
	}, false
}

// joinWords joins items as "a, b and c", with conj in place of "and".
func joinWords[T ~string](items []T, conj string) string {
	words := make([]string, len(items))
	for i, it := range items {
		words[i] = string(it)
	}
	if len(words) < 2 {
		return strings.Join(words, "")
	}
	return strings.Join(words[:len(words)-1], ", ") + " " + conj + " " + words[len(words)-1]
}
