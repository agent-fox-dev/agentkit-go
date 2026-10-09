package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

func noopHandler(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }

// agentCfg is a minimal valid config carrying tools as custom tools.
func agentCfg(tools ...core.Tool) core.AgentConfig {
	s := &scripted{}
	return core.AgentConfig{
		Model:      testModel(),
		StopPolicy: afterTurns(3),
		Providers:  core.ProviderRegistry{testAPI: s.provider()},
		ToolPolicy: core.ToolPolicy{CustomTools: tools},
	}
}

// cyclicPair builds toolA -> toolB -> toolA. Tools are values, so the cycle
// exists through the shared backing array of toolA.ReachableTools.
func cyclicPair() core.Tool {
	var toolA, toolB core.Tool
	toolA = core.Tool{Name: "toolA", Handler: noopHandler, ReachableTools: []core.Tool{toolB}}
	toolB = core.Tool{Name: "toolB", Handler: noopHandler, ReachableTools: []core.Tool{toolA}}
	toolA.ReachableTools[0] = toolB
	return toolA
}

// TS-07-3: a reachability cycle is refused by NewAgent, NewAgentWithHistory
// and RegisterTool, naming the path.
func TestCycleInReachableToolsIsRefused_TS07_3(t *testing.T) {
	const want = "agentkit: reachable tools cycle detected: toolA -> toolB -> toolA"
	if _, err := NewAgent(agentCfg(cyclicPair())); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewAgent err = %v, want %q", err, want)
	}
	if _, err := NewAgentWithHistory(agentCfg(cyclicPair()), nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewAgentWithHistory err = %v, want %q", err, want)
	}
	ag, err := NewAgent(agentCfg())
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.RegisterTool(cyclicPair()); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RegisterTool err = %v, want %q", err, want)
	}
	// A tool that reaches itself directly is the shortest cycle.
	self := core.Tool{Name: "self", Handler: noopHandler, ReachableTools: make([]core.Tool, 1)}
	self.ReachableTools[0] = self
	if err := ag.RegisterTool(self); err == nil ||
		!strings.Contains(err.Error(), "agentkit: reachable tools cycle detected: self -> self") {
		t.Fatalf("RegisterTool(self) err = %v", err)
	}
}

// TS-07-4: a wrapper may not reach a terminating tool.
func TestTerminatingToolReachedThroughWrapperIsRefused_TS07_4(t *testing.T) {
	term := core.Tool{Name: "finish", Terminating: true, Handler: noopHandler}
	wrap := core.Tool{Name: "code_mode", ReachableTools: []core.Tool{term}, Handler: noopHandler}
	const want = `agentkit: terminating tool "finish" cannot be reached through wrapper "code_mode"`
	if _, err := NewAgent(agentCfg(wrap)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewAgent err = %v, want %q", err, want)
	}
	if _, err := NewAgentWithHistory(agentCfg(wrap), nil); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewAgentWithHistory err = %v, want %q", err, want)
	}
	ag, err := NewAgent(agentCfg())
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.RegisterTool(wrap); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("RegisterTool err = %v, want %q", err, want)
	}
	// Transitively: the wrapper that declares the terminating tool is named.
	outer := core.Tool{Name: "outer", ReachableTools: []core.Tool{wrap}, Handler: noopHandler}
	if _, err := NewAgent(agentCfg(outer)); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("NewAgent(outer) err = %v, want %q", err, want)
	}
	// A terminating tool registered directly is fine.
	if _, err := NewAgent(agentCfg(term)); err != nil {
		t.Fatalf("a top-level terminating tool was refused: %v", err)
	}
}

// TS-07-5: diamonds — the same tool reached along two paths — are not
// cycles, at any shape and depth.
func TestReachableDiamondIsNotACycle_TS07_5(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for i := range 40 {
		// A layered DAG: every tool reaches only tools in deeper layers, and
		// at least one tool is reached along two paths.
		layers := 2 + r.Intn(3)
		width := 2 + r.Intn(3)
		var below []core.Tool
		for l := layers; l >= 0; l-- {
			var level []core.Tool
			for w := range width {
				tl := core.Tool{Name: fmt.Sprintf("t%d_%d", l, w), Handler: noopHandler}
				if len(below) > 0 {
					tl.ReachableTools = []core.Tool{below[0], below[r.Intn(len(below))]}
				}
				level = append(level, tl)
			}
			below = level
		}
		root := core.Tool{Name: "root", Handler: noopHandler, ReachableTools: below}
		ag, err := NewAgent(agentCfg(root))
		if err != nil || ag == nil {
			t.Fatalf("iteration %d: diamond refused: %v", i, err)
		}
	}
}

