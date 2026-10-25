package anthropic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// optional is a tool with n optional parameters, each a union in the strict
// rewrite.
func optional(name string, n int) core.ToolWire {
	var props []schema.Field
	for i := range n {
		props = append(props, schema.Opt("p"+string(rune('a'+i)), schema.String()))
	}
	return core.ToolWire{Name: name, InputSchema: schema.Object(props...)}
}

// strictness renders each wire tool as name:strict.
func strictness(body map[string]any) string {
	var out []string
	for _, tl := range objects(body["tools"]) {
		out = append(out, fmt.Sprintf("%s:%v", tl["name"], tl["strict"]))
	}
	return strings.Join(out, " ")
}

// countUnions counts the anyOf and type-array nodes of a wire schema, which
// is what the API counts against its union limit.
func countUnions(v any) int {
	x, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	n := 0
	if _, ok := x["anyOf"]; ok {
		n++
	}
	if _, ok := x["type"].([]any); ok {
		n++
	}
	for _, p := range objects(mapValues(x["properties"])) {
		n += countUnions(p)
	}
	n += countUnions(x["items"])
	for _, a := range objects(x["anyOf"]) {
		n += countUnions(a)
	}
	return n
}

func mapValues(v any) any {
	m, _ := v.(map[string]any)
	out := make([]any, 0, len(m))
	for _, x := range m {
		out = append(out, x)
	}
	return out
}

// Tools are declared strict in the order they are sent while they fit the
// API's limits of 20 strict tools and 16 union parameters; one that does not
// fit, cannot be rewritten or is named in NonStrictTools goes without the
// flag and spends none of the budget.
func TestToolsAreDeclaredStrictWithinTheAPIsLimits(t *testing.T) {
	dict := core.ToolWire{Name: "tally", InputSchema: &schema.Schema{Type: schema.TypeObject,
		AdditionalProperties: &schema.AdditionalProperties{Allowed: true, Schema: schema.Int()}}}
	var many []core.ToolWire
	var manyWant []string
	for i := range 22 {
		many = append(many, optional(fmt.Sprintf("t%02d", i), 0))
		manyWant = append(manyWant, fmt.Sprintf("t%02d:%v", i, i < 20))
	}
	unions := []core.ToolWire{optional("a", 7), optional("b", 7), optional("c", 7), optional("d", 2), optional("e", 0)}
	cases := []struct {
		name      string
		tools     []core.ToolWire
		nonStrict []string
		want      string
	}{
		{"the union limit", unions, nil, "a:true b:true c:false d:true e:true"},
		{"a non-strict tool spends no budget", unions, []string{"a", "not_a_tool"}, "a:false b:true c:true d:true e:true"},
		{"the tool limit", many, nil, strings.Join(manyWant, " ")},
		{"a schema with no strict form", []core.ToolWire{dict, optional("plain", 1)}, nil, "tally:false plain:true"},
	}
	for _, c := range cases {
		body := wireBody(t, testModel(), core.Request{Tools: c.tools}, anthropic.Options{NonStrictTools: c.nonStrict})
		if got := strictness(body); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
		// Whether a tool is strict never changes its schema bytes.
		for _, tl := range objects(body["tools"]) {
			in := tl["input_schema"].(map[string]any)
			if tl["name"] != "tally" && in["additionalProperties"] != false {
				t.Errorf("%s: %v was not sent in the strict rewrite: %v", c.name, tl["name"], in)
			}
			if tl["name"] == "tally" && in["additionalProperties"].(map[string]any)["type"] != "integer" {
				t.Errorf("%s: tally was not sent as written: %v", c.name, in)
			}
		}
	}
}

