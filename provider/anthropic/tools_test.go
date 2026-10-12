package anthropic_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
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

// A strict tool's schema must be in the strict subset, or the API answers 400
// "additionalProperties must be explicitly set to false". schema.Object leaves
// the keyword unset, so the encoder is what closes it.
func TestToolSchemaIsProjectedIntoStrictSubset(t *testing.T) {
	m := *testModel()
	open := core.Tool{Name: "lookup", InputSchema: schema.Object(
		schema.Prop("ids", schema.Array(schema.String())),
		schema.Opt("verbose", schema.Bool("more detail"))),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
	tools := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires([]core.Tool{open})}))
	in, _ := tools[0]["input_schema"].(map[string]any)
	if in["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false: %v", in["additionalProperties"], in)
	}
	if req, _ := in["required"].([]any); len(req) != 2 {
		t.Errorf("required = %v, want both properties listed", in["required"])
	}
	// The tool's own schema is shared with the validator and must not change.
	if open.InputSchema.AdditionalProperties != nil || len(open.InputSchema.Required) != 1 {
		t.Errorf("the tool's schema was mutated: %+v", open.InputSchema)
	}
}

// A schema the strict subset cannot express is refused by name, not sent to
// be rejected by the API.
func TestUnconvertibleToolSchemaNamesTheTool(t *testing.T) {
	m := *testModel()
	dict := core.Tool{Name: "tally", InputSchema: &schema.Schema{Type: schema.TypeObject,
		AdditionalProperties: &schema.AdditionalProperties{Allowed: true, Schema: schema.Int()}},
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
	_, _, err := anthropic.BuildRequest(&m, core.Request{Messages: userTurn(), Tools: core.ToolWires([]core.Tool{dict})}, core.CacheRetentionNone)
	if err == nil || !strings.Contains(err.Error(), "tally") {
		t.Fatalf("err = %v, want one naming the tool \"tally\"", err)
	}
}
