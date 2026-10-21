package catalog

import (
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
