package tooldigest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

func randString(r *rand.Rand) string {
	const letters = "ab\x00:;{}\"x"
	b := make([]byte, r.IntN(6))
	for i := range b {
		b[i] = letters[r.IntN(len(letters))]
	}
	return string(b)
}

func randSchema(r *rand.Rand, depth int) *schema.Schema {
	switch n := r.IntN(5); {
	case n == 0 || depth > 2:
		return schema.String(randString(r))
	case n == 1:
		return schema.Int()
	case n == 2:
		return schema.Array(randSchema(r, depth+1))
	default:
		var fields []schema.Field
		for i := range r.IntN(3) {
			fields = append(fields, schema.Prop(fmt.Sprintf("p%d%s", i, randString(r)), randSchema(r, depth+1)))
		}
		return schema.Object(fields...)
	}
}

func randTool(r *rand.Rand) core.Tool {
	var g []string
	for range r.IntN(3) {
		g = append(g, randString(r))
	}
	return core.Tool{
		Name: randString(r), Description: randString(r),
		InputSchema: randSchema(r, 0), OutputSchema: randSchema(r, 0),
		PromptGuidelines: g, Builtin: r.IntN(2) == 0, ExecutionMode: core.ExecutionMode(r.IntN(2)),
	}
}

// mutate returns t with field f changed so that it differs from t.
func mutate(t core.Tool, f string) core.Tool {
	switch f {
	case "Name":
		t.Name += "~"
	case "Description":
		t.Description += "~"
	case "InputSchema":
		t.InputSchema = schema.Object(schema.Prop("changed~", t.InputSchema))
	case "OutputSchema":
		t.OutputSchema = schema.Object(schema.Prop("changed~", t.OutputSchema))
	case "PromptGuidelines":
		t.PromptGuidelines = append(slices.Clone(t.PromptGuidelines), "")
	case "Builtin":
		t.Builtin = !t.Builtin
	case "ExecutionMode":
		t.ExecutionMode = 1 - t.ExecutionMode
	}
	return t
}

// TS-13-37: the definition digest is equal exactly when name, description,
// schemas, guidelines, Builtin and execution mode are equal. 13-REQ-6.2.
func TestDigestProperty_TS13_37(t *testing.T) {
	r := rand.New(rand.NewPCG(37, 37))
	for range 1000 {
		tl := randTool(r)
		d := Digest(tl)
		if len(d) != 32 {
			t.Fatalf("digest has %d bytes, want a SHA-256", len(d))
		}
		clone := tl
		clone.PromptGuidelines = slices.Clone(tl.PromptGuidelines)
		if !bytes.Equal(d, Digest(clone)) {
			t.Fatalf("a copy of %+v has a different digest", tl)
		}
		other := tl
		other.Label = "label~"
		other.Execute = func(context.Context, json.RawMessage) core.ToolResult { return core.ToolResult{} }
		other.Handler = func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
		other.PrepareArguments = func(m map[string]any) map[string]any { return m }
		if !bytes.Equal(d, Digest(other)) {
			t.Fatal("fields outside the seven changed the digest")
		}
		for _, f := range []string{"Name", "Description", "InputSchema", "OutputSchema", "PromptGuidelines", "Builtin", "ExecutionMode"} {
			if bytes.Equal(d, Digest(mutate(tl, f))) {
				t.Fatalf("changing %s left the digest of %+v unchanged", f, tl)
			}
		}
	}
	// Moving a byte between adjacent fields must change the digest.
	a := core.Tool{Name: "ab", Description: "c"}
	b := core.Tool{Name: "a", Description: "bc"}
	if bytes.Equal(Digest(a), Digest(b)) {
		t.Error("the encoding is not injective across field boundaries")
	}

	// The host and the agent build tools.All separately; the digests agree.
	build := func() []core.Tool {
		ws, err := tools.NewWorkspace(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
		if err != nil {
			t.Fatal(err)
		}
		return all
	}
	h, g := build(), build()
	for i := range h {
		if !bytes.Equal(Digest(h[i]), Digest(g[i])) {
			t.Errorf("%s: digest differs between two tools.All builds", h[i].Name)
		}
	}
}
