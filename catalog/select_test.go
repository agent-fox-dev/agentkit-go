package catalog

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

func TestProvidersAreTheThreeNamesInOrderAsAFreshSlice(t *testing.T) {
	want := []string{"anthropic", "openrouter", "ollama"}
	a := Providers()
	if !slices.Equal(a, want) {
		t.Fatalf("Providers() = %v, want %v", a, want)
	}
	a[0] = "x"
	if got := Providers(); !slices.Equal(got, want) {
		t.Errorf("Providers() = %v after the first result was mutated; the slice is shared", got)
	}
}

func TestSelectCanonicalizesTheProviderAndRefusesAnUnknownOne(t *testing.T) {
	for in, want := range map[string]string{" OpenRouter ": "openrouter", "OLLAMA": "ollama", "": "anthropic", "   ": "anthropic"} {
		if sel, err := Select(in, "STANDARD", ""); err != nil || sel.Provider != want {
			t.Errorf("Select(%q, STANDARD) = %q, %v; want provider %q", in, sel.Provider, err, want)
		}
	}
	sel, err := Select("openai", "STANDARD", "")
	if !zeroSelection(sel) || err == nil ||
		err.Error() != `agentkit: unknown provider "openai"; known providers are anthropic, openrouter and ollama` {
		t.Fatalf("Select(openai) = %+v, %v", sel, err)
	}
	_, err = Select(" Azure ", "x", "")
	wantError(t, err, ErrUnknownProvider, `" Azure "`)
	// The provider set is fixed in code: a catalog vendor that is not a provider is not selectable.
	for _, p := range []string{"azure", "openai"} {
		sel, err := testCatalog(t).Select(p, "gpt-4o", "")
		if !zeroSelection(sel) || !errors.Is(err, ErrUnknownProvider) {
			t.Errorf("Select(%q) = %+v, %v; want ErrUnknownProvider", p, sel, err)
		}
	}
}

func TestSelectResolvesAModelIDAmongItsOwnProvidersRowsOnly(t *testing.T) {
	for _, tc := range []struct {
		provider, model, id string
		listed              bool
		window, maxTokens   int
	}{
		{"ollama", "qwen3.8:27b", "qwen3.8:27b", true, 32768, 32768},
		{"openrouter", " qwen/qwen3.8-flash ", "qwen/qwen3.8-flash", true, 1000000, 131072},
		{"anthropic", "anthropic/claude-sonnet-5-5", "claude-sonnet-5-5", true, 1000000, 128000},
		// An id is trimmed, never case-folded.
		{"openrouter", "  Qwen/X  ", "Qwen/X", false, 131072, 32768},
		{"ollama", "QWEN3.8:27B", "QWEN3.8:27B", false, 32768, 32768},
		// A row of one provider is never returned for another.
		{"openrouter", "anthropic/claude-sonnet-5-5", "anthropic/claude-sonnet-5-5", false, 131072, 32768},
		{"ollama", "qwen/qwen3.8-flash", "qwen/qwen3.8-flash", false, 32768, 32768},
		{"openrouter", "qwen3.8:27b", "qwen3.8:27b", false, 131072, 32768},
		{"anthropic", "qwen/qwen3.8-flash", "qwen/qwen3.8-flash", false, 1_000_000, 128_000},
		// An unlisted id gets its provider's fallback.
		{"ollama", "llama4:8b", "llama4:8b", false, 32768, 32768},
		{"anthropic", "claude-future-9", "claude-future-9", false, 1_000_000, 128_000},
		{"ollama", "STANDARD-1", "STANDARD-1", false, 32768, 32768},
	} {
		sel, err := Select(tc.provider, tc.model, "")
		m := sel.Model
		if err != nil || sel.Listed != tc.listed || sel.Tier != "" || m.ID != tc.id || m.ContextWindow != tc.window || m.MaxOutputTokens != tc.maxTokens {
			t.Errorf("Select(%q, %q) = %q %d/%d listed=%v tier=%q, %v; want %q %d/%d listed=%v",
				tc.provider, tc.model, m.ID, m.ContextWindow, m.MaxOutputTokens, sel.Listed, sel.Tier, err,
				tc.id, tc.window, tc.maxTokens, tc.listed)
		}
		if !tc.listed && (m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 || m.CacheReadCostPerMillion != 0 ||
			m.CacheWriteCostPerMillion != 0 || m.ThinkingKind != core.ThinkingKindAdaptive || m.Efforts != nil) {
			t.Errorf("Select(%q, %q) fallback = %+v, want no price, adaptive thinking and no efforts", tc.provider, tc.model, m)
		}
	}
	// On anthropic an id goes through Lookup.
	for _, id := range []string{"anthropic/claude-sonnet-5-5", "claude-opus-5-5", "claude-future-9"} {
		sel, _ := Select("anthropic", id, "")
		if want, ok := Lookup(id); !reflect.DeepEqual(sel.Model, want) || sel.Listed != ok {
			t.Errorf("Select(anthropic, %q) = %+v, want Lookup's %+v", id, sel.Model, want)
		}
	}
	// A provider whose section the catalog lacks still selects, with its fallback.
	if sel, err := testCatalog(t).Select("ollama", "llama4:8b", ""); err != nil || sel.Listed || sel.Model.ContextWindow != 32768 {
		t.Errorf("Select(ollama) without an ollama section = %+v, %v", sel, err)
	}
}

