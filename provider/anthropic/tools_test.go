package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
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

// A schema the strict subset cannot express is sent as written, without the
// strict flag, rather than refused: the API takes it as an ordinary tool, and
// the tool's arguments are still validated when they arrive.
func TestUnconvertibleToolSchemaIsSentNonStrict(t *testing.T) {
	m := *testModel()
	dict := core.Tool{Name: "tally", InputSchema: &schema.Schema{Type: schema.TypeObject,
		AdditionalProperties: &schema.AdditionalProperties{Allowed: true, Schema: schema.Int()}},
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
	plain := sampleTools()[0]
	tools := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires([]core.Tool{dict, plain})}))
	if len(tools) != 2 {
		t.Fatalf("%d tools on the wire, want 2", len(tools))
	}
	if tools[0]["strict"] != false {
		t.Errorf("tally: strict = %v, want false", tools[0]["strict"])
	}
	in, _ := tools[0]["input_schema"].(map[string]any)
	if ap, _ := in["additionalProperties"].(map[string]any); ap["type"] != "integer" {
		t.Errorf("tally's schema was not sent as written: %v", in)
	}
	if tools[1]["strict"] != true {
		t.Errorf("search: strict = %v, want true", tools[1]["strict"])
	}
}

// optionalTool has n optional parameters, each a union once in the strict
// subset.
func optionalTool(name string, n int) core.Tool {
	var props []schema.Field
	for i := range n {
		props = append(props, schema.Opt("p"+string(rune('a'+i)), schema.String()))
	}
	return core.Tool{Name: name, InputSchema: schema.Object(props...),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
}

// Strict mode allows at most 16 union parameters across a request's strict
// tools, and refuses the whole request past it. Tools are declared strict in
// the order they are sent while they fit; one that does not is sent without
// the flag, and a later one that fits is still strict.
func TestStrictToolsStayWithinTheUnionLimit(t *testing.T) {
	m := *testModel()
	ts := []core.Tool{optionalTool("a", 7), optionalTool("b", 7), optionalTool("c", 7),
		optionalTool("d", 2), optionalTool("e", 0)}
	tools := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires(ts)}))
	want := map[string]bool{"a": true, "b": true, "c": false, "d": true, "e": true}
	for _, tl := range tools {
		if tl["strict"] != want[tl["name"].(string)] {
			t.Errorf("tool %v: strict = %v, want %v", tl["name"], tl["strict"], want[tl["name"].(string)])
		}
	}
	// The non-strict tool's schema is the same rewritten one it would have
	// been sent with as a strict tool.
	in, _ := tools[2]["input_schema"].(map[string]any)
	if in["additionalProperties"] != false {
		t.Errorf("c's schema = %v, want the strict rewrite", in)
	}
}

// At most 20 tools of a request may be strict.
func TestStrictToolsStayWithinTheToolLimit(t *testing.T) {
	m := *testModel()
	var ts []core.Tool
	for i := range 25 {
		ts = append(ts, optionalTool(fmt.Sprintf("t%02d", i), 0))
	}
	tools := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires(ts)}))
	strict := 0
	for i, tl := range tools {
		if tl["strict"] == true {
			strict++
		} else if i < 20 {
			t.Errorf("tool %v is not strict, but the first 20 fit", tl["name"])
		}
	}
	if strict != 20 {
		t.Errorf("%d strict tools, want 20", strict)
	}
}

// The file tools an agent is commonly given carry more optional parameters
// than strict mode allows in one request; the request still goes out within
// the limit.
func TestTheBuiltinToolsFitTheStrictLimits(t *testing.T) {
	m := *testModel()
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	built, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	wire := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires(built)}))
	unions, strict := 0, 0
	for _, tl := range wire {
		if tl["strict"] != true {
			continue
		}
		strict++
		unions += countUnions(tl["input_schema"])
	}
	if unions > 16 || strict > 20 {
		t.Errorf("%d strict tools with %d unions, over the limits", strict, unions)
	}
	if strict == 0 {
		t.Error("no tool is strict")
	}
}

// countUnions counts the anyOf and type-array nodes of a wire schema: what
// the API counts against its union limit.
func countUnions(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["anyOf"]; ok {
			n++
		}
		if _, ok := x["type"].([]any); ok {
			n++
		}
		if props, ok := x["properties"].(map[string]any); ok {
			for _, p := range props {
				n += countUnions(p)
			}
		}
		n += countUnions(x["items"])
		if as, ok := x["anyOf"].([]any); ok {
			for _, a := range as {
				n += countUnions(a)
			}
		}
	}
	return n
}

// The strict subset has no numeric, string-length or array-size bounds: the
// API answers 400 "properties maximum, minimum are not supported". The wire
// schema drops them and tells the model in the description instead; the tool's
// own schema keeps them, so the validator still enforces every bound.
func TestStrictWireSchemaDropsUnsupportedBounds(t *testing.T) {
	m := *testModel()
	bounded := core.Tool{Name: "convert", InputSchema: schema.Object(
		schema.Prop("value", schema.Int("amount").Min(1).Max(10)),
		schema.Prop("names", schema.Array(schema.String("a name")).MinItemsN(2)),
		schema.Prop("one", schema.Array(schema.String()).MinItemsN(1)),
		schema.Opt("limit", schema.Int("how many").Min(0))),
		Execute: func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }}
	tools := wireTools(t, wireOf(t, m, core.Request{Messages: userTurn(), Tools: core.ToolWires([]core.Tool{bounded})}))
	raw, _ := json.Marshal(tools[0]["input_schema"])
	for _, kw := range []string{`"minimum"`, `"maximum"`, `"minLength"`, `"maxLength"`, `"maxItems"`, `"multipleOf"`} {
		if strings.Contains(string(raw), kw) {
			t.Errorf("wire schema carries %s: %s", kw, raw)
		}
	}
	props := tools[0]["input_schema"].(map[string]any)["properties"].(map[string]any)
	if d, _ := props["value"].(map[string]any)["description"].(string); !strings.Contains(d, "amount") ||
		!strings.Contains(d, "minimum: 1") || !strings.Contains(d, "maximum: 10") {
		t.Errorf("value description = %q, want the text plus {minimum: 1, maximum: 10}", d)
	}
	if n, _ := props["names"].(map[string]any)["minItems"]; n != nil {
		t.Errorf("minItems 2 stayed on the wire: %v", n)
	}
	if n, _ := props["one"].(map[string]any)["minItems"].(float64); n != 1 {
		t.Errorf("minItems 1 is supported and must stay: %v", props["one"])
	}
	// The tool's own schema still carries the bounds for the validator.
	v := bounded.InputSchema.Properties["value"]
	if v.Minimum == nil || v.Maximum == nil || v.Description != "amount" {
		t.Errorf("the tool's schema was changed: %+v", v)
	}
	if err := schema.Validate(bounded.InputSchema, map[string]any{"value": 99, "names": []any{"a", "b"}, "one": []any{"x"}}); err == nil {
		t.Error("the validator no longer enforces maximum")
	}
}
