package catalog

import (
	"encoding/json"
	"errors"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

func TestParseRejectsACorruptCatalogAtThePathThatFailed(t *testing.T) {
	low, std, high := pick("qwen", "low"), pick("qwen", "medium"), pick("qwen", "high")
	for _, tc := range []struct{ name, doc, path, reason string }{
		{"not json", `{`, "", "unexpected end"},
		{"future schema version", `{"schema_version":2,"vendors":{}}`, "schema_version", "this build understands 1"},
		{"no vendors", `{"schema_version":1,"vendors":{}}`, "vendors", "declares no vendors"},
		{"null vendor", `{"schema_version":1,"vendors":{"v":null}}`, "vendors.v", "null vendor"},
		{"empty vendor id", `{"schema_version":1,"vendors":{"":{"default_model":"m","models":{"m":{}}}}}`, "vendors", "empty vendor id"},
		{"vendor id with a slash", `{"schema_version":1,"vendors":{"a/b":{"api":"faux","base_url":"x","default_model":"m","models":{"m":{}}}}}`, "vendors.a/b", "may not contain a slash"},
		{"vendor with no models", oneVendor(`"models":{}`), "vendors.v.models", "declares no models"},
		{"missing default_model", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","models":{"m":{}}}}}`, "vendors.v.default_model", "missing; every vendor names its most representative model"},
		{"dangling default_model", `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"nope","models":{"m":{}}}}}`, "vendors.v.default_model", "not a model of this vendor"},
		{"null model row", oneVendor(`"models":{"m":{},"n":null}`), "vendors.v.models.n", "null model row"},
		{"empty model id", oneVendor(`"models":{"m":{},"":{}}`), "vendors.v.models", "empty model id"},
		{"row id disagrees with key", oneVendor(`"models":{"m":{"id":"other"}}`), "vendors.v.models.m.id", "keyed"},
		{"no api anywhere", `{"schema_version":1,"vendors":{"v":{"base_url":"x","default_model":"m","models":{"m":{}}}}}`, "vendors.v.models.m.api", "no default api"},
		{"no base_url anywhere", `{"schema_version":1,"vendors":{"v":{"api":"faux","default_model":"m","models":{"m":{}}}}}`, "vendors.v.models.m.base_url", "no default base_url"},
		{"negative window", oneVendor(`"models":{"m":{"context_window":-1}}`), "vendors.v.models.m.context_window", "negative"},
		{"negative output cap", oneVendor(`"models":{"m":{"max_tokens":-1}}`), "vendors.v.models.m.max_tokens", "negative"},
		{"negative input price", oneVendor(`"models":{"m":{"cost":{"input":-1}}}`), "vendors.v.models.m.cost.input", "negative"},
		{"negative cache price", oneVendor(`"models":{"m":{"cost":{"cache_write":-0.5}}}`), "vendors.v.models.m.cost.cache_write", "negative"},
		{"misspelled thinking level", oneVendor(`"models":{"m":{"reasoning":true,"thinking_level_map":{"higth":"high"}}}`), "vendors.v.models.m.thinking_level_map", "unknown thinking level"},
		{"thinking without reasoning", oneVendor(`"models":{"m":{"thinking_level_map":{"high":"high"}}}`), "vendors.v.models.m.reasoning", "supports 1 level"},
		{"lower-case tier key", tierDoc(`{"SIMPLE":` + low + `,"standard":` + std + `,"ADVANCED":` + high + `}`), "vendors.ollama.tiers.standard", "not a tier"},
		{"unknown tier key", tierDoc(`{"SIMPLE":` + low + `,"STANDARD":` + std + `,"ADVANCED":` + high + `,"EXPERT":` + high + `}`), "vendors.ollama.tiers.EXPERT", "not a tier"},
		{"missing tier", tierDoc(tiers(low, std, "")), "vendors.ollama.tiers.ADVANCED", "missing"},
		{"empty tiers", tierDoc(`{}`), "vendors.ollama.tiers.SIMPLE", "missing"},
		{"empty tier model", tierDoc(tiers(low, pick("", "medium"), high)), "vendors.ollama.tiers.STANDARD.model", "not a model"},
		{"tier row of another vendor", tierDoc(tiers(low, pick("other-model", "medium"), high)), "vendors.ollama.tiers.STANDARD.model", "not a model"},
		{"null tier", tierDoc(tiers(low, "null", high)), "vendors.ollama.tiers.STANDARD.model", "not a model"},
		{"tier effort in the wrong case", tierDoc(tiers(low, pick("qwen", "HIGH"), high)), "vendors.ollama.tiers.STANDARD.effort", "not an effort"},
		{"tier level that is not an effort", tierDoc(tiers(low, pick("qwen", "minimal"), high)), "vendors.ollama.tiers.STANDARD.effort", "not an effort"},
		{"tier effort the row lacks", tierDoc(tiers(low, pick("qwen", "xhigh"), high)), "vendors.ollama.tiers.STANDARD.effort", "does not take"},
		{"tier effort the row lists as null", tierDoc(tiers(low, pick("nullhigh", "high"), high)), "vendors.ollama.tiers.STANDARD.effort", "does not take"},
		{"tier on a row without a level map", tierDoc(tiers(low, pick("nomap", "low"), high)), "vendors.ollama.tiers.STANDARD.effort", "does not take"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse([]byte(tc.doc))
			var pe *ParseError
			if c != nil || !errors.As(err, &pe) || !errors.Is(err, ErrCorruptCatalog) {
				t.Fatalf("Parse = %v, %v; want no catalog and a *ParseError matching ErrCorruptCatalog", c, err)
			}
			if pe.Path != tc.path || !strings.Contains(pe.Reason, tc.reason) {
				t.Fatalf("error at %q (%s), want %q mentioning %q", pe.Path, pe.Reason, tc.path, tc.reason)
			}
			want := "agentkit: model catalog is corrupt at " + tc.path + ": " + pe.Reason
			if tc.path == "" {
				want = "agentkit: model catalog is corrupt: " + pe.Reason
			}
			if err.Error() != want {
				t.Fatalf("Error() = %q, want %q", err, want)
			}
		})
	}
}

func TestParseLoadsAnUnknownAPIAndAVendorWithoutTiers(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown api":  `{"schema_version":1,"vendors":{"acme":{"api":"acme-chat-v9","base_url":"https://acme.example","default_model":"m","models":{"m":{}}}}}`,
		"null tiers":   tierDoc("null"),
		"valid tiers":  tierDoc(tiers(pick("qwen", "low"), pick("qwen", "medium"), pick("nullhigh", "low"))),
		"row api only": `{"schema_version":1,"vendors":{"v":{"default_model":"m","models":{"m":{"api":"a","base_url":"b"}}}}}`,
	} {
		if _, err := Parse([]byte(doc)); err != nil {
			t.Errorf("%s: Parse: %v", name, err)
		}
	}
}

