package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/schema"
)

func TestPrepareArgumentsPassesTheModelsBytesThroughWhenNothingChanged(t *testing.T) {
	in := `{"zeta": "test",  "limit":10}`
	tool := Tool{Name: "search", InputSchema: schema.Object(schema.Prop("zeta", schema.String()), schema.Opt("limit", schema.Int()))}
	p, err := PrepareArguments(tool, ToolUseBlock{ID: "t1", Name: "search", Input: json.RawMessage(in)})
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Raw) != in || p.Args["zeta"] != "test" || p.Args["limit"] != json.Number("10") || p.Coercions != nil {
		t.Fatalf("PrepareArguments = %s, %v, %v; want the input bytes and json.Number values", p.Raw, p.Args, p.Coercions)
	}
	// Without a schema there is nothing to validate.
	if p, err := PrepareArguments(Tool{Name: "free"}, toolUse(t, "t", "free", in)); err != nil || string(p.Raw) != in {
		t.Fatalf("schemaless PrepareArguments = %s, %v", p.Raw, err)
	}
}

func TestPrepareArgumentsRejectsInputThatIsNotAJSONObject(t *testing.T) {
	for _, in := range []string{`[1]`, `{"a":`, `{"a":1}}`} {
		if p, err := PrepareArguments(Tool{Name: "t"}, ToolUseBlock{Input: json.RawMessage(in)}); err == nil || p.Args != nil {
			t.Errorf("PrepareArguments(%s) = %+v, %v; want an error", in, p, err)
		}
	}
}

func TestPrepareArgumentsDeletesAnOptionalNullAndKeepsARequiredOne(t *testing.T) {
	tool := Tool{Name: "t", InputSchema: schema.Object(schema.Prop("path", schema.String()), schema.Opt("limit", schema.Int()))}
	p, err := PrepareArguments(tool, toolUse(t, "c", "t", `{"path":"/x", "limit":null}`))
	if err != nil {
		t.Fatalf("an explicit null for an optional property must be deleted, not rejected: %v", err)
	}
	if _, present := p.Args["limit"]; present || string(p.Raw) != `{"path":"/x"}` {
		t.Fatalf("Args = %v, Raw = %s; want limit gone from both", p.Args, p.Raw)
	}
	_, err = PrepareArguments(tool, toolUse(t, "c", "t", `{"path":null}`))
	if err == nil || !strings.Contains(err.Error(), "path: expected string, got null") {
		t.Fatalf("a null for a required property = %v, want it reported as the wrong type", err)
	}
}

func TestPrepareArgumentsCoercesAnIntegerWithTrailingSeparators(t *testing.T) {
	tool := Tool{Name: "read_file", InputSchema: schema.Object(schema.Prop("path", schema.String()), schema.Opt("offset", schema.Int()))}
	for in, want := range map[string]string{`"1, "`: "1", `"120"`: "120", `" 7 "`: "7", `"30;"`: "30"} {
		p, err := PrepareArguments(tool, toolUse(t, "c", "read_file", `{"path":"a.go","offset":`+in+`}`))
		if err != nil {
			t.Errorf("offset %s: %v", in, err)
			continue
		}
		if got := fmt.Sprint(p.Args["offset"]); got != want || string(p.Raw) != `{"path":"a.go","offset":`+want+`}` {
			t.Errorf("offset %s = %v, Raw %s; want %s", in, p.Args["offset"], p.Raw, want)
		}
		if len(p.Coercions) != 1 || p.Coercions[0].Path != "offset" {
			t.Errorf("offset %s coercions = %+v", in, p.Coercions)
		}
	}
}

