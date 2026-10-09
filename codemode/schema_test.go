package codemode_test

import (
	"fmt"
	"math/rand"
	"regexp"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// TS-08-10: input properties map to Starlark types, and optional ones
// default to None, for any schema.
func TestRenderSignatureTypes_TS08_10(t *testing.T) {
	kinds := []struct {
		s    func() *schema.Schema
		want string
	}{
		{func() *schema.Schema { return schema.String("a string") }, "str"},
		{func() *schema.Schema { return schema.Int() }, "int"},
		{func() *schema.Schema { return schema.Number() }, "float"},
		{func() *schema.Schema { return schema.Bool() }, "bool"},
		{func() *schema.Schema { return schema.Array(schema.String()) }, "list"},
		{func() *schema.Schema { return schema.Object(schema.Prop("k", schema.Int())) }, "dict"},
	}
	r := rand.New(rand.NewSource(10))
	for i := range 50 {
		var fields []schema.Field
		want := map[string]string{}
		required := map[string]bool{}
		for j := range 1 + r.Intn(5) {
			k := kinds[r.Intn(len(kinds))]
			name := fmt.Sprintf("p%d", j)
			want[name] = k.want
			if r.Intn(2) == 0 {
				fields = append(fields, schema.Prop(name, k.s()))
				required[name] = true
			} else {
				fields = append(fields, schema.Opt(name, k.s()))
			}
		}
		tl := core.Tool{Name: fmt.Sprintf("tool_%d", i), Description: "d", InputSchema: schema.Object(fields...)}
		sig := codemode.RenderSignature(tl)
		head := strings.SplitN(sig, "\n", 2)[0]
		for name, typ := range want {
			// A list may name its item type: list[str].
			decl := `\b` + name + `: ` + typ + `(\[[a-z]+\])?`
			if required[name] && !regexp.MustCompile(decl+`[,)]`).MatchString(head) {
				t.Fatalf("iteration %d: %q lacks required %s: %s", i, head, name, typ)
			}
			if !required[name] && !regexp.MustCompile(decl+` = None`).MatchString(head) {
				t.Fatalf("iteration %d: %q lacks %s: %s = None", i, head, name, typ)
			}
		}
		// Required parameters come before optional ones, as Python requires.
		if idx := strings.Index(head, "= None"); idx >= 0 {
			for name := range required {
				if strings.Index(head, name+":") > idx {
					t.Fatalf("iteration %d: required %s after an optional parameter: %q", i, name, head)
				}
			}
		}
	}
}

// TS-08-11: the return type is the output schema's fields, or Any.
func TestRenderSignatureReturnType_TS08_11(t *testing.T) {
	with := core.Tool{Name: "fetch", Description: "Fetch a URL.",
		InputSchema: schema.Object(schema.Prop("url", schema.String("The URL"))),
		OutputSchema: schema.Object(schema.Prop("status", schema.Int()), schema.Prop("url", schema.String()),
			schema.Prop("body", schema.String()), schema.Opt("binary", schema.Bool()),
			schema.Prop("headers", schema.Object()), schema.Prop("lines", schema.Array(schema.String())))}
	sig := codemode.RenderSignature(with)
	for _, want := range []string{"def fetch(url: str)", "status: int", "url: str", "binary: bool", "headers: dict",
		"lines: list[str]", "Fetch a URL.", "url: The URL"} {
		if !strings.Contains(sig, want) {
			t.Errorf("signature lacks %q:\n%s", want, sig)
		}
	}
	without := core.Tool{Name: "ping", InputSchema: schema.Object()}
	if sig := codemode.RenderSignature(without); !strings.Contains(sig, "def ping() -> Any") {
		t.Fatalf("signature = %q, want an Any return", sig)
	}
	status := core.Tool{Name: "s", OutputSchema: schema.Object(schema.Prop("status", schema.String()))}
	if sig := codemode.RenderSignature(status); !strings.Contains(sig, "status: str") {
		t.Fatalf("signature = %q", sig)
	}
}
