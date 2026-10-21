package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// testCatalogJSON is a purpose-built catalog for the REQ-CAT-02 matching
// rules. It is deliberately NOT the shipped catalog: the rules it exercises
// (an ambiguous bare id, an OpenRouter-style row whose own id contains a
// slash) are configurations the shipped catalog must NOT have, and pinning
// them here lets the shipped catalog stay usable while the rules stay tested.
const testCatalogJSON = `{
  "schema_version": 1,
  "catalog_version": "test-1",
  "vendors": {
    "anthropic": {
      "api": "anthropic-messages",
      "base_url": "https://api.anthropic.com",
      "headers": {"anthropic-version": "2023-06-01"},
      "default_model": "claude-sonnet-4-5",
      "models": {
        "claude-opus-4-5": {"name": "Claude Opus 4.5", "context_window": 200000, "max_tokens": 64000,
          "cost": {"input": 5, "output": 25}, "input": ["text","image"], "reasoning": true,
          "thinking_level_map": {"off": "disabled", "minimal": null, "low": "low", "medium": "medium", "high": "high"}},
        "claude-sonnet-4-5": {"name": "Claude Sonnet 4.5", "context_window": 200000, "max_tokens": 64000,
          "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
          "compat": {"beta_headers": ["fine-grained-tool-streaming"]},
          "input": ["text","image"], "reasoning": true,
          "thinking_level_map": {"off": "disabled", "low": "4096", "medium": "16384", "high": "32768"}}
      }
    },
    "openai": {
      "api": "openai-completions", "base_url": "https://api.openai.com/v1", "default_model": "gpt-4o",
      "models": {
        "gpt-4o": {"context_window": 128000, "max_tokens": 16384, "cost": {"input": 2.5, "output": 10}}
      }
    },
    "azure": {
      "api": "openai-completions", "base_url": "https://example.openai.azure.com", "default_model": "gpt-4o",
      "models": {
        "gpt-4o": {"context_window": 128000, "max_tokens": 16384, "cost": {"input": 2.5, "output": 10}}
      }
    },
    "openrouter": {
      "api": "openai-completions", "base_url": "https://openrouter.ai/api/v1",
      "default_model": "anthropic/claude-opus-4-5",
      "models": {
        "anthropic/claude-opus-4-5": {"context_window": 200000, "max_tokens": 64000, "cost": {"input": 5, "output": 25}},
        "deepseek-ai/DeepSeek-V3": {"context_window": 64000, "max_tokens": 8192, "cost": {"input": 0.3, "output": 0.9}},
        "openai/o4-preview": {"context_window": 200000, "max_tokens": 100000, "cost": {"input": 2, "output": 8}}
      }
    }
  }
}`

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Parse([]byte(testCatalogJSON))
	if err != nil {
		t.Fatalf("test catalog does not parse: %v", err)
	}
	return c
}

func TestEmbeddedCatalogPopulatesTheREQPROV10Descriptor(t *testing.T) {
	c := Default()

	haiku, ok := c.Lookup("anthropic/claude-haiku-4-5")
	if !ok {
		t.Fatal("claude-haiku-4-5 is not in the catalog")
	}
	if haiku.ContextWindow != 200000 || haiku.MaxOutputTokens != 64000 {
		t.Errorf("window/max = %d/%d, want 200000/64000", haiku.ContextWindow, haiku.MaxOutputTokens)
	}
	if haiku.InputCostPerMillion != 1 || haiku.OutputCostPerMillion != 5 {
		t.Errorf("cost = %v/%v per 1M, want 1/5", haiku.InputCostPerMillion, haiku.OutputCostPerMillion)
	}
	if haiku.ThinkingKind != core.ThinkingKindBudget {
		t.Errorf("ThinkingKind = %q; a row whose levels are token counts takes a budget", haiku.ThinkingKind)
	}
	if w := haiku.Efforts[core.EffortLow]; w == nil || *w == "" {
		t.Errorf("Efforts[low] = %v, want the row's budget", w)
	}
	if opus, _ := c.Lookup("claude-opus-5-5"); opus.ThinkingKind != core.ThinkingKindAdaptive {
		t.Errorf("claude-opus-5-5 ThinkingKind = %q; a row whose levels are efforts is adaptive", opus.ThinkingKind)
	}
}

