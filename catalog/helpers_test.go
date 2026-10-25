package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

// testCatalogJSON is a purpose-built catalog, not the shipped one: it holds
// configurations the shipped catalog must not have (vendors that are not
// providers, an id carried by two vendors, an openrouter row whose id has a
// slash) and no ollama section.
const testCatalogJSON = `{
  "schema_version": 1,
  "catalog_version": "test-1",
  "note": "test note",
  "vendors": {
    "anthropic": {
      "api": "anthropic-messages", "base_url": "https://api.anthropic.com",
      "headers": {"anthropic-version": "2023-06-01"}, "default_model": "claude-sonnet-4-5",
      "models": {
        "claude-opus-4-5": {"name": "Claude Opus 4.5", "context_window": 200000, "max_tokens": 64000,
          "cost": {"input": 5, "output": 25}, "input": ["text","image"], "reasoning": true,
          "thinking_level_map": {"off": "disabled", "minimal": null, "low": "low", "medium": "medium", "high": null}},
        "claude-sonnet-4-5": {"id": "claude-sonnet-4-5", "context_window": 200000, "max_tokens": 64000,
          "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
          "compat": {"beta_headers": ["fine-grained-tool-streaming"]}, "reasoning": true,
          "thinking_level_map": {"off": "disabled", "low": "4096", "medium": "16384", "high": "32768"}},
        "claude-plain": {"api": "own-api", "base_url": "https://own.example", "context_window": 1000, "max_tokens": 10,
          "thinking_level_map": {"off": "disabled"}}
      }
    },
    "openai": {
      "api": "openai-completions", "base_url": "https://api.openai.com/v1", "default_model": "gpt-4o",
      "models": {"gpt-4o": {"context_window": 128000, "max_tokens": 16384}}
    },
    "azure": {
      "api": "openai-completions", "base_url": "https://example.openai.azure.com", "default_model": "gpt-4o",
      "models": {"gpt-4o": {"context_window": 128000, "max_tokens": 16384}}
    },
    "openrouter": {
      "api": "openai-completions", "base_url": "https://openrouter.ai/api/v1", "default_model": "anthropic/claude-opus-4-5",
      "models": {
        "anthropic/claude-opus-4-5": {"context_window": 200000, "max_tokens": 64000, "cost": {"input": 5, "output": 25}},
        "deepseek-ai/DeepSeek-V3": {"context_window": 64000, "max_tokens": 8192}
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

// testCatalogWithTiers is the test catalog with a tiers block on its
// openrouter vendor, whose tiers name ids with slashes of their own.
func testCatalogWithTiers(t *testing.T) *Catalog {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(testCatalogJSON), &doc); err != nil {
		t.Fatal(err)
	}
	vendor := doc["vendors"].(map[string]any)["openrouter"].(map[string]any)
	for _, row := range vendor["models"].(map[string]any) {
		row.(map[string]any)["reasoning"] = true
		row.(map[string]any)["thinking_level_map"] = map[string]any{"low": "low", "medium": "medium", "high": "high"}
	}
	vendor["tiers"] = map[string]any{
		"SIMPLE":   map[string]any{"model": "deepseek-ai/DeepSeek-V3", "effort": "low"},
		"STANDARD": map[string]any{"model": "anthropic/claude-opus-4-5", "effort": "medium"},
		"ADVANCED": map[string]any{"model": "anthropic/claude-opus-4-5", "effort": "high"},
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

// oneVendor is a catalog of a single vendor "v" with the given members after
// a valid api, base_url and default_model "m".
func oneVendor(members string) string {
	return `{"schema_version":1,"vendors":{"v":{"api":"faux","base_url":"x","default_model":"m",` + members + `}}}`
}

// tierDoc is a catalog whose ollama vendor carries tiers. Its rows are the
// ones a tier can name: qwen takes low, medium, high and max but not xhigh;
// nullhigh lists high as null; nomap has no level map. The other vendor holds
// a row ollama does not.
func tierDoc(tiers string) string {
	return fmt.Sprintf(`{"schema_version":1,"vendors":{
  "ollama":{"api":"ollama-chat","base_url":"http://127.0.0.1:11434","default_model":"qwen",
    "models":{
      "qwen":{"reasoning":true,"thinking_level_map":{"low":"low","medium":"medium","high":"high","max":"max"}},
      "nullhigh":{"reasoning":true,"thinking_level_map":{"low":"low","high":null}},
      "nomap":{}},
    "tiers":%s},
  "other":{"api":"ollama-chat","base_url":"http://127.0.0.1:11434","default_model":"other-model",
    "models":{"other-model":{"reasoning":true,"thinking_level_map":{"medium":"medium"}}}}}}`, tiers)
}

// tiers spells a tiers object; an empty entry is left out.
func tiers(simple, standard, advanced string) string {
	var parts []string
	for _, kv := range [][2]string{{"SIMPLE", simple}, {"STANDARD", standard}, {"ADVANCED", advanced}} {
		if kv[1] != "" {
			parts = append(parts, fmt.Sprintf("%q:%s", kv[0], kv[1]))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func pick(model, effort string) string { return fmt.Sprintf(`{"model":%q,"effort":%q}`, model, effort) }

func zeroSelection(sel Selection) bool { return reflect.DeepEqual(sel, Selection{}) }

func recoverPanic(f func() *Catalog) (p any) {
	defer func() { p = recover() }()
	f()
	return nil
}

func wantError(t *testing.T, err, sentinel error, parts ...string) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want one wrapping %v", err, sentinel)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Errorf("message %q does not contain %q", err, p)
		}
	}
}

// randomAlphabet mixes ASCII, the characters a vendor id uses, whitespace and
// non-ASCII letters, kana and an emoji.
var randomAlphabet = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-./:@_ \t\n ÄΩ日本\U0001F600")

func randomString(r *rand.Rand, max int) string {
	var b strings.Builder
	for n := r.Intn(max + 1); n > 0; n-- {
		b.WriteRune(randomAlphabet[r.Intn(len(randomAlphabet))])
	}
	return b.String()
}

func shorten(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}
