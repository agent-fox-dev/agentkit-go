package catalog

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// zeroSelection reports whether sel is the zero Selection, which is what every
// error return carries. A Selection holds a map, so it cannot be compared
// with ==.
func zeroSelection(sel Selection) bool { return reflect.DeepEqual(sel, Selection{}) }

// wantMessageContains fails the test unless msg contains every part.
func wantMessageContains(t *testing.T, msg string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(msg, p) {
			t.Errorf("message %q does not contain %q", msg, p)
		}
	}
}

// TS-26-3: an unknown provider fails with ErrUnknownProvider and a message
// that names the value as given and the three providers.
func TestSelectUnknownProvider_TS26_3(t *testing.T) {
	sel, err := Select("openai", "STANDARD", "")
	if !zeroSelection(sel) {
		t.Errorf("Selection = %+v, want the zero value", sel)
	}
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("error = %v, want one wrapping ErrUnknownProvider", err)
	}
	const want = `agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama`
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
	// The value is named as given, whitespace included.
	_, err = Select(" Azure ", "x", "")
	wantMessageContains(t, err.Error(), `" Azure "`)
}

// TS-26-4: a vendor of a parsed catalog that is not one of the three providers
// cannot be selected, because the provider set comes from code.
func TestSelectRefusesAVendorThatIsNotAProvider_TS26_4(t *testing.T) {
	c := testCatalog(t)
	for _, p := range []string{"azure", "openai"} {
		sel, err := c.Select(p, "some-model", "")
		if !zeroSelection(sel) {
			t.Errorf("Select(%q) = %+v, want the zero value", p, sel)
		}
		if !errors.Is(err, ErrUnknownProvider) {
			t.Errorf("Select(%q) error = %v, want one wrapping ErrUnknownProvider", p, err)
		}
	}
}

// TS-26-5: a known provider whose vendor section a parsed catalog lacks still
// selects, with the provider's fallback descriptor.
func TestSelectKnownProviderWithoutVendorSection_TS26_5(t *testing.T) {
	c := testCatalog(t) // has no ollama vendor
	sel, err := c.Select("ollama", "llama4:8b", "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.Listed {
		t.Error("Listed = true for an id of a vendor the catalog lacks")
	}
	m := sel.Model
	if m.ID != "llama4:8b" || m.ContextWindow != 32768 || m.MaxOutputTokens != 32768 {
		t.Errorf("Model = %q %d/%d, want llama4:8b 32768/32768", m.ID, m.ContextWindow, m.MaxOutputTokens)
	}
	if m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 {
		t.Errorf("a fallback has no price, got %v/%v", m.InputCostPerMillion, m.OutputCostPerMillion)
	}
}

// TS-26-10: a model id is trimmed and keeps its case, and a listed id of
// openrouter or ollama returns its row with the id as written.
func TestSelectModelIDsAreTrimmedNotFolded_TS26_10(t *testing.T) {
	if sel, err := Select("openrouter", "  Qwen/X  ", ""); err != nil || sel.Model.ID != "Qwen/X" {
		t.Errorf("Select(openrouter, \"  Qwen/X  \") = %q, %v; want the id Qwen/X", sel.Model.ID, err)
	}
	sel, err := Select("ollama", "qwen3.8:27b", "")
	if err != nil || !sel.Listed || sel.Model.ContextWindow != 32768 || sel.Model.MaxOutputTokens != 32768 {
		t.Errorf("Select(ollama, qwen3.8:27b) = %+v, %v; want the listed 32768/32768 row", sel, err)
	}
	sel, err = Select("openrouter", " qwen/qwen3.8-flash ", "")
	if err != nil || !sel.Listed || sel.Model.ID != "qwen/qwen3.8-flash" {
		t.Errorf("Select(openrouter, \" qwen/qwen3.8-flash \") = %+v, %v; want the listed row", sel, err)
	}
	// Case is part of a vendor id: a different case is a different, unlisted id.
	if sel, _ := Select("ollama", "QWEN3.8:27B", ""); sel.Listed || sel.Model.ID != "QWEN3.8:27B" {
		t.Errorf("Select(ollama, QWEN3.8:27B) = %q listed=%v, want an unlisted id kept as written", sel.Model.ID, sel.Listed)
	}
}