// TS-09-22 (smoke, 09-PATH-3): an embedder registers the retained providers,
// builds an Agent with no session store or middleware, and runs a turn; the
// result carries the provider's usage and the conversation is in history.
func TestSmokeAgentRunsWithRetainedProviders_TS09_22(t *testing.T) {
	p := faux.New(faux.Turn{
		Blocks:     []core.ContentBlock{faux.FauxText("hello back")},
		StopReason: core.StopReasonStop,
		Usage:      core.Usage{InputTokens: 12, OutputTokens: 3},
	})
	cfg := core.AgentConfig{Model: faux.Model()}
	RegisterDefaults(&cfg, anthropic.Provider(anthropic.Options{}), p.APIProvider())
	agent, err := NewAgent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var events []core.Event
	st, err := agent.Stream(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	for e := range st.Events() {
		events = append(events, e)
	}
	res, err := st.RunResult()
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage.InputTokens <= 0 && res.Usage.OutputTokens <= 0 {
		t.Fatalf("run usage = %+v, want the provider's tokens", res.Usage)
	}
	if res.FinalText() != "hello back" || res.StopReason != core.RunStopEndTurn {
		t.Fatalf("result = %q / %q", res.FinalText(), res.StopReason)
	}
	if n := agent.History().Len(); n != 2 {
		t.Fatalf("history holds %d messages, want the prompt and the reply", n)
	}
	if len(events) == 0 {
		t.Fatal("the run emitted no events")
	}
	if _, ok := events[0].(core.AgentStartEvent); !ok {
		t.Fatalf("first event = %T, want AgentStartEvent", events[0])
	}
}

// driverModel is the spec's model id. The catalog does not list it, so it
// takes the default row: a 1M-token window and no price.
const driverModel = "claude-3-5-sonnet"

func allowAll(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
	return core.BeforeToolCallDecision{}
}

func noopAfter(context.Context, core.AfterToolCallContext) core.AfterToolCallDecision {
	return core.AfterToolCallDecision{}
}

func userText(s string) core.UserMessage {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

// TS-11-1: Config carries the fifteen driver fields.
func TestConfigDefinesTheDriverFields_TS11_1(t *testing.T) {
	cfg := Config{
		Client:     &sdk.Client{},
		Provider:   faux.New(),
		Model:      driverModel,
		Effort:     EffortHigh,
		System:     "system instruction",
		Prefix:     []core.Message{userText("preamble")},
		Tools:      []core.Tool{{Name: "toolA", Handler: noopHandler}},
		Policy:     core.ToolPolicy{},
		Guard:      allowAll,
		After:      noopAfter,
		MaxTurns:   10,
		MaxCostUSD: 1.5,
		Timeout:    30 * time.Second,
		Prune:      PruneOptions{Threshold: 0.35, KeepTurns: 2},
		MaxTokens:  4096,
	}
	if cfg.Model != driverModel || cfg.Effort != core.EffortHigh || cfg.MaxTurns != 10 ||
		cfg.MaxCostUSD != 1.5 || cfg.Timeout != 30*time.Second || cfg.MaxTokens != 4096 ||
		cfg.Prune.KeepTurns != 2 || len(cfg.Prefix) != 1 || len(cfg.Tools) != 1 {
		t.Fatalf("config = %+v", cfg)
	}
}

// TS-11-2: no client and no provider is refused.
func TestNewRefusesNoClientOrProvider_TS11_2(t *testing.T) {
	a, err := New(Config{Model: driverModel})
	if a != nil || err == nil || !strings.Contains(err.Error(), "client or provider") {
		t.Fatalf("New = %v, %v; want nil and an error naming client or provider", a, err)
	}
}

// TS-11-3: an empty model is refused.
func TestNewRefusesEmptyModel_TS11_3(t *testing.T) {
	a, err := New(Config{Provider: faux.New()})
	if a != nil || err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("New = %v, %v; want nil and an error naming the model", a, err)
	}
}

// TS-11-4: an invalid tool hierarchy is refused.
func TestNewRefusesInvalidTools_TS11_4(t *testing.T) {
	exec := func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }
	done := core.Tool{Name: "done", Handler: noopHandler, Terminating: true}
	cases := map[string][]core.Tool{
		"both":    {{Name: "both", Handler: noopHandler, Execute: exec}},
		"neither": {{Name: "neither"}},
		"cycle":   {cyclicPair()},
		"wrapper": {done, {Name: "wrap", Handler: noopHandler, ReachableTools: []core.Tool{done}}},
	}
	for name, tools := range cases {
		a, err := New(Config{Provider: faux.New(), Model: "m", Tools: tools})
		if a != nil || err == nil {
			t.Errorf("%s: New = %v, %v; want nil and an error", name, a, err)
		}
	}
}

