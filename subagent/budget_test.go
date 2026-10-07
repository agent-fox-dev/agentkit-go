package subagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/internal/testkit"
)

// costlyChild is a factory whose child answers once at the given cost.
func costlyChild(cost float64) Factory {
	return func(context.Context) (*agentkit.Agent, error) {
		m := core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop}
		m.Usage.SetCost(cost)
		p := &testkit.Scripted{Turns: []core.AssistantMessage{m}}
		return agentkit.NewAgent(core.AgentConfig{Model: testkit.TestModel(),
			Providers: core.ProviderRegistry{testkit.TestAPI: p.Provider()}})
	}
}

func budgetRefusals(msgs core.Messages) int {
	n := 0
	for _, m := range msgs {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.IsError && strings.Contains(tr.Content.Text(), "budget") {
			n++
		}
	}
	return n
}

// Issue #82 §1: a child's spend reaches the parent's usage, so the budget a
// later delegation is granted is a fraction of what is actually left. Before,
// parent.Usage() never saw a child's cost and every delegation was granted a
// fraction of the whole budget.
func TestAChildsSpendCountsAgainstTheParent(t *testing.T) {
	call := func(id string) core.AssistantMessage {
		return testkit.AssistantWithTools(core.StopReasonToolUse, testkit.ToolUse(t, id, "specialist", `{"prompt":"go"}`))
	}
	parentProv := &testkit.Scripted{Turns: []core.AssistantMessage{
		call("c1"), call("c2"), call("c3"),
		{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
	}}
	parent := newTestAgent(t, parentProv, nil)
	_ = parent.RegisterTool(Tool(parent, costlyChild(0.9), Options{
		Name: "specialist", BudgetFraction: 1.0, MaxBudgetUSD: 1.0,
	}))
	res, err := parent.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if got := parent.Usage().CostUSD; got < 1.79 {
		t.Fatalf("parent.Usage().CostUSD = %v after two children spending 0.9 each; a child's spend must reach the parent", got)
	}
	if n := budgetRefusals(res.Messages); n != 1 {
		t.Fatalf("%d delegations refused for budget, want the third (1): two children already spent 1.8 of 1.0", n)
	}
}

// Delegations in ONE batch run in parallel. A child still running holds the
// slice it was granted, so a second delegation asking while the first is in
// flight is not also granted the same remaining budget.
func TestParallelDelegationsDoNotShareOneSlice(t *testing.T) {
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(1)
	var once sync.Once
	blocking := func(context.Context) (*agentkit.Agent, error) {
		prov := core.APIProvider{API: testkit.TestAPI,
			Stream: func(ctx context.Context, m *core.Model, _ core.Request, _ core.ProviderStreamOptions) *core.EventStream {
				st := core.NewEventStream(core.StreamOptions{})
				go func() {
					once.Do(started.Done)
					select {
					case <-release:
					case <-ctx.Done():
					}
					msg := core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "done"}},
						StopReason: core.StopReasonStop, Provider: m.Provider, API: m.API, Model: m.ID}
					msg.Usage.SetCost(0.1)
					st.End(core.StreamResult{Message: &msg})
				}()
				return st
			}}
		return agentkit.NewAgent(core.AgentConfig{Model: testkit.TestModel(),
			Providers: core.ProviderRegistry{testkit.TestAPI: prov}})
	}
	go func() {
		// Let both delegations reach their budget decision while the first
		// child is mid-run, then let it finish.
		started.Wait()
		time.Sleep(200 * time.Millisecond)
		close(release)
	}()

	parentProv := &testkit.Scripted{Turns: []core.AssistantMessage{
		testkit.AssistantWithTools(core.StopReasonToolUse,
			testkit.ToolUse(t, "c1", "specialist", `{"prompt":"a"}`),
			testkit.ToolUse(t, "c2", "specialist", `{"prompt":"b"}`)),
		{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
	}}
	parent := newTestAgent(t, parentProv, func(c *core.AgentConfig) { c.ParallelTools = true })
	_ = parent.RegisterTool(Tool(parent, blocking, Options{
		Name: "specialist", BudgetFraction: 1.0, MaxBudgetUSD: 1.0,
	}))
	res, err := parent.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if n := budgetRefusals(res.Messages); n != 1 {
		t.Fatalf("%d of two concurrent delegations refused; the first holds the whole remaining "+
			"budget (fraction 1.0) while it runs, so the second must be refused", n)
	}
}