// TS-26-11: on anthropic, Select resolves ids through Lookup: the optional
// anthropic/ prefix, the rows and the unchanged fallback.
func TestSelectAnthropicResolvesThroughLookup_TS26_11(t *testing.T) {
	for _, id := range []string{"anthropic/claude-sonnet-5-5", "claude-opus-5-5", "claude-future-9"} {
		sel, err := Select("anthropic", id, "")
		if err != nil {
			t.Fatalf("Select(anthropic, %q): %v", id, err)
		}
		want, ok := Lookup(id)
		if !reflect.DeepEqual(sel.Model, want) || sel.Listed != ok {
			t.Errorf("Select(anthropic, %q) = %+v listed=%v, want Lookup's %+v listed=%v", id, sel.Model, sel.Listed, want, ok)
		}
	}
	sel, _ := Select("anthropic", "anthropic/claude-sonnet-5-5", "")
	if sel.Model.ID != "claude-sonnet-5-5" || !sel.Listed {
		t.Errorf("the prefixed id gave %q listed=%v, want claude-sonnet-5-5 listed", sel.Model.ID, sel.Listed)
	}
	sel, _ = Select("anthropic", "claude-future-9", "")
	if sel.Listed || sel.Model.ContextWindow != DefaultContextWindow || sel.Model.MaxOutputTokens != DefaultMaxTokens {
		t.Errorf("unlisted id = %+v, want the Anthropic fallback %d/%d", sel, DefaultContextWindow, DefaultMaxTokens)
	}
}

// TS-26-12: a row of one provider is never returned for another provider.
func TestSelectNeverCrossesProviders_TS26_12(t *testing.T) {
	sel, err := Select("openrouter", "anthropic/claude-sonnet-5-5", "")
	if err != nil || sel.Listed || sel.Model.ID != "anthropic/claude-sonnet-5-5" {
		t.Errorf("openrouter + anthropic id = %q listed=%v, %v; want an unlisted id with its prefix", sel.Model.ID, sel.Listed, err)
	}
	sel, err = Select("ollama", "qwen/qwen3.8-flash", "")
	if err != nil || sel.Listed || sel.Model.ContextWindow != 32768 || sel.Model.MaxOutputTokens != 32768 {
		t.Errorf("ollama + openrouter id = %+v, %v; want the Ollama fallback 32768/32768, not the 1000000 row", sel, err)
	}
	sel, err = Select("openrouter", "qwen3.8:27b", "")
	if err != nil || sel.Listed || sel.Model.ContextWindow != OpenRouterDefaultContextWindow {
		t.Errorf("openrouter + ollama id = %+v, %v; want the OpenRouter fallback", sel, err)
	}
	sel, err = Select("anthropic", "qwen/qwen3.8-flash", "")
	if err != nil || sel.Listed || sel.Model.ContextWindow != DefaultContextWindow {
		t.Errorf("anthropic + openrouter id = %+v, %v; want the Anthropic fallback", sel, err)
	}
}

