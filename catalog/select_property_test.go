package catalog

import (
	"math/rand"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// propertyAlphabet mixes ASCII, the characters a vendor id uses, whitespace
// (including a no-break space and a newline) and non-ASCII letters, kana and an
// emoji.
var propertyAlphabet = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-./:@_ \t\n ÄΩ日本\U0001F600")

// randomString returns a string of at most max runes drawn from
// propertyAlphabet.
func randomString(r *rand.Rand, max int) string {
	var b strings.Builder
	for n := r.Intn(max + 1); n > 0; n-- {
		b.WriteRune(propertyAlphabet[r.Intn(len(propertyAlphabet))])
	}
	return b.String()
}

// shorten cuts a string for a failure message.
func shorten(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// TS-26-13 (property): for any provider and any model id that is not a tier, a
// result reported as listed is a row of that provider's own vendor, and an id
// that only another provider lists is never listed.
func TestSelectListedIsAlwaysAnOwnRow_TS26_13(t *testing.T) {
	c := Default()
	r := rand.New(rand.NewSource(13))
	var ids []string
	for _, v := range c.Vendors() {
		for _, id := range c.Models(v) {
			ids = append(ids, id, strings.ToUpper(id), strings.ToLower(id), " "+id+" ",
				"anthropic/"+id, "openrouter/"+id, "ollama/"+id)
		}
	}
	for range 500 {
		ids = append(ids, randomString(r, 30))
	}

	for _, p := range Providers() {
		own := c.Models(p)
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
			if err != nil {
				t.Errorf("Select(%q, %q): %v", p, shorten(raw), err)
				continue
			}
			if got := slices.Contains(own, bare); sel.Listed != got {
				t.Errorf("Select(%q, %q).Listed = %v, but %q is a row of %s: %v", p, shorten(raw), sel.Listed, bare, p, got)
			}
			if sel.Listed {
				if row := c.byCanonical[p+"/"+bare]; row == nil || !reflect.DeepEqual(sel.Model, row.model) {
					t.Errorf("Select(%q, %q) is listed but is not the %s row %q: %+v", p, shorten(raw), p, bare, sel.Model)
				}
			}
			for _, q := range Providers() {
				if q != p && !slices.Contains(own, bare) && slices.Contains(c.Models(q), id) && sel.Listed {
					t.Errorf("Select(%q, %q) is listed, but %q is only a row of %s", p, shorten(raw), id, q)
				}
			}
		}
	}
}

// checkSelectIsTotal fails the test if Select panics, or returns neither an
// error nor a Selection with positive limits and a provider of Providers().
func checkSelectIsTotal(t *testing.T, name string, c *Catalog, provider, model, effort string) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: Select(%q, %q, %q) panicked: %v", name, shorten(provider), shorten(model), shorten(effort), r)
		}
	}()
	sel, err := c.Select(provider, model, core.Effort(effort))
	if err != nil {
		if !zeroSelection(sel) {
			t.Errorf("%s: Select(%q, %q, %q) returned %+v with the error %v", name, shorten(provider), shorten(model), shorten(effort), sel, err)
		}
		return
	}
	if sel.Model.ContextWindow <= 0 || sel.Model.MaxOutputTokens <= 0 || !slices.Contains(Providers(), sel.Provider) {
		t.Errorf("%s: Select(%q, %q, %q) = provider %q window %d cap %d, want a provider and positive limits",
			name, shorten(provider), shorten(model), shorten(effort), sel.Provider, sel.Model.ContextWindow, sel.Model.MaxOutputTokens)
	}
}

// TS-26-31 (property): for any provider, model and effort strings, Select
// never panics and returns an error or a Selection with positive limits.
func TestSelectIsTotal_TS26_31(t *testing.T) {
	catalogs := []struct {
		name string
		c    *Catalog
	}{
		{"embedded", Default()},
		{"test", testCatalog(t)},
		{"test with tiers", testCatalogWithTiers(t)},
	}
	r := rand.New(rand.NewSource(31))
	providers := []string{"", " ", "anthropic", "OpenRouter", " ollama ", "OLLAMA", "openai", "azure", "anthropic/"}
	models := []string{
		"standard", " ADVANCED ", "STANDARD-1", "Simple", "SIMPLE ", "", "  ",
		"claude-sonnet-5-5", "anthropic/claude-opus-5-5", "anthropic/", "qwen/qwen3.8-flash", "qwen3.8:27b",
		"deepseek-ai/DeepSeek-V3", "日本語", strings.Repeat("x", 100_000), strings.Repeat("STANDARD", 5_000),
	}
	efforts := []string{"", " ", "low", "HIGH", " XHigh ", "max", "turbo", "minimal", "off", "高", strings.Repeat("l", 50_000)}
	pick := func(fixed []string) string {
		if r.Intn(3) == 0 {
			return randomString(r, 40)
		}
		return fixed[r.Intn(len(fixed))]
	}
	for range 3000 {
		p, m, e := pick(providers), pick(models), pick(efforts)
		for _, cat := range catalogs {
			checkSelectIsTotal(t, cat.name, cat.c, p, m, e)
		}
	}
}