// The wire schema is the strict rewrite with the bounds strict mode refuses
// moved into the description; the tool's own schema, which the validator
// reads, keeps them, and nothing but the input schema reaches the wire.
func TestTheWireSchemaIsTheStrictRewriteOfTheToolsSchema(t *testing.T) {
	tool := core.Tool{Name: "convert", Description: "convert a value",
		InputSchema: schema.Object(
			schema.Prop("value", schema.Int("amount").Min(1).Max(10)),
			schema.Prop("names", schema.Array(schema.String()).MinItemsN(2)),
			schema.Prop("one", schema.Array(schema.String()).MinItemsN(1)),
			schema.Prop("when", &schema.Schema{Type: schema.TypeString, Format: "date-time"}),
			schema.Prop("color", &schema.Schema{Type: schema.TypeString, Format: "hex-color", MaxLength: new(7)}),
			schema.Prop("tags", &schema.Schema{Type: schema.TypeArray, Items: schema.String(), UniqueItems: new(true)})),
		OutputSchema:   schema.Object(schema.Prop("hits", schema.Int())),
		ReachableTools: []core.Tool{{Name: "child", InputSchema: schema.Object()}},
		Terminating:    true,
		Execute:        func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) },
	}
	body := wireBody(t, testModel(), core.Request{Tools: core.ToolWires([]core.Tool{tool})}, anthropic.Options{})
	tl := objects(body["tools"])[0]
	if len(tl) != 5 || tl["description"] != "convert a value" || tl["strict"] != true {
		t.Fatalf("tool = %v, want name, description, input_schema, strict and cache_control only", tl)
	}
	in := tl["input_schema"].(map[string]any)
	props := in["properties"].(map[string]any)
	desc := func(p string) string { d, _ := props[p].(map[string]any)["description"].(string); return d }
	if req, _ := in["required"].([]any); in["additionalProperties"] != false || len(req) != 6 {
		t.Errorf("schema = %v, want closed with every property required", in)
	}
	for p, want := range map[string]string{
		"value": "amount\n\n{minimum: 1, maximum: 10}", "names": "{minItems: 2}", "one": "",
		"when": "", "color": "{maxLength: 7, format: hex-color}", "tags": "{uniqueItems: true}",
	} {
		if got := desc(p); got != want {
			t.Errorf("%s description = %q, want %q", p, got, want)
		}
	}
	raw, _ := json.Marshal(in)
	for _, kw := range []string{`"minimum"`, `"maximum"`, `"maxLength"`, `"uniqueItems"`, `"hex-color"`, `"minItems":2`} {
		if strings.Contains(string(raw), kw) {
			t.Errorf("wire schema carries %s: %s", kw, raw)
		}
	}
	if !strings.Contains(string(raw), `"minItems":1`) || !strings.Contains(string(raw), `"date-time"`) {
		t.Errorf("a supported bound or format was dropped: %s", raw)
	}
	if v := tool.InputSchema.Properties["value"]; v.Maximum == nil || v.Description != "amount" ||
		tool.InputSchema.AdditionalProperties != nil {
		t.Errorf("the tool's own schema was changed: %+v", tool.InputSchema)
	}
}

// The union count strict mode is held to is the wire schema's (a tool padded
// to 16 unions beside it is strict, to 17 is not), and the built-in file
// tools fit the limits together.
func TestTheUnionCountMatchesTheWireSchema(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	built, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	schemas := []*schema.Schema{
		schema.Object(schema.Opt("a", schema.String()), schema.Prop("b", schema.String().Nullable_()),
			schema.Opt("c", schema.Array(schema.Object(schema.Opt("d", schema.Int()))))),
		schema.Object(schema.Prop("u", &schema.Schema{AnyOf: []*schema.Schema{schema.String(), schema.Int()}})),
	}
	for _, b := range built {
		schemas = append(schemas, b.InputSchema)
	}
	for i, s := range schemas {
		alone := wireBody(t, testModel(), core.Request{Tools: []core.ToolWire{{Name: "s", InputSchema: s}}}, anthropic.Options{})
		tl := objects(alone["tools"])[0]
		n := countUnions(tl["input_schema"])
		if n > 16 || tl["strict"] != true {
			if tl["strict"] == true {
				t.Errorf("schema %d: %d unions alone, but strict", i, n)
			}
			continue
		}
		for pad, want := range map[int]string{16 - n: "s:true pad:true", 17 - n: "s:true pad:false"} {
			body := wireBody(t, testModel(), core.Request{Tools: []core.ToolWire{{Name: "s", InputSchema: s}, optional("pad", pad)}},
				anthropic.Options{})
			if got := strictness(body); got != want {
				t.Errorf("schema %d (%d unions) beside %d: %s, want %s", i, n, pad, got, want)
			}
		}
	}

	body := wireBody(t, testModel(), core.Request{Tools: core.ToolWires(built)}, anthropic.Options{})
	unions, strict := 0, 0
	for _, tl := range objects(body["tools"]) {
		if tl["strict"] == true {
			strict++
			unions += countUnions(tl["input_schema"])
		}
	}
	if unions > 16 || strict > 20 || strict == 0 {
		t.Errorf("the built-in tools: %d strict with %d unions", strict, unions)
	}
}
