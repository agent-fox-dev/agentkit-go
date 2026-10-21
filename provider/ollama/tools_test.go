package ollama_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustSchema(t *testing.T, doc string) *schema.Schema {
	t.Helper()
	s, err := schema.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("schema %s: %v", doc, err)
	}
	return s
}

// wireTools returns the tools of a request body, with each tool's parameters
// decoded.
func wireTools(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, _ := body["tools"].([]any)
	var out []map[string]any
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

// TS-28-15 (integration): tools are declared in request order with each schema
// cut to the part the SDK's parameter type carries.
func TestToolsAreDeclaredWithTheSchemaSubset_TS28_15(t *testing.T) {
	first := mustSchema(t, `{"type":"object","additionalProperties":false,"properties":{
		"url":{"type":"string","format":"uri","description":"where to look"},
		"n":{"type":"integer","minimum":1},
		"mode":{"type":"string","enum":["a","b"]},
		"tags":{"type":"array","items":{"type":"string"},"description":"labels"}},
		"required":["url"]}`)
	second := mustSchema(t, `{"type":"object","properties":{"q":{"type":"string"}}}`)
	req := userRequest("hi")
	req.Tools = []core.ToolWire{
		{Name: "fetch", Description: "fetches a page", InputSchema: first},
		{Name: "search", Description: "searches", InputSchema: second},
	}
	srv := onePlainTurn(t)
	run(t, newClient(t, srv, testModel()), req)

	tools := wireTools(t, sentBody(t, srv, 0))
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want two", tools)
	}
	for i, want := range []struct{ name, desc string }{{"fetch", "fetches a page"}, {"search", "searches"}} {
		fn, _ := tools[i]["function"].(map[string]any)
		if tools[i]["type"] != "function" || fn["name"] != want.name || fn["description"] != want.desc {
			t.Errorf("tool %d = %v, want a function named %s: %s", i, tools[i], want.name, want.desc)
		}
	}
	params := tools[0]["function"].(map[string]any)["parameters"]
	var want any
	if err := json.Unmarshal([]byte(`{"type":"object","required":["url"],"properties":{
		"url":{"type":"string","description":"where to look"},
		"n":{"type":"integer"},
		"mode":{"type":"string","enum":["a","b"]},
		"tags":{"type":"array","items":{"type":"string"},"description":"labels"}}}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(params, want) {
		t.Errorf("parameters = %s\nwant       %s", mustJSON(t, params), mustJSON(t, want))
	}
	for _, kw := range []string{"additionalProperties", "format", "minimum"} {
		if strings.Contains(mustJSON(t, params), kw) {
			t.Errorf("the parameters still hold %q: %s", kw, mustJSON(t, params))
		}
	}
}

// TS-28-16: a tool with no schema, and one whose schema has no properties, is
// declared with an empty object schema.
func TestToolWithoutASchemaIsAnEmptyObject_TS28_16(t *testing.T) {
	for name, s := range map[string]*schema.Schema{"nil": nil, "no properties": mustSchema(t, `{"type":"object"}`)} {
		req := userRequest("hi")
		req.Tools = []core.ToolWire{{Name: "ping", InputSchema: s}}
		srv := onePlainTurn(t)
		run(t, newClient(t, srv, testModel()), req)
		tools := wireTools(t, sentBody(t, srv, 0))
		if len(tools) != 1 {
			t.Fatalf("%s: tools = %v, want one", name, tools)
		}
		params := tools[0]["function"].(map[string]any)["parameters"]
		if got := mustJSON(t, params); got != `{"properties":{},"type":"object"}` {
			t.Errorf("%s: parameters = %s, want {\"type\":\"object\",\"properties\":{}}", name, got)
		}
	}
}

// TS-28-17: a schema the SDK's parameter type cannot decode fails the turn before
// any request is sent, naming the tool.
func TestUndecodableSchemaFailsBeforeTheRequest_TS28_17(t *testing.T) {
	// A top-level type of ["object","null"] is a union the SDK's parameter type,
	// whose type is one string, cannot read.
	bad := mustSchema(t, `{"type":["object","null"]}`)
	req := userRequest("hi")
	req.Tools = []core.ToolWire{{Name: "good_tool"}, {Name: "bad_tool", InputSchema: bad}}
	srv := onePlainTurn(t)
	o := run(t, newClient(t, srv, testModel()), req)

	if o.err == nil || !strings.HasPrefix(o.err.Error(), "ollama:") || !strings.Contains(o.err.Error(), "bad_tool") {
		t.Fatalf("error = %v, want one that starts \"ollama:\" and names bad_tool", o.err)
	}
	if o.msg.StopReason != core.StopReasonError {
		t.Errorf("StopReason = %q, want error", o.msg.StopReason)
	}
	// A request sent after the stream has ended would arrive a moment later.
	time.Sleep(100 * time.Millisecond)
	if n := srv.Count(); n != 0 {
		t.Errorf("the server received %d requests, want 0", n)
	}
}

// TS-28-18: ToolChoiceNone sends no tools key at all, and unset and auto send the
// declared tools.
func TestToolChoice_TS28_18(t *testing.T) {
	for _, c := range []struct {
		choice core.ToolChoice
		want   int
	}{{core.ToolChoiceNone, 0}, {core.ToolChoiceUnset, 1}, {core.ToolChoiceAuto, 1}} {
		req := userRequest("hi")
		req.Tools = []core.ToolWire{{Name: "ping"}}
		req.ToolChoice = c.choice
		srv := onePlainTurn(t)
		run(t, newClient(t, srv, testModel()), req)
		body := sentBody(t, srv, 0)
		if _, present := body["tools"]; present != (c.want > 0) || len(wireTools(t, body)) != c.want {
			t.Errorf("tool choice %q: tools = %v, want %d", c.choice, body["tools"], c.want)
		}
	}
}