// TS-26-14: an unlisted id is served with its provider's fallback limits, no
// price, adaptive thinking and a nil Efforts map.
func TestSelectUnlistedIDsGetTheProviderFallback_TS26_14(t *testing.T) {
	if OpenRouterDefaultContextWindow != 131_072 || OpenRouterDefaultMaxTokens != 32_768 ||
		OllamaDefaultContextWindow != 32_768 || OllamaDefaultMaxTokens != 32_768 {
		t.Errorf("fallback constants = %d/%d and %d/%d, want 131072/32768 and 32768/32768",
			OpenRouterDefaultContextWindow, OpenRouterDefaultMaxTokens, OllamaDefaultContextWindow, OllamaDefaultMaxTokens)
	}
	cases := []struct {
		provider, model, id string
		window, maxTokens   int
	}{
		{"openrouter", " vendor/new-model ", "vendor/new-model", 131072, 32768},
		{"ollama", "llama4:8b", "llama4:8b", 32768, 32768},
		{"anthropic", "claude-future-9", "claude-future-9", 1_000_000, 128_000},
	}
	for _, tc := range cases {
		sel, err := Select(tc.provider, tc.model, "")
		if err != nil {
			t.Errorf("Select(%q, %q): %v", tc.provider, tc.model, err)
			continue
		}
		m := sel.Model
		if sel.Listed || m.ID != tc.id || m.ContextWindow != tc.window || m.MaxOutputTokens != tc.maxTokens {
			t.Errorf("Select(%q, %q) = %q %d/%d listed=%v, want %q %d/%d unlisted",
				tc.provider, tc.model, m.ID, m.ContextWindow, m.MaxOutputTokens, sel.Listed, tc.id, tc.window, tc.maxTokens)
		}
		if m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 || m.CacheReadCostPerMillion != 0 || m.CacheWriteCostPerMillion != 0 {
			t.Errorf("Select(%q, %q) is priced: %+v", tc.provider, tc.model, m)
		}
		if m.ThinkingKind != core.ThinkingKindAdaptive || m.Efforts != nil {
			t.Errorf("Select(%q, %q) thinking = %q, efforts = %v; want adaptive and nil", tc.provider, tc.model, m.ThinkingKind, m.Efforts)
		}
	}
}

// TS-26-21: the package-level Select equals Default().Select, and the inputs
// are checked in the order provider, model, effort.
func TestSelectChecksInputsInOrder_TS26_21(t *testing.T) {
	triples := [][3]string{
		{"anthropic", "claude-sonnet-5-5", "high"},
		{"openrouter", "qwen/qwen3.8-flash", ""},
		{" OLLAMA ", "llama4:8b", " Low "},
		{"", "claude-future-9", "max"},
		{"openai", "x", ""},
		{"anthropic", "", ""},
		{"anthropic", "x", "turbo"},
	}
	for _, tr := range triples {
		got, gotErr := Select(tr[0], tr[1], core.Effort(tr[2]))
		want, wantErr := Default().Select(tr[0], tr[1], core.Effort(tr[2]))
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("Select%q = %+v, %v; Default().Select gave %+v, %v", tr, got, gotErr, want, wantErr)
		}
	}

	_, err := Select("openai", "", "bogus")
	if !errors.Is(err, ErrUnknownProvider) || errors.Is(err, ErrNoModel) || errors.Is(err, ErrUnknownEffort) {
		t.Errorf("Select(openai, \"\", bogus) error = %v, want only ErrUnknownProvider", err)
	}
	_, err = Select("anthropic", "", "bogus")
	if !errors.Is(err, ErrNoModel) || errors.Is(err, ErrUnknownEffort) {
		t.Errorf("Select(anthropic, \"\", bogus) error = %v, want only ErrNoModel", err)
	}
}

// TS-26-22: an effort is trimmed and folded to the lower-case constant, and an
// effort outside the five fails with ErrUnknownEffort.
func TestSelectNormalizesEfforts_TS26_22(t *testing.T) {
	for _, tc := range []struct {
		in   core.Effort
		want core.Effort
	}{
		{" XHigh ", core.EffortXHigh},
		{"MAX", core.EffortMax},
		{"Low", core.EffortLow},
		{"", ""},
		{"  ", ""},
	} {
		sel, err := Select("anthropic", "claude-sonnet-5-5", tc.in)
		if err != nil || sel.Effort != tc.want {
			t.Errorf("Select(effort %q) = %q, %v; want %q", tc.in, sel.Effort, err, tc.want)
		}
	}

	sel, err := Select("anthropic", "claude-sonnet-5-5", "turbo")
	if !zeroSelection(sel) {
		t.Errorf("Selection = %+v, want the zero value", sel)
	}
	if !errors.Is(err, ErrUnknownEffort) {
		t.Fatalf("error = %v, want one wrapping ErrUnknownEffort", err)
	}
	if !strings.HasPrefix(err.Error(), "agentkit:") {
		t.Errorf("message %q does not start with agentkit:", err)
	}
	wantMessageContains(t, err.Error(), `"turbo"`, "low", "medium", "high", "xhigh", "max")
}

