package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// strictUnions mirrors the strict rewrite rather than running it; it must
// agree with what the rewrite actually sends, or the limit is miscounted.
func TestStrictUnionsAgreesWithTheRewrite(t *testing.T) {
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	built, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	extra := []*schema.Schema{
		schema.Object(schema.Opt("a", schema.String()), schema.Prop("b", schema.String().Nullable_()),
			schema.Opt("c", schema.Array(schema.Object(schema.Opt("d", schema.Int()))))),
		schema.Object(schema.Prop("u", &schema.Schema{AnyOf: []*schema.Schema{schema.String(), schema.Int()}})),
	}
	schemas := extra
	for _, b := range built {
		schemas = append(schemas, b.InputSchema)
	}
	for i, s := range schemas {
		raw, err := wireSchema(s)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		if got, want := strictUnions(s), wireUnions(v); got != want {
			t.Errorf("schema %d: strictUnions = %d, the rewrite sends %d: %s", i, got, want, raw)
		}
	}
}

func wireUnions(v any) int {
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
	if props, ok := x["properties"].(map[string]any); ok {
		for _, p := range props {
			n += wireUnions(p)
		}
	}
	n += wireUnions(x["items"])
	if as, ok := x["anyOf"].([]any); ok {
		for _, a := range as {
			n += wireUnions(a)
		}
	}
	return n
}