func TestParseProjectsARowOntoItsModelDescriptor(t *testing.T) {
	c := testCatalog(t)
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		id   string
		want core.Model
	}{
		{"claude-sonnet-4-5", core.Model{ID: "claude-sonnet-4-5", ContextWindow: 200000, MaxOutputTokens: 64000,
			InputCostPerMillion: 3, OutputCostPerMillion: 15, CacheReadCostPerMillion: 0.3, CacheWriteCostPerMillion: 3.75,
			ThinkingKind: core.ThinkingKindBudget,
			Efforts:      map[core.Effort]*string{core.EffortLow: str("4096"), core.EffortMedium: str("16384"), core.EffortHigh: str("32768")},
			Compat:       json.RawMessage(`{"beta_headers": ["fine-grained-tool-streaming"]}`)}},
		{"claude-opus-4-5", core.Model{ID: "claude-opus-4-5", ContextWindow: 200000, MaxOutputTokens: 64000,
			InputCostPerMillion: 5, OutputCostPerMillion: 25, ThinkingKind: core.ThinkingKindAdaptive,
			Efforts: map[core.Effort]*string{core.EffortLow: str("low"), core.EffortMedium: str("medium"), core.EffortHigh: nil}}},
		{"claude-plain", core.Model{ID: "claude-plain", ContextWindow: 1000, MaxOutputTokens: 10,
			ThinkingKind: core.ThinkingKindNone, Efforts: map[core.Effort]*string{}}},
	} {
		got, ok := c.Lookup(tc.id)
		gj, _ := json.Marshal(got)
		wj, _ := json.Marshal(tc.want)
		if !ok || string(gj) != string(wj) {
			t.Errorf("Lookup(%q) = %s, %v\nwant %s", tc.id, gj, ok, wj)
		}
	}
}

func TestOnceCatalogPanicsOnFirstUseAndAgainWithTheSameValue(t *testing.T) {
	get := onceCatalog([]byte(`{"schema_version": 1, "vendors": {`)) // building it must not panic
	first := recoverPanic(get)
	if msg, ok := first.(string); !ok || !strings.Contains(msg, "embedded model catalog failed to load") || !strings.Contains(msg, "corrupt") {
		t.Fatalf("panic value = %#v, want a string naming the corruption", first)
	}
	if second := recoverPanic(get); second != first {
		t.Fatalf("second use panicked with %#v, want the identical value %#v", second, first)
	}

	good := onceCatalog([]byte(testCatalogJSON))
	if p := recoverPanic(good); p != nil {
		t.Fatalf("a valid catalog panicked: %v", p)
	}
	if good() != good() || Default() != Default() { //nolint:staticcheck // memoization is the behaviour
		t.Error("the catalog was parsed again on a later call")
	}
}

