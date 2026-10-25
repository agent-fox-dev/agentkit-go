package ollama_test

import (
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// Tools are declared in request order as functions, each schema cut to what
// the SDK's parameter type carries; no schema, or one without properties, is
// an empty object schema.
func TestToolsAreDeclaredWithTheSchemaSubsetOllamaCarries(t *testing.T) {
	full := mustSchema(t, `{"type":"object","additionalProperties":false,"properties":{
		"url":{"type":"string","format":"uri","description":"where to look"},"n":{"type":"integer","minimum":1},
		"mode":{"type":"string","enum":["a","b"]},"tags":{"type":"array","items":{"type":"string"},"description":"labels"}},
		"required":["url"]}`)
	req := userRequest("hi")
	req.Tools = []core.ToolWire{
		{Name: "fetch", Description: "fetches a page", InputSchema: full},
		{Name: "ping", Description: "pings"},
		{Name: "bare", InputSchema: mustSchema(t, `{"type":"object"}`)},
	}
	tools := sentBody(t, testModel(), req)["tools"].([]any)
	want := []string{
		`{"type":"function","function":{"name":"fetch","description":"fetches a page","parameters":{"type":"object","required":["url"],"properties":{` +
			`"url":{"type":"string","description":"where to look"},"n":{"type":"integer"},"mode":{"type":"string","enum":["a","b"]},` +
			`"tags":{"type":"array","items":{"type":"string"},"description":"labels"}}}}}`,
		`{"type":"function","function":{"name":"ping","description":"pings","parameters":{"type":"object","properties":{}}}}`,
		`{"type":"function","function":{"name":"bare","parameters":{"type":"object","properties":{}}}}`,
	}
	if len(tools) != len(want) {
		t.Fatalf("tools = %v, want %d", tools, len(want))
	}
	for i, w := range want {
		if !testutil.SameJSON([]byte(mustJSON(tools[i])), []byte(w)) {
			t.Errorf("tool %d = %s\nwant     %s", i, mustJSON(tools[i]), w)
		}
	}
}

// A schema the SDK's parameter type cannot read (a union type) fails the turn
// before any request, naming the tool.
func TestAnUnreadableSchemaFailsTheTurnBeforeTheRequest(t *testing.T) {
	req := userRequest("hi")
	req.Tools = []core.ToolWire{{Name: "good_tool"}, {Name: "bad_tool", InputSchema: mustSchema(t, `{"type":["object","null"]}`)}}
	srv := newServer(t, okTurn())
	o := run(t, newClient(t, srv, testModel()), req)
	if err := failedTurn(t, o); !strings.Contains(err.Error(), "bad_tool") || srv.Count() != 0 {
		t.Errorf("error = %v after %d requests, want one naming bad_tool and none", err, srv.Count())
	}
}

// Ollama has no tool choice: none sends no tools key, unset and auto send them.
func TestToolChoiceNoneSendsNoTools(t *testing.T) {
	for choice, want := range map[core.ToolChoice]bool{core.ToolChoiceNone: false, core.ToolChoiceUnset: true, core.ToolChoiceAuto: true} {
		req := userRequest("hi")
		req.Tools, req.ToolChoice = []core.ToolWire{{Name: "ping"}}, choice
		if _, has := sentBody(t, testModel(), req)["tools"]; has != want {
			t.Errorf("tool choice %q: tools present %v, want %v", choice, has, want)
		}
	}
}