// TestCorruptCatalogPanicsOnFirstUseNotAtInit pins ruling P-15, which
// reconciles REQ-CAT-01 ("panics at init") with NFR-SEC-05 ("no init() global
// mutation"): building the accessor must be silent, the FIRST USE must panic,
// and every later use must panic with the same value. A wrong implementation
// that panics in init() cannot be tested at all, and one that parses eagerly
// per call would produce a fresh (non-identical) panic value each time.
func TestCorruptCatalogPanicsOnFirstUseNotAtInit(t *testing.T) {
	corrupt := []byte(`{"schema_version": 1, "vendors": {`)

	get := onceCatalog(corrupt) // must not panic: this is the "init" moment

	first := recoverPanic(t, get)
	if first == nil {
		t.Fatal("first use of a corrupt catalog did not panic; REQ-CAT-01 forbids a silently empty catalog")
	}
	msg, ok := first.(string)
	if !ok || !strings.Contains(msg, "corrupt") {
		t.Fatalf("panic value = %#v, want a string naming the corruption", first)
	}

	second := recoverPanic(t, get)
	if second != first {
		t.Fatalf("second use panicked with %#v, want the identical value %#v; sync.OnceValue must\n"+
			"re-panic with the stored value, not re-parse", second, first)
	}
}

func TestGoodCatalogNeverPanics(t *testing.T) {
	get := onceCatalog([]byte(testCatalogJSON))
	if p := recoverPanic(t, func() *Catalog { return get() }); p != nil {
		t.Fatalf("valid catalog panicked: %v", p)
	}
	if got := get().Version(); got != "test-1" {
		t.Errorf("Version() = %q, want test-1", got)
	}
	// Parsed ONCE, not once per call: the accessor memoizes the value, which
	// is the same property that makes the corrupt case re-panic with the
	// stored value rather than re-deriving one (P-15).
	if get() != get() { //nolint:staticcheck // intentional: testing memoization
		t.Error("the catalog was re-parsed; onceCatalog must memoize")
	}
	if Default() != Default() { //nolint:staticcheck // intentional: testing memoization
		t.Error("Default() re-parses the embedded catalog on every call")
	}
}

func recoverPanic(t *testing.T, f func() *Catalog) (p any) {
	t.Helper()
	defer func() { p = recover() }()
	f()
	return nil
}

