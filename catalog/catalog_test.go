package catalog

import (
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
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

func strp(s string) *string { return &s }

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
	if got := c.Vendors(); len(got) != 1 || got[0] != "anthropic" {
		t.Errorf("Vendors() = %v, want [anthropic]", got)
	}
	if !c.KnownVendor("anthropic") || c.KnownVendor("openai") {
		t.Error("KnownVendor must report anthropic and nothing else")
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

// TS-10-11: the catalog holds Claude rows only, and the multi-vendor
// resolution and clamping files are gone.
func TestCatalogIsClaudeOnly_TS10_11(t *testing.T) {
	raw := string(embeddedCatalog)
	if !strings.Contains(raw, "claude") {
		t.Fatal("the catalog has no Claude rows")
	}
	for _, other := range []string{"gpt-", "gemini", `"openai"`, `"google"`} {
		if strings.Contains(raw, other) {
			t.Errorf("catalog.json still mentions %s", other)
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