// TS-11-5: the policy resolves the tools, and Provider wins over Client.
func TestNewResolvesToolsAndPrefersProvider_TS11_5(t *testing.T) {
	clientUsed := false
	client := sdk.NewClient(option.WithoutEnvironmentDefaults(), option.WithAPIKey("sk-ant-test"),
		option.WithMiddleware(func(r *http.Request, _ option.MiddlewareNext) (*http.Response, error) {
			clientUsed = true
			return nil, errors.New("the client must not be used")
		}))
	fp := faux.New()
	a, err := New(Config{
		Client:   &client,
		Provider: fp,
		Model:    driverModel,
		Tools:    []core.Tool{{Name: "toolA", Handler: noopHandler}, {Name: "toolB", Handler: noopHandler}},
		Policy:   core.ToolPolicy{ToolNames: []string{"toolA"}},
	})
	if err != nil || a == nil {
		t.Fatalf("New = %v, %v", a, err)
	}
	if got := a.ReachableTools(); len(got) != 1 || got[0].Name != "toolA" {
		t.Fatalf("ReachableTools = %v, want [toolA]", got)
	}
	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if fp.Calls() == 0 || clientUsed {
		t.Fatalf("provider calls = %d, client used = %v; want the provider only", fp.Calls(), clientUsed)
	}
}

// TS-11-6: a resolved shell tool with no Guard is refused at construction.
func TestNewRefusesUnguardedShellTool_TS11_6(t *testing.T) {
	a, err := New(Config{Provider: faux.New(), Model: driverModel,
		Tools: []core.Tool{{Name: "execute", Handler: noopHandler}}})
	if a != nil || !errors.Is(err, core.ErrUnguardedExecute) || !strings.Contains(err.Error(), `"execute"`) {
		t.Fatalf("New = %v, %v; want ErrUnguardedExecute naming execute", a, err)
	}
}

// TS-11-7: a shell tool behind a wrapper with no Guard is refused, naming
// both. The spec's "bash" is not in guard.ShellToolNames; run_command is.
func TestNewRefusesUnguardedShellBehindWrapper_TS11_7(t *testing.T) {
	shell := core.Tool{Name: "run_command", Handler: noopHandler}
	wrapper := core.Tool{Name: "codemode", Handler: noopHandler, ReachableTools: []core.Tool{shell}}
	a, err := New(Config{Provider: faux.New(), Model: driverModel, Tools: []core.Tool{wrapper}})
	if a != nil || !errors.Is(err, core.ErrUnguardedExecute) ||
		!strings.Contains(err.Error(), `wrapper "codemode"`) || !strings.Contains(err.Error(), `shell tool "run_command"`) {
		t.Fatalf("New = %v, %v; want ErrUnguardedExecute naming codemode and run_command", a, err)
	}
}

// TS-11-8: a Guard admits a reachable shell tool.
func TestNewAcceptsGuardedShellTool_TS11_8(t *testing.T) {
	a, err := New(Config{Provider: faux.New(), Model: driverModel,
		Tools: []core.Tool{{Name: "execute", Handler: noopHandler}}, Guard: guard.AllowAll})
	if err != nil || a == nil {
		t.Fatalf("New = %v, %v; want an agent", a, err)
	}
}
