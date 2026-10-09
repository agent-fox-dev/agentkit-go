package agentkit

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

func toolTurns(t *testing.T) []core.AssistantMessage {
	return []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "echo", `{"v":"x"}`)),
	}
}

// Issue #79 §3: RunResult.Usage and StopContext.Usage are THIS run's usage;
// Agent.Usage is the lifetime aggregate.
func TestRunUsageIsPerRun(t *testing.T) {
	turn := func() core.AssistantMessage {
		m := core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop}
		m.Usage.SetField(core.UsageInputTokens, 100)
		return m
	}
	s := &scripted{turns: []core.AssistantMessage{turn(), turn()}}
	var seen []int64
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.StopPolicy = func(sc core.StopContext) bool { seen = append(seen, sc.Usage.InputTokens); return false }
	})
	for i := 0; i < 2; i++ {
		res, err := a.Run(context.Background(), "go")
		if err != nil {
			t.Fatal(err)
		}
		if res.Usage.InputTokens != 100 {
			t.Fatalf("run %d: RunResult.Usage.InputTokens = %d, want 100 (this run only)", i+1, res.Usage.InputTokens)
		}
	}
	if len(seen) != 2 || seen[0] != 100 || seen[1] != 100 {
		t.Fatalf("StopContext.Usage per run = %v, want [100 100]", seen)
	}
	if got := a.Usage().InputTokens; got != 200 {
		t.Fatalf("Agent.Usage().InputTokens = %d, want the lifetime 200", got)
	}
}

// Issue #79: a context transform that mutates a block in place does not
// rewrite stored history.
func TestATransformCannotRewriteStoredHistory(t *testing.T) {
	s := &scripted{}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.TransformContext = func(_ context.Context, msgs core.Messages) core.Messages {
			for _, m := range msgs {
				if um, ok := m.(core.UserMessage); ok && len(um.Content) > 0 {
					um.Content[0] = core.TextBlock{Text: "REWRITTEN"}
				}
			}
			return msgs
		}
	})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.history.Messages() {
		if um, ok := m.(core.UserMessage); ok && um.Content.Text() == "REWRITTEN" {
			t.Fatal("the transform's in-place edit reached stored history")
		}
	}
}

// Issue #79: an interceptor returning arguments that are not JSON (NaN) gets
// one invalid_arguments result, not a panic that ends the whole run.
func TestNonJSONInterceptorArgumentsFailOneCall(t *testing.T) {
	s := &scripted{turns: toolTurns(t)}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{echoTool("echo", nil)}
		c.BeforeToolCall = func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Arguments: map[string]any{"v": math.NaN()}}
		}
	})
	res, err := a.Run(context.Background(), "go")
	if err != nil || res.StopReason == core.RunStopError {
		t.Fatalf("run ended %q, %v; one bad interceptor value must not end the run", res.StopReason, err)
	}
	var found bool
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			found = tr.IsError && strings.Contains(tr.Content.Text(), "invalid_arguments")
		}
	}
	if !found {
		t.Fatal("want an invalid_arguments error result for the call")
	}
}

// Issue #79: a CustomTools entry with neither Handler nor Execute is refused
// at construction, as RegisterTool refuses it.
func TestACustomToolWithNoHandlerIsRefusedAtConstruction(t *testing.T) {
	for _, tl := range []core.Tool{
		{Name: "nothing"},
		{Name: "both", Handler: echoTool("x", nil).Handler,
			Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.ToolResult{} }},
	} {
		cfg := core.AgentConfig{Model: testModel()}
		cfg.ToolPolicy.CustomTools = []core.Tool{tl}
		if _, err := NewAgent(cfg); err == nil {
			t.Errorf("tool %q: NewAgent accepted it", tl.Name)
		}
		if _, err := NewAgentWithHistory(cfg, nil); err == nil {
			t.Errorf("tool %q: NewAgentWithHistory accepted it", tl.Name)
		}
	}
}