// TS-26-23: an empty model fails with ErrNoModel and a message that names the
// tiers, and the three sentinels are distinct.
func TestSelectNoModel_TS26_23(t *testing.T) {
	sel, err := Select("anthropic", "   ", "")
	if !zeroSelection(sel) {
		t.Errorf("Selection = %+v, want the zero value", sel)
	}
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("error = %v, want one wrapping ErrNoModel", err)
	}
	if !strings.HasPrefix(err.Error(), "agentkit:") {
		t.Errorf("message %q does not start with agentkit:", err)
	}
	wantMessageContains(t, err.Error(), "SIMPLE", "STANDARD", "ADVANCED", "model id")

	sentinels := []error{ErrUnknownProvider, ErrUnknownEffort, ErrNoModel, ErrCorruptCatalog}
	for i, a := range sentinels {
		if !strings.HasPrefix(a.Error(), "agentkit:") {
			t.Errorf("sentinel %q does not start with agentkit:", a)
		}
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("errors.Is(%q, %q) = true, want the sentinels distinct", a, b)
			}
		}
	}
}

// TS-26-2: provider names are trimmed and case-folded, and an empty name means
// anthropic.
func TestSelectCanonicalizesTheProvider_TS26_2(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{" OpenRouter ", "openrouter"},
		{"OLLAMA", "ollama"},
		{"", "anthropic"},
		{"   ", "anthropic"},
	} {
		sel, err := Select(tc.in, "STANDARD", "")
		if err != nil || sel.Provider != tc.want {
			t.Errorf("Select(%q, STANDARD) = %q, %v; want provider %q", tc.in, sel.Provider, err, tc.want)
		}
	}
	empty, err1 := Select("", "STANDARD", "")
	named, err2 := Select("anthropic", "STANDARD", "")
	if err1 != nil || err2 != nil || !reflect.DeepEqual(empty, named) {
		t.Errorf("Select(\"\", STANDARD) = %+v, %v; Select(anthropic, STANDARD) = %+v, %v; want equal", empty, err1, named, err2)
	}
}

// TS-26-15: a whole-string tier name in any case is a tier; a string that
// merely contains one is a model id.
func TestSelectRecognisesTierNames_TS26_15(t *testing.T) {
	if TierSimple != "SIMPLE" || TierStandard != "STANDARD" || TierAdvanced != "ADVANCED" {
		t.Errorf("tier constants = %q, %q, %q", TierSimple, TierStandard, TierAdvanced)
	}
	for _, tc := range []struct {
		in   string
		want Tier
	}{
		{" standard ", TierStandard},
		{"Advanced", TierAdvanced},
		{"simple", TierSimple},
	} {
		sel, err := Select("ollama", tc.in, "")
		if err != nil || sel.Tier != tc.want {
			t.Errorf("Select(ollama, %q) = tier %q, %v; want %q", tc.in, sel.Tier, err, tc.want)
		}
	}
	sel, err := Select("ollama", "STANDARD-1", "")
	if err != nil || sel.Tier != "" || sel.Model.ID != "STANDARD-1" || sel.Listed {
		t.Errorf("Select(ollama, STANDARD-1) = %+v, %v; want the unlisted model id STANDARD-1 with no tier", sel, err)
	}
}