func TestParseRejectsCorruptCatalogs(t *testing.T) {
	// Each row is one way a hand-edited catalog goes wrong during the
	// REQ-CAT-06 regeneration. Every one of them would otherwise produce a
	// catalog that loads and lies.
	tests := []struct {
		name string
		json string
		want string // substring of the error
	}{
		{"not json", `{`, "unexpected end"},
		{"future schema version", `{"schema_version":2,"vendors":{}}`, "this build understands 1"},
		{"no vendors", `{"schema_version":1,"vendors":{}}`, "declares no vendors"},
		{"vendor with no models", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m","models":{}}}}`, "declares no models"},
		{"missing default_model", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","models":{"m":{}}}}}`, "default_model"},
		{"dangling default_model", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"nope","models":{"m":{}}}}}`, "not a model of this vendor"},
		{"vendor id with slash", `{"schema_version":1,"vendors":{"a/b":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{}}}}}`, "may not contain a slash"},
		{"row id disagrees with key", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{"id":"other"}}}}}`, "keyed"},
		{"no api anywhere", `{"schema_version":1,"vendors":{"v":{"base_url":"x","default_model":"m","models":{"m":{}}}}}`, "no default api"},
		{"no base_url anywhere", `{"schema_version":1,"vendors":{"v":{"api":"faux","default_model":"m","models":{"m":{}}}}}`, "no default base_url"},
		{"negative price", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{"cost":{"input":-1}}}}}}`, "cost.input"},
		{"negative window", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{"context_window":-1}}}}}`, "context_window"},
		{"misspelled thinking level", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{"reasoning":true,"thinking_level_map":{"higth":"high"}}}}}}`, "unknown thinking level"},
		{"thinking without reasoning", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{"thinking_level_map":{"high":"high"}}}}}}`, "reasoning"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.json))
			if err == nil {
				t.Fatalf("Parse accepted a corrupt catalog")
			}
			if !errors.Is(err, ErrCorruptCatalog) {
				t.Errorf("error does not match ErrCorruptCatalog: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// tierDocFmt is a two-vendor catalog document with a %s for the ollama
// vendor's tiers object. The ollama rows are the three a tier can name
// (qwen3.8:27b takes low, medium, high and max but not xhigh; nullhigh lists
// high as present-and-null; nomap has no thinking_level_map at all), and the
// "other" vendor holds a row the ollama vendor does not.
const tierDocFmt = `{"schema_version":1,"catalog_version":"t","vendors":{
  "ollama":{"api":"ollama-chat","base_url":"http://127.0.0.1:11434","default_model":"qwen3.8:27b",
    "models":{
      "qwen3.8:27b":{"context_window":32768,"max_tokens":32768,"reasoning":true,
        "thinking_level_map":{"low":"low","medium":"medium","high":"high","max":"max"}},
      "nullhigh":{"context_window":32768,"max_tokens":32768,"reasoning":true,
        "thinking_level_map":{"low":"low","high":null}},
      "nomap":{"context_window":32768,"max_tokens":32768}
    },
    "tiers":%s},
  "other":{"api":"ollama-chat","base_url":"http://127.0.0.1:11434","default_model":"other-model",
    "models":{"other-model":{"context_window":8192,"max_tokens":4096,"reasoning":true,
      "thinking_level_map":{"low":"low","medium":"medium","high":"high"}}}}
}}`

// tierPick is one tier's entry as the catalog file spells it.
func tierPick(model, effort string) string {
	return fmt.Sprintf(`{"model":%q,"effort":%q}`, model, effort)
}

// tiersObject spells a tiers object from its three entries.
func tiersObject(simple, standard, advanced string) string {
	return fmt.Sprintf(`{"SIMPLE":%s,"STANDARD":%s,"ADVANCED":%s}`, simple, standard, advanced)
}

// validTiers is a tiers object that Parse accepts against tierDocFmt.
func validTiers() string {
	return tiersObject(tierPick("qwen3.8:27b", "low"), tierPick("qwen3.8:27b", "medium"), tierPick("qwen3.8:27b", "high"))
}

// wantTierParseError fails the test unless Parse rejects doc with a
// *ParseError at path that matches ErrCorruptCatalog and returns no catalog.
func wantTierParseError(t *testing.T, doc, path string) {
	t.Helper()
	c, err := Parse([]byte(doc))
	if c != nil {
		t.Error("Parse returned a catalog for a corrupt tiers block")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want a *ParseError", err)
	}
	if pe.Path != path {
		t.Errorf("Path = %q, want %q (reason: %s)", pe.Path, path, pe.Reason)
	}
	if !errors.Is(err, ErrCorruptCatalog) {
		t.Errorf("error does not match ErrCorruptCatalog: %v", err)
	}
}

// TestTierDocParses pins the base the tier rejection tests vary: it is valid,
// so each rejection below is caused by the one block it changes.
func TestTierDocParses(t *testing.T) {
	c, err := Parse([]byte(fmt.Sprintf(tierDocFmt, validTiers())))
	if err != nil {
		t.Fatalf("the base document does not parse: %v", err)
	}
	if got := c.Models("ollama"); len(got) != 3 {
		t.Errorf("Models(ollama) = %v, want the three rows", got)
	}
	// A vendor without a tiers key loads as well (26-REQ-4.6).
	if _, err := Parse([]byte(fmt.Sprintf(tierDocFmt, "null"))); err != nil {
		t.Errorf("a null tiers object is a vendor without tiers, got %v", err)
	}
}

// TS-26-18: a tiers key that is not exactly a tier name, and a block that lacks
// one, are rejected at the key's path. An unknown key is reported before a
// missing tier, so a lower-case "standard" names the key and not the gap.
func TestParseRejectsTierKeys_TS26_18(t *testing.T) {
	std := tierPick("qwen3.8:27b", "medium")
	low := tierPick("qwen3.8:27b", "low")
	high := tierPick("qwen3.8:27b", "high")
	tests := []struct {
		name  string
		tiers string
		path  string
	}{
		{"lower-case key", `{"SIMPLE":` + low + `,"standard":` + std + `,"ADVANCED":` + high + `}`, "vendors.ollama.tiers.standard"},
		{"unknown key", `{"SIMPLE":` + low + `,"STANDARD":` + std + `,"ADVANCED":` + high + `,"EXPERT":` + high + `}`, "vendors.ollama.tiers.EXPERT"},
		{"missing ADVANCED", `{"SIMPLE":` + low + `,"STANDARD":` + std + `}`, "vendors.ollama.tiers.ADVANCED"},
		{"missing SIMPLE", `{"STANDARD":` + std + `,"ADVANCED":` + high + `}`, "vendors.ollama.tiers.SIMPLE"},
		{"empty object", `{}`, "vendors.ollama.tiers.SIMPLE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantTierParseError(t, fmt.Sprintf(tierDocFmt, tc.tiers), tc.path)
		})
	}
}

// TS-26-19: an empty tier model, a model the vendor has no row for and a row of
// another vendor are rejected at the tier's model.
func TestParseRejectsTierModels_TS26_19(t *testing.T) {
	low := tierPick("qwen3.8:27b", "low")
	high := tierPick("qwen3.8:27b", "high")
	tests := []struct {
		name     string
		standard string
	}{
		{"empty model", tierPick("", "medium")},
		{"no such row", tierPick("nope", "medium")},
		{"row of another vendor", tierPick("other-model", "medium")},
		{"null entry", `null`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := fmt.Sprintf(tierDocFmt, tiersObject(low, tc.standard, high))
			wantTierParseError(t, doc, "vendors.ollama.tiers.STANDARD.model")
		})
	}
}