func TestSelectNormalizesTheEffortAndReportsItAsAsked(t *testing.T) {
	for in, want := range map[core.Effort]core.Effort{" XHigh ": core.EffortXHigh, "MAX": core.EffortMax, "Low": core.EffortLow, "": "", "  ": ""} {
		if sel, err := Select("anthropic", "claude-sonnet-5-5", in); err != nil || sel.Effort != want {
			t.Errorf("Select(effort %q) = %q, %v; want %q", in, sel.Effort, err, want)
		}
	}
	// Not clamped against the row: the ollama row does not take xhigh.
	sel, err := Select("ollama", "qwen3.8:27b", "xhigh")
	if _, has := sel.Model.Efforts[core.EffortXHigh]; err != nil || sel.Effort != core.EffortXHigh || has {
		t.Errorf("Select(ollama, qwen3.8:27b, xhigh) = %q (row takes xhigh: %v), %v", sel.Effort, has, err)
	}
	sel, err = Select("anthropic", "claude-sonnet-5-5", "turbo")
	if !zeroSelection(sel) {
		t.Errorf("Selection = %+v, want the zero value", sel)
	}
	wantError(t, err, ErrUnknownEffort, "agentkit:", `"turbo"`, "low, medium, high, xhigh and max")
}

func TestSelectRefusesAnEmptyModel(t *testing.T) {
	sel, err := Select("anthropic", "   ", "")
	if !zeroSelection(sel) {
		t.Errorf("Selection = %+v, want the zero value", sel)
	}
	wantError(t, err, ErrNoModel, "agentkit:", "SIMPLE, STANDARD or ADVANCED", "model id")
	sentinels := []error{ErrUnknownProvider, ErrUnknownEffort, ErrNoModel, ErrCorruptCatalog}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j && errors.Is(a, b) {
				t.Errorf("errors.Is(%q, %q) = true, want the sentinels distinct", a, b)
			}
		}
	}
}

func TestSelectChecksProviderThenModelThenEffort(t *testing.T) {
	if _, err := Select("openai", "", "bogus"); !errors.Is(err, ErrUnknownProvider) || errors.Is(err, ErrNoModel) {
		t.Errorf("Select(openai, \"\", bogus) = %v, want only ErrUnknownProvider", err)
	}
	if _, err := Select("anthropic", "", "bogus"); !errors.Is(err, ErrNoModel) || errors.Is(err, ErrUnknownEffort) {
		t.Errorf("Select(anthropic, \"\", bogus) = %v, want only ErrNoModel", err)
	}
	if _, err := Select("ollama", "STANDARD", "turbo"); !errors.Is(err, ErrUnknownEffort) {
		t.Errorf("Select(ollama, STANDARD, turbo) = %v, want ErrUnknownEffort before the tier is resolved", err)
	}
	for _, tr := range [][3]string{{"anthropic", "claude-sonnet-5-5", "high"}, {" OLLAMA ", "llama4:8b", " Low "}, {"openai", "x", ""}} {
		got, gotErr := Select(tr[0], tr[1], core.Effort(tr[2]))
		want, wantErr := Default().Select(tr[0], tr[1], core.Effort(tr[2]))
		if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(gotErr, wantErr) {
			t.Errorf("Select%q = %+v, %v; Default().Select gave %+v, %v", tr, got, gotErr, want, wantErr)
		}
	}
}