func TestPrepareArgumentsRefusesInvalidArgumentsEchoingTheModelsBytes(t *testing.T) {
	tool := Tool{Name: "read_file", InputSchema: schema.Object(schema.Prop("path", schema.String()), schema.Opt("offset", schema.Int()))}
	p, err := PrepareArguments(tool, toolUse(t, "c", "read_file", `{"zeta":1,"offset":"1-50","alpha":2}`))
	if !errors.Is(err, schema.ErrArgumentValidation) || len(p.Raw) != 0 || p.Args != nil {
		t.Fatalf("PrepareArguments = %+v, %v; want nothing and a validation error", p, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"1-50"`) || !strings.Contains(msg, "pass a number") {
		t.Errorf("the message must quote the value and say to pass a number:\n%s", msg)
	}
	if !strings.HasSuffix(msg, `{"zeta":1,"offset":"1-50","alpha":2}`) {
		t.Errorf("the echo is not the model's own bytes in its own key order:\n%s", msg)
	}
}

func TestPrepareArgumentsEchoesALongArgumentOnceAndAbbreviated(t *testing.T) {
	tool := Tool{Name: "edit_file", InputSchema: schema.Object(
		schema.Prop("path", schema.String()),
		schema.Prop("edits", schema.Array(schema.Object(schema.Prop("old_string", schema.String())))))}
	args, _ := json.Marshal(map[string]any{"path": "p.go", "edits": "[{\"old_string\">" + strings.Repeat("x", 1000)})
	_, err := PrepareArguments(tool, toolUse(t, "c", "edit_file", string(args)))
	if err == nil {
		t.Fatal("want a validation error")
	}
	msg := err.Error()
	if strings.Count(msg, "p.go") != 1 || len(msg) > 600 || !strings.Contains(msg, "not a JSON string") {
		t.Fatalf("want one abbreviated echo that says the array came as a string; got %d bytes:\n%s", len(msg), msg)
	}
}

func TestPrepareArgumentsRunsTheToolsRepairFirstOnACopy(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		repair         func(map[string]any) map[string]any
	}{
		{"an added key follows, in lexical order", `{"v": 1}`, `{"v": 1,"a":0,"injected":true}`,
			func(a map[string]any) map[string]any { a["injected"], a["a"] = true, 0; return a }},
		{"a nested edit in place is a change", `{"o": {"k":1}, "l":[{"m":1}]}`, `{"o":{"k":2},"l":[{"m":1}]}`,
			func(a map[string]any) map[string]any { a["o"].(map[string]any)["k"] = 2; return a }},
		{"a nil repair changes nothing", `{"v": 1}`, `{"v": 1}`,
			func(a map[string]any) map[string]any { a["v"] = 9; return nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := PrepareArguments(Tool{Name: "t", PrepareArguments: tc.repair}, toolUse(t, "c", "t", tc.in))
			if err != nil || string(p.Raw) != tc.want {
				t.Fatalf("Raw = %s, %v; want %s", p.Raw, err, tc.want)
			}
		})
	}
	// The repair runs before validation, so it can fix what validation would refuse.
	tool := Tool{Name: "t", InputSchema: schema.Object(schema.Prop("n", schema.Int())),
		PrepareArguments: func(a map[string]any) map[string]any { a["n"] = a["count"]; delete(a, "count"); return a }}
	if p, err := PrepareArguments(tool, toolUse(t, "c", "t", `{"count":3}`)); err != nil || string(p.Raw) != `{"n":3}` {
		t.Fatalf("repaired call = %s, %v", p.Raw, err)
	}
	// A repair that produces a value JSON cannot carry fails the call.
	bad := Tool{Name: "t", PrepareArguments: func(a map[string]any) map[string]any { a["x"] = math.NaN(); return a }}
	if _, err := PrepareArguments(bad, toolUse(t, "c", "t", `{}`)); err == nil || !strings.Contains(err.Error(), `argument "x"`) {
		t.Fatalf("NaN repair = %v, want an error naming the argument", err)
	}
}

func TestWithArgsKeepsThePositionsOfTheKeysItKeeps(t *testing.T) {
	p := PreparedArguments{Raw: json.RawMessage(`{"b":1, "a":"x", "gone":true}`), Coercions: []schema.Coercion{{Path: "b"}}}
	args := map[string]any{"a": "y", "b": json.Number("1"), "c": true, "0": 1}
	got := p.WithArgs(args)
	if string(got.Raw) != `{"b":1,"a":"y","0":1,"c":true}` || got.Args["c"] != true || len(got.Coercions) != 1 {
		t.Fatalf("WithArgs = %s, %v, %v", got.Raw, got.Args, got.Coercions)
	}
	if _, err := p.TryWithArgs(map[string]any{"x": math.NaN()}); err == nil {
		t.Fatal("TryWithArgs accepted NaN")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("WithArgs did not panic on NaN")
		}
	}()
	p.WithArgs(map[string]any{"x": math.NaN()})
}

// An authored schema keeps its constraints through Parse into the pipeline.
func TestPrepareArgumentsEnforcesAParsedSchema(t *testing.T) {
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"query":{"type":"string"},"filter":{"type":"string"},` +
		`"limit":{"type":"integer","maximum":10}},"required":["query"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	tool := Tool{Name: "search", InputSchema: s}
	in := `{"query":"test", "filter":"active","limit":5}`
	if p, err := PrepareArguments(tool, toolUse(t, "t1", "search", in)); err != nil || string(p.Raw) != in {
		t.Fatalf("prepared = %s, %v", p.Raw, err)
	}
	_, err = PrepareArguments(tool, toolUse(t, "t2", "search", `{"query":"x","limit":50,"extra":1}`))
	if err == nil || !strings.Contains(err.Error(), "at most 10") || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("PrepareArguments = %v, want the bound and the closed object enforced", err)
	}
}