// TS-26-20: an effort that is not exactly one of the five levels, and one the
// named row does not take (absent, null, or no thinking_level_map at all), are
// rejected at the tier's effort.
func TestParseRejectsTierEfforts_TS26_20(t *testing.T) {
	low := tierPick("qwen3.8:27b", "low")
	high := tierPick("qwen3.8:27b", "high")
	tests := []struct {
		name     string
		standard string
	}{
		{"wrong case", tierPick("qwen3.8:27b", "HIGH")},
		{"not an effort", tierPick("qwen3.8:27b", "bogus")},
		{"empty", tierPick("qwen3.8:27b", "")},
		{"a level, but not an effort", tierPick("qwen3.8:27b", "minimal")},
		{"absent in the row", tierPick("qwen3.8:27b", "xhigh")},
		{"null in the row", tierPick("nullhigh", "high")},
		{"row without a level map", tierPick("nomap", "low")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := fmt.Sprintf(tierDocFmt, tiersObject(low, tc.standard, high))
			wantTierParseError(t, doc, "vendors.ollama.tiers.STANDARD.effort")
		})
	}
}

// TestUnknownAPIStringLoads: a catalog row naming an API this build has never
// heard of must LOAD. Only an empty API is corrupt.
func TestUnknownAPIStringLoads(t *testing.T) {
	c, err := Parse([]byte(`{"schema_version":1,"vendors":{"acme":{"api":"acme-chat-v9",
		"base_url":"https://acme.example","default_model":"m","models":{"m":{}}}}}`))
	if err != nil {
		t.Fatalf("Parse rejected an unknown api string: %v", err)
	}
	if _, ok := c.byCanonical["acme/m"]; !ok {
		t.Error("the row did not load")
	}
}

// TestResolvedModelIsADeepCopy: the catalog is a process-wide singleton and
// REQ-CAT-03 invites callers to "override any inherited field". Without a deep
// copy, one caller's override silently rewrites every later resolution in the
// process.
func TestResolvedModelIsADeepCopy(t *testing.T) {
	c := testCatalog(t)

	first, ok := c.Lookup("anthropic/claude-sonnet-4-5")
	if !ok {
		t.Fatal("claude-sonnet-4-5 is not in the test catalog")
	}
	first.MaxOutputTokens = 1
	first.Compat[0] = ' '
	first.Efforts[core.EffortHigh] = nil
	first.InputCostPerMillion = 999

	second, _ := c.Lookup("anthropic/claude-sonnet-4-5")
	if second.MaxOutputTokens != 64000 {
		t.Errorf("MaxOutputTokens = %d, want 64000", second.MaxOutputTokens)
	}
	if !json.Valid(second.Compat) {
		t.Errorf("Compat was mutated through the shared backing array: %s", second.Compat)
	}
	if second.Efforts[core.EffortHigh] == nil {
		t.Error("Efforts was mutated through the shared map")
	}
	if second.InputCostPerMillion != 3 {
		t.Errorf("InputCostPerMillion = %v, want 3", second.InputCostPerMillion)
	}
}

// TestEveryShippedRowLooksUp: every row the catalog ships is found by its
// bare id.
func TestEveryShippedRowLooksUp(t *testing.T) {
	c := Default()
	for _, id := range c.Models(Vendor) {
		if m, ok := c.Lookup(id); !ok || m.ID != id {
			t.Errorf("Lookup(%q) = %q, %v", id, m.ID, ok)
		}
	}
}

func TestCatalogAccessors(t *testing.T) {
	c := Default()
	if c.Version() == "" {
		t.Error("Version() is empty; REQ-CAT-01 versions the catalog separately from the SDK")
	}
	if !strings.Contains(c.Note(), "REQ-CAT-06") {
		t.Error("Note() does not carry the regeneration ritual")
	}
	if got := c.Vendors(); !slices.Equal(got, []string{"anthropic", "ollama", "openrouter"}) {
		t.Errorf("Vendors() = %v, want [anthropic ollama openrouter]", got)
	}
	for _, v := range []string{"anthropic", "ollama", "openrouter"} {
		if !c.KnownVendor(v) {
			t.Errorf("KnownVendor(%q) = false, want true", v)
		}
	}
	if c.KnownVendor("openai") {
		t.Error("KnownVendor(openai) = true; the catalog has no OpenAI section")
	}
	if c.DefaultModelID("anthropic") == "" {
		t.Error("anthropic has no default_model; REQ-CAT-03 has nothing to clone")
	}
	if c.DefaultModelID("nope") != "" || c.Models("nope") != nil {
		t.Error("accessors must report nothing for an unknown vendor")
	}
	// Vendors() must hand out a copy, not the index's own slice.
	vs := c.Vendors()
	vs[0] = "tampered"
	if c.Vendors()[0] != "anthropic" {
		t.Error("Vendors() aliases internal state")
	}
}

