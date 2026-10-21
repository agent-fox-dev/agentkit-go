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

// The live smoke tests run one real tool loop on each provider against its live
// service. They spend money on Anthropic and OpenRouter and run a model on the
// machine for Ollama, so they need an explicit opt-in on top of each provider's
// prerequisites: many machines export a key or run ollama serve all the time.

// liveHint is where a skip message points.
const liveHint = "run make provider-live-test"

// tagsTimeout bounds the one request the Ollama prerequisite makes.
const tagsTimeout = 2 * time.Second

// TestLive is the live smoke test: one subtest for each provider, run on the
// STANDARD tier. It skips as a whole under -short and unless AGENTKIT_LIVE is
// exactly 1, before anything is contacted, and a provider whose prerequisites are
// missing skips its own subtest and says what is missing.
func TestLive(t *testing.T) {
	if testing.Short() {
		t.Skip("TestLive makes real requests and is skipped under -short; " + liveHint)
	}
	if v := os.Getenv("AGENTKIT_LIVE"); v != "1" {
		t.Skipf("AGENTKIT_LIVE is %q, not 1: the live provider tests spend money and run models; %s", v, liveHint)
	}
	for _, p := range providerTable {
		t.Run(p.name, func(t *testing.T) {
			livePrerequisites(t, p)
			runLive(t, p, liveOptions{})
		})
	}
}

// livePrerequisites skips t, naming what is missing, unless the provider can be
// reached: Anthropic resolves a deployment from the environment, OpenRouter has a
// key, and Ollama answers at its host with the model of the STANDARD tier.
func livePrerequisites(t *testing.T, p providerInfo) {
	t.Helper()
	switch p.name {
	case "anthropic":
		if _, _, err := anthropic.Resolve(anthropic.OSEnv{}); err != nil {
			t.Skipf("anthropic: %v", err)
		}
	case "openrouter":
		if os.Getenv("OPENROUTER_API_KEY") == "" {
			t.Skip("openrouter: OPENROUTER_API_KEY is not set")
		}
	case "ollama":
		sel, err := catalog.Select(p.name, "STANDARD", "")
		if err != nil {
			t.Fatal(err)
		}
		client, err := ollama.New(sel.Model, ollama.Options{})
		if err != nil {
			t.Skipf("ollama: %v", err)
		}
		host := client.Host()
		models, err := ollamaModels(host)
		if err != nil {
			t.Skipf("ollama: no server answers at %s (%v); start one with \"ollama serve\"", host, err)
		}
		if !hasModel(models, sel.Model.ID) {
			t.Skipf("ollama: the server at %s does not have %s; run \"ollama pull %s\"", host, sel.Model.ID, sel.Model.ID)
		}
	}
}

// ollamaModels asks the server at host which models it has: GET /api/tags, with
// tagsTimeout. A server that does not answer 200 with that list is an error.
func ollamaModels(host string) ([]ollamaModel, error) {
	client := &http.Client{Timeout: tagsTimeout}
	resp, err := client.Get(strings.TrimSuffix(host, "/") + "/api/tags")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/tags answered %d", resp.StatusCode)
	}
	var tags struct {
		Models []ollamaModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("GET /api/tags did not answer a list of models: %w", err)
	}
	return tags.Models, nil
}

// ollamaModel is a model /api/tags lists.
type ollamaModel struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// hasModel reports whether the list holds id, by name or by model.
func hasModel(models []ollamaModel, id string) bool {
	for _, m := range models {
		if m.Name == id || m.Model == id {
			return true
		}
	}
	return false
}

// liveOptions vary a live run. The zero value is the run TestLive makes.
type liveOptions struct {
	// word is the word the file holds; empty means a fresh random one.
	word string
	// configure may change the Config before the agent is built. TestLive passes
	// none, so the agent reads the process environment; a test of the run itself
	// points it at a fake.
	configure func(*agentkit.Config)
}

// words are what the random word is made from: unusual enough that a model
// cannot know it without the file.
var words = []string{"heliotrope", "quillon", "tamarind", "obsidian", "fennel", "cobalt", "sorrel", "juniper", "marlin", "peridot", "umber", "vellum"}

