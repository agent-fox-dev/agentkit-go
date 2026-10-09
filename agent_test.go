package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
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