// otherVendorMarkers are strings that only an OpenAI or Google section of the
// catalog would contain.
var otherVendorMarkers = []string{"gpt-", "gemini", `"openai"`, `"google"`}

// TS-10-11, restated by 26-REQ-6: the only vendors are the three providers, the
// catalog holds no OpenAI or Google content, and the multi-vendor resolution
// and clamping files are gone.
func TestCatalogHasOnlyTheThreeProviders_TS10_11(t *testing.T) {
	raw := string(embeddedCatalog)
	if !strings.Contains(raw, "claude") {
		t.Fatal("the catalog has no Claude rows")
	}
	if got := Default().Vendors(); !slices.Equal(got, []string{"anthropic", "ollama", "openrouter"}) {
		t.Errorf("Vendors() = %v, want [anthropic ollama openrouter]", got)
	}
	for _, other := range otherVendorMarkers {
		if strings.Contains(raw, other) {
			t.Errorf("catalog.json mentions %s", other)
		}
	}
	for _, gone := range []string{"resolve.go", "clamp.go"} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("catalog/%s still exists", gone)
		}
	}
}

// TS-10-12: a known Claude id resolves, with or without the anthropic/
// prefix.
func TestLookupKnownModel_TS10_12(t *testing.T) {
	for _, id := range []string{"claude-sonnet-4-5", "anthropic/claude-sonnet-4-5"} {
		m, ok := Lookup(id)
		if !ok || m.ID != "claude-sonnet-4-5" {
			t.Fatalf("Lookup(%q) = %q, %v", id, m.ID, ok)
		}
		if m.ContextWindow == 0 || m.MaxOutputTokens == 0 || m.InputCostPerMillion == 0 {
			t.Fatalf("Lookup(%q) returned an empty row: %+v", id, m)
		}
	}
}

// TS-10-13: an uncataloged id gets a usable default and false.
func TestLookupUnknownModel_TS10_13(t *testing.T) {
	m, ok := Lookup("claude-next-gen-future")
	if ok {
		t.Fatal("an uncataloged model was reported as known")
	}
	if m.ID != "claude-next-gen-future" || m.ContextWindow != 1_000_000 || m.MaxOutputTokens != 128_000 ||
		m.ThinkingKind != core.ThinkingKindAdaptive {
		t.Fatalf("default = %+v", m)
	}
	if m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 || m.CacheReadCostPerMillion != 0 || m.CacheWriteCostPerMillion != 0 {
		t.Fatalf("default = %+v, want no price", m)
	}
}

// TS-10-14 (property): any id resolves to a usable descriptor and never
// panics.
func TestLookupAlwaysUsable_TS10_14(t *testing.T) {
	r := rand.New(rand.NewSource(10))
	alphabet := []rune("abcdefghijklmnopqrstuvwxyz0123456789-./@_ ÄΩ")
	ids := []string{"anthropic/", "x", "claude-opus-5-5", "anthropic/claude-opus-5-5", "openai/gpt-5", "@@@"}
	for i := 0; i < 500; i++ {
		n := 1 + r.Intn(40)
		var b strings.Builder
		for j := 0; j < n; j++ {
			b.WriteRune(alphabet[r.Intn(len(alphabet))])
		}
		ids = append(ids, b.String())
	}
	for _, id := range ids {
		m, _ := Lookup(id)
		if m.ID == "" || m.ContextWindow <= 0 || m.MaxOutputTokens <= 0 {
			t.Fatalf("Lookup(%q) = %+v, not usable", id, m)
		}
	}
}

// TS-26-1: the provider constants and Providers() return the three names in
// order, as a fresh slice on every call.
func TestProvidersAreTheThreeNamesInOrder_TS26_1(t *testing.T) {
	if ProviderAnthropic != "anthropic" || ProviderOpenRouter != "openrouter" || ProviderOllama != "ollama" {
		t.Errorf("constants = %q, %q, %q, want anthropic, openrouter, ollama",
			ProviderAnthropic, ProviderOpenRouter, ProviderOllama)
	}
	want := []string{"anthropic", "openrouter", "ollama"}
	a := Providers()
	if !slices.Equal(a, want) {
		t.Fatalf("Providers() = %v, want %v", a, want)
	}
	a[0] = "x"
	if got := Providers(); !slices.Equal(got, want) {
		t.Errorf("Providers() = %v after the first result was mutated, want %v; the slice is shared", got, want)
	}
}

