package anthropic_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func wireTools(t *testing.T, w map[string]any) []map[string]any {
	t.Helper()
	raw, _ := w["tools"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		out = append(out, x.(map[string]any))
	}
	return out
}

func sampleTools() []core.Tool {
	child := core.Tool{Name: "child", InputSchema: schema.Object(),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
	return []core.Tool{
		{Name: "search", Description: "search the code", InputSchema: schema.Object(schema.Prop("q", schema.String())),
			OutputSchema: schema.Object(schema.Prop("hits", schema.Int())), ReachableTools: []core.Tool{child},
			Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }},
		{Name: "edit", Description: "edit a file", InputSchema: schema.Object(schema.Prop("path", schema.String())),
			Terminating: true,
			Execute:     func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }},
	}
}

// TS-10-20: every tool on the wire is strict.
func TestEveryToolIsStrict_TS10_20(t *testing.T) {
	m := *testModel()
	tools := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires(sampleTools())}))
	if len(tools) != 2 {
		t.Fatalf("%d tools on the wire, want 2", len(tools))
	}
	for _, tl := range tools {
		if tl["strict"] != true {
			t.Errorf("tool %v: strict = %v, want true", tl["name"], tl["strict"])
		}
	}
}

// TS-10-21: a tool's output schema, reachable tools and terminating flag
// never reach the wire.
func TestInternalToolFieldsAreStripped_TS10_21(t *testing.T) {
	m := *testModel()
	w := wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires(sampleTools())})
	raw, err := json.Marshal(w["tools"])
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"OutputSchema", "output_schema", "outputSchema", "ReachableTools",
		"reachable_tools", "Terminating", "terminating", "hits", "child"} {
		if strings.Contains(string(raw), field) {
			t.Errorf("the wire tools carry %q: %s", field, raw)
		}
	}
}

// TS-10-22: no forced tool choice reaches the wire; tool_choice is auto or
// absent.
func TestNoForcedToolChoice_TS10_22(t *testing.T) {
	m := *testModel()
	for _, choice := range []core.ToolChoice{core.ToolChoiceUnset, core.ToolChoiceAuto,
		core.ToolChoice("any"), core.ToolChoice("tool"), core.ToolChoice("search")} {
		w := wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires(sampleTools()), ToolChoice: choice})
		tc, present := w["tool_choice"]
		if !present {
			continue
		}
		obj, _ := tc.(map[string]any)
		if obj == nil || obj["type"] != "auto" || obj["name"] != nil {
			t.Errorf("tool choice %q went out as %v, want auto or nothing", choice, tc)
		}
	}
}