// freshWord is a word and a number nobody has seen, such as "tamarind-7203".
func freshWord() string {
	pick := func(n int64) int64 {
		v, err := rand.Int(rand.Reader, big.NewInt(n))
		if err != nil {
			panic(fmt.Sprintf("providerconformance: crypto/rand: %v", err))
		}
		return v.Int64()
	}
	return fmt.Sprintf("%s-%04d", words[pick(int64(len(words)))], pick(10000))
}

// liveRun is what a run left, for assertLiveRun.
type liveRun struct {
	res        core.RunResult
	err        error
	resolution agentkit.Resolution
	word       string
}

// runLive writes secret.txt with a random word into a temporary workspace, builds
// an agent on the provider's STANDARD tier with the built-in read_file tool alone
// and bounds of 6 turns, $0.50 and 10 minutes, asks it to read the file and reply
// with the word, and checks the run.
func runLive(t *testing.T, p providerInfo, o liveOptions) {
	t.Helper()
	word := o.word
	if word == "" {
		word = freshWord()
	}
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
	cfg := agentkit.Config{
		Provider:   p.name,
		Model:      "STANDARD",
		Toolset:    agentkit.Toolset{Tools: readFile},
		MaxTurns:   6,
		MaxCostUSD: 0.50,
		Timeout:    10 * time.Minute,
	}
	if o.configure != nil {
		o.configure(&cfg)
	}
	agent, err := agentkit.New(cfg)
	if err != nil {
		t.Fatalf("agentkit.New: %v", err)
	}
	res, runErr := agent.Run(context.Background(), "Read the file secret.txt and reply with the word it holds, and nothing else.")
	assertLiveRun(t, p, liveRun{res: res, err: runErr, resolution: agent.Resolution(), word: word})
}

// assertLiveRun requires the run to have ended normally with the word in its
// answer after a successful read_file call, the agent to report the provider, the
// tier and the model and effort the catalog selects, input and output tokens to
// be positive, the usage not to be flagged unpriced, and the cost to be positive
// on Anthropic and OpenRouter and zero on Ollama. It logs what the run spent.
func assertLiveRun(tb reporter, p providerInfo, run liveRun) {
	tb.Helper()
	res := run.res
	if res.StopReason != core.RunStopEndTurn || run.err != nil {
		tb.Errorf("%s: the run ended %q with the error %v, want %q and none", p.name, res.StopReason, run.err, core.RunStopEndTurn)
	}
	read := false
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolName == "read_file" && !tr.IsError {
			read = true
		}
	}
	if !read {
		tb.Errorf("%s: no read_file call succeeded", p.name)
	}
	if !strings.Contains(res.FinalText(), run.word) {
		tb.Errorf("%s: the answer %q does not contain the word %q from the file", p.name, res.FinalText(), run.word)
	}

	sel, err := catalog.Select(p.name, "STANDARD", "")
	if err != nil {
		tb.Errorf("%s: catalog.Select: %v", p.name, err)
		return
	}
	if r := run.resolution; r.Provider != p.name || r.Tier != catalog.TierStandard || r.Model.ID != sel.Model.ID || r.Effort != sel.Effort {
		tb.Errorf("%s: Agent.Resolution reports provider %q, tier %q, model %q, effort %q; want %q, %q, %q and %q",
			p.name, r.Provider, r.Tier, r.Model.ID, r.Effort, p.name, catalog.TierStandard, sel.Model.ID, sel.Effort)
	}

	u := res.Usage
	if u.InputTokens <= 0 || u.OutputTokens <= 0 {
		tb.Errorf("%s: the run used %d input and %d output tokens, want both positive", p.name, u.InputTokens, u.OutputTokens)
	}
	if u.Unpriced {
		tb.Errorf("%s: the run's usage is flagged unpriced", p.name)
	}
	if free := p.name == "ollama"; free && u.CostUSD != 0 {
		tb.Errorf("%s: the run cost $%g, want nothing for a model on the local machine", p.name, u.CostUSD)
	} else if !free && u.CostUSD <= 0 {
		tb.Errorf("%s: the run cost $%g, want a positive cost", p.name, u.CostUSD)
	}
	if l, ok := tb.(interface{ Logf(string, ...any) }); ok {
		l.Logf("%s: model %s, effort %s, %d turns, %d input and %d output tokens, $%.5f",
			p.name, run.resolution.Model.ID, run.resolution.Effort, res.TurnCount, u.InputTokens, u.OutputTokens, u.CostUSD)
	}
}