// TS-10-15: no model id, price or token limit is written into the Anthropic
// provider's sources; they come from the catalog.
func TestNoModelMetadataInTheProvider_TS10_15(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "provider", "anthropic", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("provider/anthropic sources: %v", err)
	}
	modelID := regexp.MustCompile(`"claude-[a-z]+-[0-9]`)
	limit := regexp.MustCompile(`\b(200000|200_000|1000000|1_000_000|128000|128_000|64000|64_000)\b`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if loc := modelID.FindIndex(b); loc != nil {
			t.Errorf("%s names a model id: %s", f, b[loc[0]:loc[1]])
		}
		if loc := limit.FindIndex(b); loc != nil {
			t.Errorf("%s carries a model token limit: %s", f, b[loc[0]:loc[1]])
		}
		for _, price := range []string{"Cost{", "USDPer", "per1M", "Per1M"} {
			if strings.Contains(string(b), price) {
				t.Errorf("%s carries a price table (%s)", f, price)
			}
		}
	}
}

// anthropicSnapshot pins the 13 Anthropic rows as catalog_version 2026-09-30
// served them: each id, in sorted order, with the JSON of the core.Model that
// Lookup returned for it before 26-REQ-2 added the other providers. json.Marshal
// writes the Efforts map in key order and keeps a present-null level as null, so
// equal text is a deep-equal descriptor.
var anthropicSnapshot = []struct{ id, model string }{
	{"claude-fable-5", `{"ID":"claude-fable-5","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":10,"OutputCostPerMillion":50,"CacheReadCostPerMillion":1,"CacheWriteCostPerMillion":12.5,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-fable-5-1", `{"ID":"claude-fable-5-1","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":10,"OutputCostPerMillion":50,"CacheReadCostPerMillion":0.25,"CacheWriteCostPerMillion":12.5,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-haiku-4-5", `{"ID":"claude-haiku-4-5","ContextWindow":200000,"MaxOutputTokens":64000,"InputCostPerMillion":1,"OutputCostPerMillion":5,"CacheReadCostPerMillion":0.1,"CacheWriteCostPerMillion":1.25,"ThinkingKind":"budget","Efforts":{"high":"32768","low":"4096","medium":"16384"},"Compat":null}`},
	{"claude-opus-4-5", `{"ID":"claude-opus-4-5","ContextWindow":200000,"MaxOutputTokens":64000,"InputCostPerMillion":5,"OutputCostPerMillion":25,"CacheReadCostPerMillion":0.5,"CacheWriteCostPerMillion":6.25,"ThinkingKind":"budget","Efforts":{"high":"32768","low":"4096","medium":"16384"},"Compat":null}`},
	{"claude-opus-4-6", `{"ID":"claude-opus-4-6","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":5,"OutputCostPerMillion":25,"CacheReadCostPerMillion":0.5,"CacheWriteCostPerMillion":6.25,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium"},"Compat":null}`},
	{"claude-opus-4-7", `{"ID":"claude-opus-4-7","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":5,"OutputCostPerMillion":25,"CacheReadCostPerMillion":0.5,"CacheWriteCostPerMillion":6.25,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-opus-4-8", `{"ID":"claude-opus-4-8","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":5,"OutputCostPerMillion":25,"CacheReadCostPerMillion":0.5,"CacheWriteCostPerMillion":6.25,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-opus-5", `{"ID":"claude-opus-5","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":5,"OutputCostPerMillion":25,"CacheReadCostPerMillion":0.5,"CacheWriteCostPerMillion":6.25,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-opus-5-5", `{"ID":"claude-opus-5-5","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":4,"OutputCostPerMillion":20,"CacheReadCostPerMillion":0.2,"CacheWriteCostPerMillion":5,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-sonnet-4-5", `{"ID":"claude-sonnet-4-5","ContextWindow":200000,"MaxOutputTokens":64000,"InputCostPerMillion":3,"OutputCostPerMillion":15,"CacheReadCostPerMillion":0.3,"CacheWriteCostPerMillion":3.75,"ThinkingKind":"budget","Efforts":{"high":"32768","low":"4096","medium":"16384"},"Compat":null}`},
	{"claude-sonnet-4-6", `{"ID":"claude-sonnet-4-6","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":3,"OutputCostPerMillion":15,"CacheReadCostPerMillion":0.3,"CacheWriteCostPerMillion":3.75,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium"},"Compat":null}`},
	{"claude-sonnet-5", `{"ID":"claude-sonnet-5","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":2,"OutputCostPerMillion":10,"CacheReadCostPerMillion":0.2,"CacheWriteCostPerMillion":2.5,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
	{"claude-sonnet-5-5", `{"ID":"claude-sonnet-5-5","ContextWindow":1000000,"MaxOutputTokens":128000,"InputCostPerMillion":2,"OutputCostPerMillion":10,"CacheReadCostPerMillion":0.2,"CacheWriteCostPerMillion":2.5,"ThinkingKind":"adaptive","Efforts":{"high":"high","low":"low","max":"max","medium":"medium","xhigh":"xhigh"},"Compat":{"supports_sampling":false}}`},
}

// TS-26-9: the 13 anthropic rows and the vendor's own fields are unchanged from
// catalog_version 2026-09-30.
func TestAnthropicRowsAreUnchanged_TS26_9(t *testing.T) {
	c := Default()
	ids := make([]string, 0, len(anthropicSnapshot))
	for _, s := range anthropicSnapshot {
		ids = append(ids, s.id)
	}
	if got := c.Models(Vendor); !slices.Equal(got, ids) {
		t.Fatalf("Models(anthropic) = %v, want the snapshot's %v", got, ids)
	}
	for _, s := range anthropicSnapshot {
		m, ok := c.Lookup(s.id)
		if !ok {
			t.Errorf("Lookup(%q) is not listed", s.id)
			continue
		}
		got, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != s.model {
			t.Errorf("Lookup(%q) changed:\n got %s\nwant %s", s.id, got, s.model)
		}
	}
	v := c.vendors[Vendor]
	if v.Name != "Anthropic" || v.API != core.APIAnthropicMessages || v.BaseURL != "https://api.anthropic.com" ||
		v.DefaultModel != "claude-sonnet-5-5" {
		t.Errorf("anthropic vendor = %q, %q, %q, default %q", v.Name, v.API, v.BaseURL, v.DefaultModel)
	}
	if h := v.Headers["anthropic-version"]; len(v.Headers) != 1 || h == nil || *h != "2023-06-01" {
		t.Errorf("anthropic headers = %v, want only anthropic-version 2023-06-01", v.Headers)
	}
}

// embeddedRow returns the vendor, the file row and the resolved descriptor of
// one row of the embedded catalog.
func embeddedRow(t *testing.T, vendor, id string) (*vendorRow, *modelRow, core.Model) {
	t.Helper()
	c := Default()
	v, ok := c.vendors[vendor]
	if !ok {
		t.Fatalf("the embedded catalog has no %s vendor", vendor)
	}
	row := v.Models[id]
	e, ok := c.byCanonical[vendor+"/"+id]
	if row == nil || !ok {
		t.Fatalf("the %s vendor has no row %q", vendor, id)
	}
	return v, row, e.model
}

// wantQwenEfforts checks the level map both new rows share: low, medium, high
// and max, each mapped to its own name, and no xhigh.
func wantQwenEfforts(t *testing.T, row *modelRow, m core.Model) {
	t.Helper()
	if len(row.ThinkingLevelMap) != 4 || len(m.Efforts) != 4 {
		t.Errorf("thinking_level_map has %d keys and Efforts %d, want the same four", len(row.ThinkingLevelMap), len(m.Efforts))
	}
	for _, e := range []core.Effort{core.EffortLow, core.EffortMedium, core.EffortHigh, core.EffortMax} {
		if w := m.Efforts[e]; w == nil || *w != string(e) {
			t.Errorf("Efforts[%s] = %v, want %q", e, w, e)
		}
	}
	if _, ok := m.Efforts[core.EffortXHigh]; ok {
		t.Error("Efforts has xhigh; it was not probed, so it is left out")
	}
}

// wantNoteMentions fails the test for each of parts that note lacks.
func wantNoteMentions(t *testing.T, note string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(note, p) {
			t.Errorf("note does not mention %q: %s", p, note)
		}
	}
}

// TS-26-6: the embedded catalog carries the openrouter vendor and its single
// qwen/qwen3.8-flash row.
func TestEmbeddedOpenRouterRow_TS26_6(t *testing.T) {
	c := Default()
	if !c.KnownVendor("openrouter") {
		t.Fatal("KnownVendor(openrouter) = false")
	}
	if got := c.DefaultModelID("openrouter"); got != "qwen/qwen3.8-flash" {
		t.Errorf("DefaultModelID(openrouter) = %q", got)
	}
	if got := c.Models("openrouter"); !slices.Equal(got, []string{"qwen/qwen3.8-flash"}) {
		t.Fatalf("Models(openrouter) = %v, want only qwen/qwen3.8-flash", got)
	}
	v, row, m := embeddedRow(t, "openrouter", "qwen/qwen3.8-flash")
	if v.Name != "OpenRouter" || v.API != "openrouter-chat" || v.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("vendor = %q, %q, %q", v.Name, v.API, v.BaseURL)
	}
	if m.ContextWindow != 1000000 || m.MaxOutputTokens != 131072 {
		t.Errorf("window/max = %d/%d, want 1000000/131072", m.ContextWindow, m.MaxOutputTokens)
	}
	if m.InputCostPerMillion != 0.15 || m.OutputCostPerMillion != 0.47 ||
		m.CacheReadCostPerMillion != 0.016 || m.CacheWriteCostPerMillion != 0 {
		t.Errorf("prices = %v/%v/%v/%v, want 0.15/0.47/0.016/0", m.InputCostPerMillion, m.OutputCostPerMillion,
			m.CacheReadCostPerMillion, m.CacheWriteCostPerMillion)
	}
	if !slices.Equal(row.Input, []string{"text"}) || !row.Reasoning {
		t.Errorf("input = %v, reasoning = %v, want [text] and true", row.Input, row.Reasoning)
	}
	wantQwenEfforts(t, row, m)
	wantNoteMentions(t, row.Note, "OpenRouter", "2026-10-21", "low", "medium", "high", "max")
}

// TS-26-7: the embedded catalog carries the ollama vendor and its single
// qwen3.8:27b row, whose note gives the three facts a reader needs.
func TestEmbeddedOllamaRow_TS26_7(t *testing.T) {
	c := Default()
	if !c.KnownVendor("ollama") {
		t.Fatal("KnownVendor(ollama) = false")
	}
	if got := c.DefaultModelID("ollama"); got != "qwen3.8:27b" {
		t.Errorf("DefaultModelID(ollama) = %q", got)
	}
	if got := c.Models("ollama"); !slices.Equal(got, []string{"qwen3.8:27b"}) {
		t.Fatalf("Models(ollama) = %v, want only qwen3.8:27b", got)
	}
	v, row, m := embeddedRow(t, "ollama", "qwen3.8:27b")
	if v.Name != "Ollama" || v.API != core.APIOllamaChat || v.BaseURL != "http://127.0.0.1:11434" {
		t.Errorf("vendor = %q, %q, %q", v.Name, v.API, v.BaseURL)
	}
	if m.ContextWindow != 32768 || m.MaxOutputTokens != 32768 {
		t.Errorf("window/max = %d/%d, want 32768/32768", m.ContextWindow, m.MaxOutputTokens)
	}
	if m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 || m.CacheReadCostPerMillion != 0 || m.CacheWriteCostPerMillion != 0 {
		t.Errorf("prices are not all zero: %+v", row.Cost)
	}
	if !slices.Equal(row.Input, []string{"text"}) || !row.Reasoning {
		t.Errorf("input = %v, reasoning = %v, want [text] and true", row.Input, row.Reasoning)
	}
	wantQwenEfforts(t, row, m)
	wantNoteMentions(t, row.Note, "32,768", "262,144", "num_ctx", "Apple M4", "own hardware")
}

// TS-26-8: the catalog version is 2026-10-21, the note keeps the ritual and
// mentions tiers, and no OpenAI or Google content exists.
func TestCatalogVersionAndNote_TS26_8(t *testing.T) {
	c := Default()
	if c.Version() != "2026-10-21" {
		t.Errorf("Version() = %q, want 2026-10-21", c.Version())
	}
	var f struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(embeddedCatalog, &f); err != nil || f.SchemaVersion != 1 {
		t.Errorf("schema_version = %d (%v), want 1", f.SchemaVersion, err)
	}
	wantNoteMentions(t, c.Note(), "REQ-CAT-06", "tier")
	for _, other := range otherVendorMarkers {
		if strings.Contains(string(embeddedCatalog), other) {
			t.Errorf("catalog.json contains %s", other)
		}
	}
}

// TS-26-32: Lookup still resolves anthropic rows only, so an id of the new
// providers gets the unchanged Anthropic fallback.
func TestLookupStaysAnthropicOnly_TS26_32(t *testing.T) {
	for _, id := range []string{"qwen/qwen3.8-flash", "qwen3.8:27b"} {
		m, ok := Lookup(id)
		if ok || m.ID != id || m.ContextWindow != 1_000_000 || m.MaxOutputTokens != 128_000 {
			t.Errorf("Lookup(%q) = %q %d/%d, %v; want the unlisted Anthropic fallback 1000000/128000",
				id, m.ID, m.ContextWindow, m.MaxOutputTokens, ok)
		}
	}
	for _, id := range []string{"anthropic/claude-sonnet-5-5", "claude-sonnet-5-5"} {
		if m, ok := Lookup(id); !ok || m.ID != "claude-sonnet-5-5" {
			t.Errorf("Lookup(%q) = %q, %v; want the claude-sonnet-5-5 row", id, m.ID, ok)
		}
	}
}

// TS-26-33: the Vendor constant stays anthropic, and the embedded vendors are
// exactly anthropic, ollama and openrouter.
func TestVendorsAreTheThreeProviders_TS26_33(t *testing.T) {
	if Vendor != "anthropic" {
		t.Errorf("Vendor = %q, want anthropic", Vendor)
	}
	c := Default()
	if got := c.Vendors(); !slices.Equal(got, []string{"anthropic", "ollama", "openrouter"}) {
		t.Errorf("Vendors() = %v, want [anthropic ollama openrouter]", got)
	}
	for _, v := range Providers() {
		if !c.KnownVendor(v) {
			t.Errorf("KnownVendor(%q) = false", v)
		}
	}
	if c.KnownVendor("openai") {
		t.Error("KnownVendor(openai) = true")
	}
}