func TestCatalogAccessorsReportTheDocument(t *testing.T) {
	c := testCatalog(t)
	if c.Version() != "test-1" || c.Note() != "test note" {
		t.Errorf("Version() = %q, Note() = %q", c.Version(), c.Note())
	}
	vs := c.Vendors()
	if !slices.Equal(vs, []string{"anthropic", "azure", "openai", "openrouter"}) {
		t.Errorf("Vendors() = %v, want them sorted", vs)
	}
	vs[0] = "tampered"
	if c.Vendors()[0] != "anthropic" {
		t.Error("Vendors() aliases internal state")
	}
	if !c.KnownVendor("azure") || c.KnownVendor("ollama") || c.KnownVendor("") {
		t.Error("KnownVendor disagrees with the document")
	}
	if got := c.Models("anthropic"); !slices.Equal(got, []string{"claude-opus-4-5", "claude-plain", "claude-sonnet-4-5"}) {
		t.Errorf("Models(anthropic) = %v, want them sorted", got)
	}
	if c.DefaultModelID("openrouter") != "anthropic/claude-opus-4-5" || c.DefaultModelID("nope") != "" || c.Models("nope") != nil {
		t.Error("DefaultModelID or Models disagrees with the document")
	}
}

func TestLookupResolvesAClaudeIDWithOrWithoutThePrefix(t *testing.T) {
	c := testCatalog(t)
	for _, id := range []string{"claude-sonnet-4-5", "anthropic/claude-sonnet-4-5", "  claude-sonnet-4-5 "} {
		if m, ok := c.Lookup(id); !ok || m.ID != "claude-sonnet-4-5" || m.ContextWindow != 200000 {
			t.Errorf("Lookup(%q) = %q %d, %v", id, m.ID, m.ContextWindow, ok)
		}
	}
	// Lookup reads the anthropic vendor only: another vendor's row is unlisted.
	if _, ok := c.Lookup("gpt-4o"); ok {
		t.Error("Lookup found a row of another vendor")
	}
}

func TestLookupServesAnUnlistedIDWithTheClaudeFallback(t *testing.T) {
	for _, tc := range []struct{ in, id string }{
		{"claude-next-gen-future", "claude-next-gen-future"},
		{"anthropic/claude-x", "claude-x"},
		{"qwen3.8:27b", "qwen3.8:27b"},
		{"anthropic/", "anthropic/"},
		{"", "unknown"},
	} {
		m, ok := Lookup(tc.in)
		want := core.Model{ID: tc.id, ContextWindow: 1_000_000, MaxOutputTokens: 128_000, ThinkingKind: core.ThinkingKindAdaptive}
		gj, _ := json.Marshal(m)
		wj, _ := json.Marshal(want)
		if ok || string(gj) != string(wj) {
			t.Errorf("Lookup(%q) = %s, %v; want %s and unlisted", tc.in, gj, ok, wj)
		}
	}
	r := rand.New(rand.NewSource(10))
	for range 500 {
		id := randomString(r, 40)
		if m, _ := Lookup(id); m.ID == "" || m.ContextWindow <= 0 || m.MaxOutputTokens <= 0 {
			t.Fatalf("Lookup(%q) = %+v, not usable", id, m)
		}
	}
}

func TestLookupReturnsADeepCopy(t *testing.T) {
	c := testCatalog(t)
	first, _ := c.Lookup("claude-sonnet-4-5")
	first.MaxOutputTokens, first.InputCostPerMillion = 1, 999
	first.Compat[0] = ' '
	*first.Efforts[core.EffortLow] = "mutated"
	first.Efforts[core.EffortHigh] = nil
	second, _ := c.Lookup("claude-sonnet-4-5")
	if second.MaxOutputTokens != 64000 || second.InputCostPerMillion != 3 || second.Compat[0] != '{' ||
		*second.Efforts[core.EffortLow] != "4096" || second.Efforts[core.EffortHigh] == nil {
		t.Fatalf("a caller's edit reached the catalog: %+v", second)
	}
	opus, _ := c.Lookup("claude-opus-4-5")
	if w, present := opus.Efforts[core.EffortHigh]; !present || w != nil {
		t.Fatal("a present-null effort must survive the copy as present and nil")
	}
}

func TestTheEmbeddedCatalogLoadsAndListsTheThreeProviders(t *testing.T) {
	c := Default()
	if got := c.Vendors(); !slices.Equal(got, []string{"anthropic", "ollama", "openrouter"}) {
		t.Fatalf("Vendors() = %v", got)
	}
	if c.Version() == "" {
		t.Error("the embedded catalog has no version")
	}
	for _, id := range c.Models(Vendor) {
		if m, ok := Lookup(id); !ok || m.ID != id || m.ContextWindow <= 0 || m.MaxOutputTokens <= 0 {
			t.Errorf("Lookup(%q) = %+v, %v", id, m, ok)
		}
	}
	if haiku, _ := Lookup("claude-haiku-4-5"); haiku.ThinkingKind != core.ThinkingKindBudget {
		t.Errorf("claude-haiku-4-5 thinking = %q; a row whose levels are token counts takes a budget", haiku.ThinkingKind)
	}
	if opus, _ := Lookup("claude-opus-5-5"); opus.ThinkingKind != core.ThinkingKindAdaptive {
		t.Errorf("claude-opus-5-5 thinking = %q; a row whose levels are efforts is adaptive", opus.ThinkingKind)
	}
}
