package codemode

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// A tool is declared as the function a script calls: required keyword
// parameters before optional ones defaulting to None, each with its Starlark
// type, the output schema as the returned dict's shape, and the descriptions
// as a docstring that cannot be ended early.
func TestRenderSignature(t *testing.T) {
	obj := func(fields ...schema.Field) *schema.Schema { return schema.Object(fields...) }
	unordered := obj(schema.Prop("z", schema.Int()))
	unordered.Properties["a"] = schema.String()
	for _, tc := range []struct {
		name string
		tool core.Tool
		want string
	}{
		{"no schemas", core.Tool{Name: "ping"}, "def ping() -> Any:\n    \"\"\"\"\"\""},
		{"parameter types", core.Tool{Name: "f", Description: "Fetch\n a   URL.", InputSchema: obj(
			schema.Opt("o", schema.Bool()), schema.Prop("s", schema.String("The URL")), schema.Prop("i", schema.Int()),
			schema.Prop("n", schema.Number()), schema.Prop("l", schema.Array(schema.String())),
			schema.Prop("lo", schema.Array(obj())), schema.Prop("d", obj()), schema.Prop("u", &schema.Schema{}),
			schema.Prop("z", &schema.Schema{Type: schema.TypeNull}), schema.Prop("nil", nil))},
			"def f(s: str, i: int, n: float, l: list[str], lo: list, d: dict, u: Any, z: None, nil: Any, o: bool = None) -> Any:\n" +
				"    \"\"\"Fetch a URL.\n    s: The URL\"\"\""},
		{"unordered properties come last, sorted", core.Tool{Name: "u", InputSchema: unordered},
			"def u(z: int, a: str = None) -> Any:\n    \"\"\"\"\"\""},
		{"the returned shape", core.Tool{Name: "r", OutputSchema: obj(
			schema.Prop("status", schema.Int()), schema.Opt("binary", schema.Bool()),
			schema.Prop("headers", obj()), schema.Prop("lines", schema.Array(schema.String())),
			schema.Prop("items", schema.Array(obj(schema.Prop("k", schema.String())))),
			schema.Prop("deep", obj(schema.Prop("a", obj(schema.Prop("b", obj(schema.Prop("c", schema.Int())))))))),
		}, "def r() -> {status: int, binary: bool | None, headers: dict, lines: list[str], items: list[{k: str}], " +
			"deep: {a: {b: dict}}}:\n    \"\"\"\"\"\""},
		{"a union", core.Tool{Name: "o", OutputSchema: &schema.Schema{OneOf: []*schema.Schema{
			obj(schema.Prop("a", schema.Int())), schema.String()}}}, "def o() -> {a: int} | str:\n    \"\"\"\"\"\""},
		{"an anyOf", core.Tool{Name: "a", OutputSchema: schema.AnyOf(schema.Int(), schema.Bool())},
			"def a() -> int | bool:\n    \"\"\"\"\"\""},
		{"quotes in a docstring", core.Tool{Name: "q", Description: `says """hi"""`},
			"def q() -> Any:\n    \"\"\"says \\\"\\\"\\\"hi\\\"\\\"\\\"\"\"\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderSignature(tc.tool); got != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
