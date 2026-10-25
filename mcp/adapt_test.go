package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/diag"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// Go's default for a JSON number is float64, which would turn
// 9007199254740993 into 9007199254740992 and 1.10 into 1.1: the model's bytes
// reach the server as written.
func TestAnAdaptedToolPassesTheModelsArgumentsVerbatim(t *testing.T) {
	var mu sync.Mutex
	var got map[string]any
	s := newServer("s")
	handle(s, &Tool{Name: "inspect"}, func(args map[string]any) (*CallToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		got = args
		return text("ok"), nil
	})
	tl := toolsOf(t, poolOf(t, s, ServerConfig{Name: "s"}))["s__inspect"]
	if res := tl.Execute(context.Background(), json.RawMessage(`{"id":9007199254740993,"ratio":1.10,"exp":1e3}`)); !res.OK {
		t.Fatalf("execute failed: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	for k, w := range map[string]json.Number{"id": "9007199254740993", "ratio": "1.10", "exp": "1e3"} {
		if n, ok := got[k].(json.Number); !ok || n != w {
			t.Fatalf("server received %s = %v (%T); want the literal %s", k, got[k], got[k], w)
		}
	}
}

func TestAnAdaptedToolsFailuresAreErrorResults(t *testing.T) {
	p := poolOf(t, nil, ServerConfig{Name: "srv"})
	tools := toolsOf(t, p)
	echo := tools["srv__echo"]
	if echo.Description != "echo the message back" || !echo.InputSchema.IsRequired("message") {
		t.Fatalf("echo = %q %+v; want the server's description and required message", echo.Description, echo.InputSchema)
	}
	for _, args := range []string{`[1]`, `"s"`, `{"message":`} {
		if res := echo.Execute(context.Background(), json.RawMessage(args)); res.OK || res.Error != "invalid_arguments" {
			t.Fatalf("args %s: %+v, want invalid_arguments", args, res)
		}
	}
	res := tools["srv__boom"].Execute(context.Background(), json.RawMessage(" "))
	if res.OK || res.Error != "tool_error" || res.Detail != "the tool refused" || res.Data["text"] != "the tool refused" {
		t.Fatalf("boom = %+v, want a tool_error carrying the server's text", res)
	}
	conn, _ := p.Get("srv")
	must(t, conn.Close())
	if res := echo.Execute(context.Background(), json.RawMessage(`{"message":"x"}`)); res.OK || res.Error != "mcp_call_failed" {
		t.Fatalf("after close: %+v, want mcp_call_failed", res)
	}
}

// An input schema the converter does not model becomes something permissive
// rather than a rejection: the server checks its arguments again anyway.
func TestInputSchemasConvertToTheTypedForm(t *testing.T) {
	deep := strings.Repeat(`{"type":"array","items":`, 18) + `{"type":"integer"}` + strings.Repeat(`}`, 18)
	for _, tc := range []struct{ name, in, want string }{
		{"absent", `null`, `{"type":"object","properties":{}}`},
		{"scalars and required", `{"type":"object","description":"d","properties":{"s":{"type":"string","description":"a"},` +
			`"n":{"type":"number"},"i":{"type":"integer"},"b":{"type":"boolean"}},"required":["s",1]}`,
			`{"type":"object","description":"d","properties":{"s":{"type":"string","description":"a"},` +
				`"n":{"type":"number"},"i":{"type":"integer"},"b":{"type":"boolean"}},"required":["s"]}`},
		{"arrays", `{"properties":{"a":{"type":"array","items":{"type":"integer"}},"bare":{"type":"array"}}}`,
			`{"type":"object","properties":{"a":{"type":"array","items":{"type":"integer"}},"bare":{"type":"array","items":{"type":"string"}}}}`},
		{"an enum keeps its strings", `{"properties":{"e":{"type":"string","enum":["x","y",3]},"none":{"type":"string","enum":[3]}}}`,
			`{"type":"object","properties":{"e":{"type":"string","enum":["x","y"]},"none":{"type":"string"}}}`},
		{"unmodelled is open", `{"properties":{"u":{"type":["string","null"]},"z":{"type":"null"},"v":true}}`,
			`{"type":"object","properties":{"u":{"type":"object","properties":{}},"z":{"type":"string"},"v":{"type":"string"}}}`},
		{"nesting past 16", deep, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in any
			must(t, json.Unmarshal([]byte(tc.in), &in))
			got, err := json.Marshal(schemaFrom(in))
			must(t, err)
			if tc.want == "" {
				if !strings.Contains(string(got), `"items":{"type":"string"}`) || strings.Contains(string(got), "integer") {
					t.Fatalf("got %s; the level past 16 must be a string", got)
				}
				return
			}
			if !testutil.SameJSON(got, []byte(tc.want)) {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	if got, _ := json.Marshal(schemaFrom(make(chan int))); !testutil.SameJSON(got, []byte(`{"type":"object","properties":{}}`)) {
		t.Fatalf("an unencodable schema = %s, want an open object", got)
	}
}

// An output schema is a promise callers bind to, so what cannot be modelled is
// left unconstrained instead of guessed, a non-object root is wrapped as
// {"value": ...} the way Data is, and a schema that is not an object fails.
func TestOutputSchemasConvertWithoutGuessing(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"an object", `{"type":"object","properties":{"value":{"type":"number"},"unit":{"type":"string","description":"u"}},"required":["value"]}`,
			`{"type":"object","properties":{"value":{"type":"number"},"unit":{"type":"string","description":"u"}},"required":["value"]}`},
		{"a scalar root is wrapped", `{"type":"integer"}`,
			`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}`},
		{"unmodelled keywords are unconstrained", `{"properties":{"n":{"type":["string","null"]},"a":{"anyOf":[{"type":"string"}]},` +
			`"e":{"enum":["x"]},"z":{"type":"null"},"m":{"type":"string","enum":["x",1]},"k":{"type":"string","enum":["x","y"]},` +
			`"b":{"type":"boolean"},"arr":{"type":"array"},"d":{"description":"free"}}}`,
			`{"type":"object","properties":{"n":{},"a":{},"e":{},"z":{"type":"null"},"m":{"type":"string"},` +
				`"k":{"type":"string","enum":["x","y"]},"b":{"type":"boolean"},"arr":{"type":"array","items":{}},"d":{"description":"free"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var in any
			must(t, json.Unmarshal([]byte(tc.in), &in))
			s, err := outputSchemaFrom(in)
			must(t, err)
			got, err := json.Marshal(s)
			must(t, err)
			if !testutil.SameJSON(got, []byte(tc.want)) {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	for _, bad := range []any{"not a schema", []any{1}, make(chan int)} {
		if s, err := outputSchemaFrom(bad); err == nil {
			t.Fatalf("outputSchemaFrom(%v) = %+v, want an error", bad, s)
		}
	}
}

// What the converter cannot model must not reject the server's valid
// structuredContent, and a wrong type where the schema is definite is still
// caught.
func TestAnImportedOutputSchemaAcceptsTheServersValidData(t *testing.T) {
	var in any
	must(t, json.Unmarshal([]byte(`{"type":"object","properties":{"n":{"type":["string","null"]},"a":{"anyOf":[{"type":"string"},{"type":"integer"}]},`+
		`"z":{"type":"null"},"s":{"type":"string"},"o":{"properties":{"k":{"type":"integer"}},"required":["k"]}},"required":["n","a","z","s","o"]}`), &in))
	out, err := outputSchemaFrom(in)
	must(t, err)
	if err := schema.Validate(out, testutil.Object(t, []byte(`{"n":"foo","a":3,"z":null,"s":"str","o":{"k":1}}`))); err != nil {
		t.Fatalf("valid structuredContent fails the imported schema: %v", err)
	}
	if schema.Validate(out, testutil.Object(t, []byte(`{"n":null,"a":1,"z":null,"s":5,"o":{"k":1}}`))) == nil {
		t.Fatal("a number where the schema says string passed the imported schema")
	}
}

// A malformed output schema costs the tool its output typing, not the tool:
// it is reported once as an error naming the server and the tool, however
// often the tools are listed; an omitted one is simply absent.
func TestAMalformedOutputSchemaIsDroppedAndReportedOnce(t *testing.T) {
	s := newServer("schemas")
	ok := func(map[string]any) (*CallToolResult, error) { return text("ok"), nil }
	handle(s, &Tool{Name: "calc", OutputSchema: json.RawMessage(`{"type":"object","properties":{"v":{"type":"number"}}}`)}, ok)
	handle(s, &Tool{Name: "broken", OutputSchema: "not a valid schema object"}, ok)
	handle(s, &Tool{Name: "simple"}, ok)
	p := poolOf(t, s, ServerConfig{Name: "srv"})
	tools := toolsOf(t, p)
	_ = toolsOf(t, p)

	if tools["srv__calc"].OutputSchema == nil || tools["srv__broken"].OutputSchema != nil || tools["srv__simple"].OutputSchema != nil {
		t.Fatalf("output schemas: calc %v, broken %v, simple %v; want only calc's",
			tools["srv__calc"].OutputSchema, tools["srv__broken"].OutputSchema, tools["srv__simple"].OutputSchema)
	}
	diags := p.Diagnostics()
	if len(diags) != 1 || diags[0].Severity != diag.SeverityError ||
		!strings.Contains(diags[0].Message, `server "srv" tool "broken"`) {
		t.Fatalf("diagnostics = %+v, want one error naming srv and broken", diags)
	}
	for name, tl := range tools {
		if res := tl.Execute(context.Background(), json.RawMessage(`{}`)); !res.OK {
			t.Fatalf("%s = %+v; the connection must keep serving", name, res)
		}
	}
}

// A pool connected to a real server imports a tool's outputSchema, and a call
// returns the server's structuredContent as Data, which conforms to it.
func TestStructuredContentConformsToTheImportedOutputSchema(t *testing.T) {
	s := newServer("weather")
	handle(s, &Tool{Name: "forecast",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},` +
			`"temperature":{"type":"number"},"days":{"type":"array","items":{"type":"string"}}},"required":["city","temperature"]}`),
	}, func(args map[string]any) (*CallToolResult, error) {
		city, _ := args["city"].(string)
		return &CallToolResult{Content: []Content{&TextContent{Text: city + ": 21.5"}},
			StructuredContent: map[string]any{"city": city, "temperature": 21.5, "days": []any{"mon", "tue"}}}, nil
	})
	tl := toolsOf(t, poolOf(t, s, ServerConfig{Name: "srv"}))["srv__forecast"]
	res := tl.Execute(context.Background(), json.RawMessage(`{"city":"Oslo"}`))
	if !res.OK || res.Text != "Oslo: 21.5" {
		t.Fatalf("call = %+v", res)
	}
	data := testutil.Object(t, []byte(testutil.MustJSON(t, res.Data)))
	if data["city"] != "Oslo" || tl.OutputSchema == nil {
		t.Fatalf("Data = %v, OutputSchema %+v", data, tl.OutputSchema)
	}
	if err := schema.Validate(tl.OutputSchema, data); err != nil {
		t.Fatalf("Data does not conform to the imported OutputSchema: %v", err)
	}
}
