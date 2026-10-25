package providerconformance

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// liveHint is where a skip message points.
const liveHint = "run make provider-live-test"

// liveProviders are the providers TestLive runs, one subtest each.
var liveProviders = []string{"anthropic", "openrouter", "ollama"}

// TestLive runs one real tool loop on the STANDARD tier of each provider whose
// prerequisites are present. It spends money on Anthropic and OpenRouter and
// runs a model locally for Ollama, so it skips as a whole under -short and
// unless AGENTKIT_LIVE is exactly 1, before anything is contacted; a provider
// whose prerequisites are missing skips its own subtest naming what is missing.
func TestLive(t *testing.T) {
	if testing.Short() {
		t.Skip("TestLive makes real requests and is skipped under -short; " + liveHint)
	}
	if v := os.Getenv("AGENTKIT_LIVE"); v != "1" {
		t.Skipf("AGENTKIT_LIVE is %q, not 1: the live provider tests spend money and run models; %s", v, liveHint)
	}
	for _, name := range liveProviders {
		t.Run(name, func(t *testing.T) {
			sel, err := catalog.Select(name, "STANDARD", "")
			if err != nil {
				t.Fatal(err)
			}
			skipUnlessReachable(t, name, sel)
			runLive(t, name, sel)
		})
	}
}

// skipUnlessReachable skips t, naming what is missing, unless the provider can
// be reached: Anthropic resolves a deployment from the environment, OpenRouter
// has a key, and Ollama answers at its host with the tier's model.
func skipUnlessReachable(t *testing.T, name string, sel catalog.Selection) {
	t.Helper()
	switch name {
	case "anthropic":
		if _, _, err := anthropic.Resolve(anthropic.OSEnv{}); err != nil {
			t.Skip(err) // Resolve's error begins "anthropic:" already
		}
	case "openrouter":
		if os.Getenv("OPENROUTER_API_KEY") == "" {
			t.Skip("openrouter: OPENROUTER_API_KEY is not set")
		}
	case "ollama":
		client, err := ollama.New(sel.Model, ollama.Options{})
		if err != nil {
			t.Skipf("ollama: %v", err)
		}
		host := client.Host()
		models, err := ollamaModels(host)
		if err != nil {
			t.Skipf("ollama: no server answers at %s (%v); start one with \"ollama serve\"", host, err)
		}
		if id := sel.Model.ID; !models[id] {
			t.Skipf("ollama: the server at %s does not have %s; run \"ollama pull %s\"", host, id, id)
		}
	}
}

// ollamaModels asks the server at host which models it has (GET /api/tags,
// within two seconds), by name and by model.
func ollamaModels(host string) (map[string]bool, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(host, "/") + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/tags answered %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct{ Name, Model string } `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("GET /api/tags did not answer a list of models: %w", err)
	}
	have := map[string]bool{}
	for _, m := range tags.Models {
		have[m.Name], have[m.Model] = true, true
	}
	return have, nil
}

// freshWord is a word and a number nobody has seen, such as "tamarind-7203",
// so that finding it in the answer shows the answer came from the file.
func freshWord(t *testing.T) string {
	words := []string{"heliotrope", "quillon", "tamarind", "obsidian", "fennel", "cobalt", "sorrel", "juniper", "marlin", "peridot", "umber", "vellum"}
	pick := func(n int) int64 {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
		if err != nil {
			t.Fatal(err)
		}
		return v.Int64()
	}
	return fmt.Sprintf("%s-%04d", words[pick(len(words))], pick(10000))
}

// runLive writes secret.txt with a fresh word into a workspace, builds an agent
// on the provider's STANDARD tier with read_file alone and bounds of 6 turns,
// $0.50 and 10 minutes, asks it for the word, and checks the run: it ended
// normally after a successful read_file call with the word in its answer, the
// agent reports the provider, tier, model and effort the catalog selects, the
// token counts are positive and priced, and the cost is positive on the paid
// services and zero on Ollama.
func runLive(t *testing.T, name string, sel catalog.Selection) {
	t.Helper()
	word := freshWord(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte(word+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	readFile, err := core.Named(all, "read_file")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := agentkit.New(agentkit.Config{
		Provider:   name,
		Model:      "STANDARD",
		Toolset:    agentkit.Toolset{Tools: readFile},
		MaxTurns:   6,
		MaxCostUSD: 0.50,
		Timeout:    10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("agentkit.New: %v", err)
	}
	res, runErr := agent.Run(context.Background(), "Read the file secret.txt and reply with the word it holds, and nothing else.")

	if res.StopReason != core.RunStopEndTurn || runErr != nil {
		t.Errorf("the run ended %q with the error %v, want %q and none", res.StopReason, runErr, core.RunStopEndTurn)
	}
	read := false
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolName == "read_file" && !tr.IsError {
			read = true
		}
	}
	if !read {
		t.Error("no read_file call succeeded")
	}
	if !strings.Contains(res.FinalText(), word) {
		t.Errorf("the answer %q does not contain the word %q from the file", res.FinalText(), word)
	}
	r := agent.Resolution()
	if r.Provider != name || r.Tier != catalog.TierStandard || r.Model.ID != sel.Model.ID || r.Effort != sel.Effort {
		t.Errorf("Agent.Resolution reports provider %q, tier %q, model %q, effort %q; want %q, %q, %q and %q",
			r.Provider, r.Tier, r.Model.ID, r.Effort, name, catalog.TierStandard, sel.Model.ID, sel.Effort)
	}
	u := res.Usage
	if u.InputTokens <= 0 || u.OutputTokens <= 0 {
		t.Errorf("the run used %d input and %d output tokens, want both positive", u.InputTokens, u.OutputTokens)
	}
	if u.Unpriced {
		t.Error("the run's usage is flagged unpriced")
	}
	if free := name == "ollama"; free && u.CostUSD != 0 {
		t.Errorf("the run cost $%g, want nothing for a model on the local machine", u.CostUSD)
	} else if !free && u.CostUSD <= 0 {
		t.Errorf("the run cost $%g, want a positive cost", u.CostUSD)
	}
	t.Logf("model %s, effort %s, %d turns, %d input and %d output tokens, $%.5f",
		r.Model.ID, r.Effort, res.TurnCount, u.InputTokens, u.OutputTokens, u.CostUSD)
}