// TS-26-16: the embedded tier table resolves to the documented model and effort
// for every provider and tier.
func TestSelectEmbeddedTierTable_TS26_16(t *testing.T) {
	for _, tc := range []struct {
		provider string
		tier     Tier
		model    string
		effort   core.Effort
	}{
		{"anthropic", TierSimple, "claude-sonnet-5-5", core.EffortMedium},
		{"anthropic", TierStandard, "claude-sonnet-5-5", core.EffortHigh},
		{"anthropic", TierAdvanced, "claude-opus-5-5", core.EffortXHigh},
		{"openrouter", TierSimple, "qwen/qwen3.8-flash", core.EffortLow},
		{"openrouter", TierStandard, "qwen/qwen3.8-flash", core.EffortMedium},
		{"openrouter", TierAdvanced, "qwen/qwen3.8-flash", core.EffortHigh},
		{"ollama", TierSimple, "qwen3.8:27b", core.EffortLow},
		{"ollama", TierStandard, "qwen3.8:27b", core.EffortMedium},
		{"ollama", TierAdvanced, "qwen3.8:27b", core.EffortHigh},
	} {
		sel, err := Select(tc.provider, string(tc.tier), "")
		if err != nil {
			t.Errorf("Select(%s, %s): %v", tc.provider, tc.tier, err)
			continue
		}
		if sel.Model.ID != tc.model || sel.Effort != tc.effort || !sel.Listed || sel.Tier != tc.tier || sel.Provider != tc.provider {
			t.Errorf("Select(%s, %s) = %s %q %q listed=%v, want %q at %q listed",
				tc.provider, tc.tier, sel.Provider, sel.Model.ID, sel.Effort, sel.Listed, tc.model, tc.effort)
		}
	}
	sel, err := Select("", "STANDARD", "")
	if err != nil || sel.Model.ID != "claude-sonnet-5-5" || sel.Effort != core.EffortHigh || !sel.Listed {
		t.Errorf("Select(\"\", STANDARD) = %q %q listed=%v, %v; want claude-sonnet-5-5 at high", sel.Model.ID, sel.Effort, sel.Listed, err)
	}
}

// testCatalogWithTiers is the test catalog with a valid tiers block on its
// openrouter vendor. The vendor's rows carry no level map, so two of them get
// one; the tiers name ids with slashes of their own.
func testCatalogWithTiers(t *testing.T) *Catalog {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(testCatalogJSON), &doc); err != nil {
		t.Fatal(err)
	}
	vendor := doc["vendors"].(map[string]any)["openrouter"].(map[string]any)
	rows := vendor["models"].(map[string]any)
	for _, id := range []string{"anthropic/claude-opus-4-5", "deepseek-ai/DeepSeek-V3"} {
		row := rows[id].(map[string]any)
		row["reasoning"] = true
		row["thinking_level_map"] = map[string]any{"low": "low", "medium": "medium", "high": "high"}
	}
	pick := func(model, effort string) map[string]any { return map[string]any{"model": model, "effort": effort} }
	vendor["tiers"] = map[string]any{
		"SIMPLE":   pick("deepseek-ai/DeepSeek-V3", "low"),
		"STANDARD": pick("anthropic/claude-opus-4-5", "medium"),
		"ADVANCED": pick("anthropic/claude-opus-4-5", "high"),
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("the test catalog with tiers does not parse: %v", err)
	}
	return c
}

// TS-26-17: a vendor without a tiers block loads, and a valid tiers block loads
// and selects, at the same schema version.
func TestParseLoadsTiersBlocks_TS26_17(t *testing.T) {
	if _, err := Parse([]byte(testCatalogJSON)); err != nil {
		t.Fatalf("the test catalog without tiers does not parse: %v", err)
	}
	c := testCatalogWithTiers(t)
	for _, tc := range []struct {
		tier   Tier
		model  string
		effort core.Effort
	}{
		{TierSimple, "deepseek-ai/DeepSeek-V3", core.EffortLow},
		{TierStandard, "anthropic/claude-opus-4-5", core.EffortMedium},
		{TierAdvanced, "anthropic/claude-opus-4-5", core.EffortHigh},
	} {
		sel, err := c.Select("openrouter", string(tc.tier), "")
		if err != nil {
			t.Errorf("Select(openrouter, %s): %v", tc.tier, err)
			continue
		}
		if sel.Model.ID != tc.model || sel.Effort != tc.effort || !sel.Listed || sel.Model.ContextWindow == 0 {
			t.Errorf("Select(openrouter, %s) = %q at %q listed=%v window=%d, want %q at %q listed",
				tc.tier, sel.Model.ID, sel.Effort, sel.Listed, sel.Model.ContextWindow, tc.model, tc.effort)
		}
	}
	// The other vendors of the catalog still have none.
	if _, err := c.Select("anthropic", "STANDARD", ""); err == nil {
		t.Error("Select(anthropic, STANDARD) succeeded on a vendor without tiers")
	}
}

