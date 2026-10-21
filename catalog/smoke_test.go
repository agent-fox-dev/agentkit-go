package catalog_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// TS-26-36 (smoke, 26-PATH-1): an embedder resolves the OpenRouter STANDARD
// tier and prices a usage of one million input tokens with the row it got.
func TestSmokeOpenRouterStandardTierPrices_TS26_36(t *testing.T) {
	sel, err := catalog.Select("openrouter", "STANDARD", "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if sel.Provider != "openrouter" || sel.Tier != catalog.TierStandard || sel.Model.ID != "qwen/qwen3.8-flash" ||
		sel.Effort != core.EffortMedium || !sel.Listed {
		t.Errorf("Selection = %+v, want openrouter STANDARD qwen/qwen3.8-flash at medium, listed", sel)
	}
	if sel.Model.ContextWindow != 1000000 || sel.Model.MaxOutputTokens != 131072 {
		t.Errorf("window/max = %d/%d, want 1000000/131072", sel.Model.ContextWindow, sel.Model.MaxOutputTokens)
	}
	if cost := provider.ComputeCost(&sel.Model, core.Usage{InputTokens: 1000000}); math.Abs(cost-0.15) > 1e-9 {
		t.Errorf("ComputeCost = %v USD for one million input tokens, want 0.15", cost)
	}
}

// TS-26-37 (smoke, 26-PATH-2): an embedder names an unlisted Ollama model with
// an explicit effort and receives the fallback descriptor.
func TestSmokeUnlistedOllamaModel_TS26_37(t *testing.T) {
	sel, err := catalog.Select(" Ollama ", "llama4:8b", "HIGH")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	m := sel.Model
	if sel.Provider != "ollama" || sel.Tier != "" || sel.Effort != core.EffortHigh || sel.Listed {
		t.Errorf("Selection = %q tier %q effort %q listed=%v, want ollama, no tier, high, unlisted",
			sel.Provider, sel.Tier, sel.Effort, sel.Listed)
	}
	if m.ID != "llama4:8b" || m.ContextWindow != 32768 || m.MaxOutputTokens != 32768 || m.Efforts != nil {
		t.Errorf("Model = %q %d/%d efforts %v, want llama4:8b 32768/32768 and a nil Efforts", m.ID, m.ContextWindow, m.MaxOutputTokens, m.Efforts)
	}
	if m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 || m.CacheReadCostPerMillion != 0 || m.CacheWriteCostPerMillion != 0 {
		t.Errorf("Model is priced: %+v", m)
	}
}

// TS-26-38 (smoke, 26-PATH-3): the embedded catalog.json with only the ollama
// STANDARD tier's effort edited to xhigh, which the qwen3.8:27b row does not
// take, is rejected at load with the failing path.
func TestSmokeMalformedTierIsRejectedAtLoad_TS26_38(t *testing.T) {
	raw, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Parse(raw); err != nil {
		t.Fatalf("the unedited catalog.json does not parse: %v", err)
	}

	// Numbers stay as written, so the only change is the effort.
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	standard := doc["vendors"].(map[string]any)["ollama"].(map[string]any)["tiers"].(map[string]any)["STANDARD"].(map[string]any)
	standard["effort"] = "xhigh"
	edited, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	c, err := catalog.Parse(edited)
	if c != nil {
		t.Error("Parse returned a catalog for the edited tier")
	}
	var pe *catalog.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error = %v, want a *catalog.ParseError", err)
	}
	if pe.Path != "vendors.ollama.tiers.STANDARD.effort" {
		t.Errorf("Path = %q, want vendors.ollama.tiers.STANDARD.effort (reason: %s)", pe.Path, pe.Reason)
	}
	if !errors.Is(err, catalog.ErrCorruptCatalog) {
		t.Errorf("error does not match ErrCorruptCatalog: %v", err)
	}
}