func TestSelectResolvesATierFromTheEmbeddedTable(t *testing.T) {
	for _, tc := range []struct {
		provider, tier, model string
		want                  Tier
		effort                core.Effort
	}{
		{"anthropic", "SIMPLE", "claude-sonnet-5-5", TierSimple, core.EffortMedium},
		{"anthropic", "STANDARD", "claude-sonnet-5-5", TierStandard, core.EffortHigh},
		{"", "standard", "claude-sonnet-5-5", TierStandard, core.EffortHigh},
		{"anthropic", "ADVANCED", "claude-opus-5-5", TierAdvanced, core.EffortXHigh},
		{"openrouter", "SIMPLE", "qwen/qwen3.8-flash", TierSimple, core.EffortLow},
		{"openrouter", "STANDARD", "qwen/qwen3.8-flash", TierStandard, core.EffortMedium},
		{"openrouter", " Advanced ", "qwen/qwen3.8-flash", TierAdvanced, core.EffortHigh},
		{"ollama", "simple", "qwen3.8:27b", TierSimple, core.EffortLow},
		{"ollama", "STANDARD", "qwen3.8:27b", TierStandard, core.EffortMedium},
		{"ollama", "ADVANCED", "qwen3.8:27b", TierAdvanced, core.EffortHigh},
	} {
		sel, err := Select(tc.provider, tc.tier, "")
		if err != nil || sel.Model.ID != tc.model || sel.Effort != tc.effort || !sel.Listed || sel.Tier != tc.want {
			t.Errorf("Select(%q, %q) = %q %q tier %q listed=%v, %v; want %q at %q", tc.provider, tc.tier,
				sel.Model.ID, sel.Effort, sel.Tier, sel.Listed, err, tc.model, tc.effort)
		}
	}
	// An explicit effort overrides the tier's.
	if sel, err := Select("ollama", "simple", "HIGH"); err != nil || sel.Tier != TierSimple || sel.Effort != core.EffortHigh {
		t.Errorf("Select(ollama, simple, HIGH) = %+v, %v; want SIMPLE at high", sel, err)
	}
}

func TestSelectResolvesATierOfAParsedCatalogAndRefusesAProviderWithoutTiers(t *testing.T) {
	c := testCatalogWithTiers(t)
	for tier, want := range map[string]string{"SIMPLE": "deepseek-ai/DeepSeek-V3", "STANDARD": "anthropic/claude-opus-4-5"} {
		if sel, err := c.Select("openrouter", tier, ""); err != nil || sel.Model.ID != want || !sel.Listed || sel.Model.ContextWindow == 0 {
			t.Errorf("Select(openrouter, %s) = %+v, %v; want the %s row", tier, sel, err, want)
		}
	}
	for _, tc := range []struct{ provider, model, wantProvider, wantTier string }{
		{"anthropic", "STANDARD", "anthropic", "STANDARD"}, // a vendor without a tiers block
		{"ollama", "ADVANCED", "ollama", "ADVANCED"},       // no vendor section at all
		{"", "simple", "anthropic", "SIMPLE"},
	} {
		sel, err := c.Select(tc.provider, tc.model, "")
		if !zeroSelection(sel) || err == nil {
			t.Errorf("Select(%q, %q) = %+v, %v; want the zero Selection and an error", tc.provider, tc.model, sel, err)
			continue
		}
		for _, part := range []string{"agentkit:", `"` + tc.wantProvider + `"`, tc.wantTier} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("Select(%q, %q) error %q lacks %q", tc.provider, tc.model, err, part)
			}
		}
	}
}

func TestSelectReturnsADeepCopy(t *testing.T) {
	for _, tc := range [][3]string{
		{"openrouter", "STANDARD", ""}, {"anthropic", "ADVANCED", ""}, {"anthropic", "claude-fable-5-1", ""},
		{"ollama", "qwen3.8:27b", "high"}, {"openrouter", "vendor/unlisted", ""},
	} {
		first, err := Select(tc[0], tc[1], core.Effort(tc[2]))
		if err != nil {
			t.Fatalf("Select%q: %v", tc, err)
		}
		pristine, _ := json.Marshal(first)
		for k, w := range first.Model.Efforts {
			if w != nil {
				*w = "mutated"
			}
			first.Model.Efforts[k] = nil
		}
		if len(first.Model.Compat) > 0 {
			first.Model.Compat[0] ^= 0xff
		}
		again, _ := Select(tc[0], tc[1], core.Effort(tc[2]))
		if got, _ := json.Marshal(again); string(got) != string(pristine) {
			t.Errorf("Select%q changed after its first result was mutated:\n got %s\nwant %s", tc, got, pristine)
		}
	}
}

// Run under -race: Select shares only the immutable catalog between calls.
func TestSelectIsSafeForConcurrentUse(t *testing.T) {
	triples := [][3]string{{"anthropic", "STANDARD", ""}, {"openrouter", "advanced", "low"}, {"ollama", "llama4:8b", "HIGH"},
		{"", "claude-future-9", ""}, {"openai", "x", ""}, {"anthropic", "x", "turbo"}}
	want := make([]Selection, len(triples))
	for i, tr := range triples {
		want[i], _ = Select(tr[0], tr[1], core.Effort(tr[2]))
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for i, tr := range triples {
				sel, _ := Select(tr[0], tr[1], core.Effort(tr[2]))
				if !reflect.DeepEqual(sel, want[i]) {
					t.Errorf("Select%q = %+v concurrently, %+v sequentially", tr, sel, want[i])
				}
				for k := range sel.Model.Efforts {
					sel.Model.Efforts[k] = nil
				}
			}
		})
	}
	wg.Wait()
}