// TS-26-25: a tier resolves to the tier's effort, unless an explicit effort
// overrides it.
func TestSelectTierEffortPrecedence_TS26_25(t *testing.T) {
	sel, err := Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	m := sel.Model
	if sel.Provider != "openrouter" || sel.Tier != TierStandard || m.ID != "qwen/qwen3.8-flash" ||
		m.ContextWindow != 1000000 || m.MaxOutputTokens != 131072 ||
		m.InputCostPerMillion != 0.15 || m.OutputCostPerMillion != 0.47 || m.CacheReadCostPerMillion != 0.016 ||
		sel.Effort != core.EffortMedium || !sel.Listed {
		t.Errorf("Select(openrouter, STANDARD) = %+v", sel)
	}

	sel, err = Select("ollama", "simple", "HIGH")
	if err != nil || sel.Tier != TierSimple || sel.Model.ID != "qwen3.8:27b" || sel.Effort != core.EffortHigh || !sel.Listed {
		t.Errorf("Select(ollama, simple, HIGH) = %+v, %v; want SIMPLE on qwen3.8:27b at high, not the tier's low", sel, err)
	}

	// The effort is checked before the tier is resolved.
	if _, err := Select("ollama", "STANDARD", "turbo"); !errors.Is(err, ErrUnknownEffort) {
		t.Errorf("Select(ollama, STANDARD, turbo) error = %v, want ErrUnknownEffort", err)
	}
}

// TS-26-26: naming a tier on a provider whose section a parsed catalog lacks,
// or that has no tiers block, fails with an error naming both.
func TestSelectTierWithoutATiersBlock_TS26_26(t *testing.T) {
	c := testCatalog(t) // openrouter has no tiers block, ollama has no section
	for _, tc := range []struct{ provider, model, wantProvider, wantTier string }{
		{"openrouter", "standard", "openrouter", "STANDARD"},
		{"ollama", "ADVANCED", "ollama", "ADVANCED"},
		{"", "simple", "anthropic", "SIMPLE"},
	} {
		sel, err := c.Select(tc.provider, tc.model, "")
		if !zeroSelection(sel) || err == nil {
			t.Errorf("Select(%q, %q) = %+v, %v; want the zero Selection and an error", tc.provider, tc.model, sel, err)
			continue
		}
		if !strings.HasPrefix(err.Error(), "agentkit:") {
			t.Errorf("message %q does not start with agentkit:", err)
		}
		wantMessageContains(t, err.Error(), tc.wantProvider, tc.wantTier)
	}
}

// TS-26-27: a model id has no tier, and the requested effort is reported as
// asked, without clamping it against the row.
func TestSelectReportsTheRequestedEffort_TS26_27(t *testing.T) {
	sel, err := Select("ollama", "qwen3.8:27b", "xhigh")
	if err != nil || sel.Tier != "" || sel.Effort != core.EffortXHigh {
		t.Errorf("Select(ollama, qwen3.8:27b, xhigh) = tier %q effort %q, %v; want no tier and xhigh", sel.Tier, sel.Effort, err)
	}
	if _, has := sel.Model.Efforts[core.EffortXHigh]; has {
		t.Error("the ollama row lists xhigh; the test needs a row that does not take it")
	}
	if sel, _ := Select("ollama", "qwen3.8:27b", ""); sel.Effort != "" {
		t.Errorf("Effort = %q with none asked, want empty", sel.Effort)
	}
	if sel, _ := Select("openrouter", "vendor/x", " Low "); sel.Effort != core.EffortLow || sel.Tier != "" {
		t.Errorf("Select(openrouter, vendor/x, \" Low \") = tier %q effort %q, want no tier and low", sel.Tier, sel.Effort)
	}
}