func TestSelectIgnoresTheEnvironment(t *testing.T) {
	keys := []string{"OPENROUTER_API_KEY", "OLLAMA_HOST", "ANTHROPIC_API_KEY"}
	run := func() []Selection {
		var out []Selection
		for _, tr := range [][2]string{{"openrouter", "STANDARD"}, {"ollama", "qwen3.8:27b"}, {"ollama", "llama4:8b"}} {
			sel, _ := Select(tr[0], tr[1], "")
			out = append(out, sel)
		}
		return out
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
	unset := run()
	for _, k := range keys {
		t.Setenv(k, "http://x:1")
	}
	if set := run(); !reflect.DeepEqual(unset, set) {
		t.Errorf("results differ with the variables set:\n unset %+v\n   set %+v", unset, set)
	}
}

// Over random ids: a listed result is always a row of the provider's own
// vendor, and Select never panics and answers an error or positive limits.
func TestSelectOverRandomInputsListsOnlyOwnRowsAndNeverPanics(t *testing.T) {
	c := Default()
	r := rand.New(rand.NewSource(13))
	var ids []string
	for _, v := range c.Vendors() {
		for _, id := range c.Models(v) {
			ids = append(ids, id, strings.ToUpper(id), " "+id+" ", "anthropic/"+id, "ollama/"+id)
		}
	}
	for range 300 {
		ids = append(ids, randomString(r, 30))
	}
	for _, p := range Providers() {
		for _, raw := range ids {
			id := strings.TrimSpace(raw)
			if _, isTier := tierOf(id); isTier || id == "" {
				continue
			}
			bare := id
			if p == ProviderAnthropic {
				bare = strings.TrimPrefix(id, ProviderAnthropic+"/")
			}
			sel, err := c.Select(p, raw, "")
			if err != nil || sel.Listed != slices.Contains(c.Models(p), bare) {
				t.Errorf("Select(%q, %q) listed=%v, %v; want listed only for a row of %s", p, shorten(raw), sel.Listed, err, p)
			}
			if row := c.byCanonical[p+"/"+bare]; sel.Listed && (row == nil || !reflect.DeepEqual(sel.Model, row.model)) {
				t.Errorf("Select(%q, %q) is listed but is not the %s row %q", p, shorten(raw), p, bare)
			}
		}
	}

	catalogs := []*Catalog{Default(), testCatalog(t), testCatalogWithTiers(t)}
	providers := []string{"", " ", "anthropic", "OpenRouter", " ollama ", "openai", "azure", "anthropic/"}
	models := []string{"standard", " ADVANCED ", "STANDARD-1", "", "  ", "anthropic/", "qwen3.8:27b",
		"deepseek-ai/DeepSeek-V3", strings.Repeat("x", 100_000)}
	efforts := []string{"", " ", "low", " XHigh ", "turbo", "minimal", "off", "高"}
	choose := func(fixed []string) string {
		if r.Intn(3) == 0 {
			return randomString(r, 40)
		}
		return fixed[r.Intn(len(fixed))]
	}
	for range 2000 {
		p, m, e := choose(providers), choose(models), choose(efforts)
		for _, c := range catalogs {
			sel, err := c.Select(p, m, core.Effort(e))
			if (err != nil && !zeroSelection(sel)) || (err == nil && (sel.Model.ContextWindow <= 0 ||
				sel.Model.MaxOutputTokens <= 0 || !slices.Contains(Providers(), sel.Provider))) {
				t.Fatalf("Select(%q, %q, %q) = %+v, %v", shorten(p), shorten(m), shorten(e), sel, err)
			}
		}
	}
}

func TestASelectedRowPricesAUsage(t *testing.T) {
	sel, err := Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatal(err)
	}
	if cost := provider.ComputeCost(&sel.Model, core.Usage{InputTokens: 1_000_000}); math.Abs(cost-0.15) > 1e-9 {
		t.Errorf("ComputeCost = %v USD for one million input tokens, want 0.15", cost)
	}
}

func TestTheEmbeddedCatalogWithAnEffortItsTierRowLacksIsRejected(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(embeddedCatalog, &doc); err != nil {
		t.Fatal(err)
	}
	doc["vendors"].(map[string]any)["ollama"].(map[string]any)["tiers"].(map[string]any)["STANDARD"].(map[string]any)["effort"] = "xhigh"
	edited, _ := json.Marshal(doc)
	c, err := Parse(edited)
	var pe *ParseError
	if c != nil || !errors.As(err, &pe) || pe.Path != "vendors.ollama.tiers.STANDARD.effort" {
		t.Fatalf("Parse(edited catalog.json) = %v, %v; want a ParseError at the STANDARD effort", c, err)
	}
}
