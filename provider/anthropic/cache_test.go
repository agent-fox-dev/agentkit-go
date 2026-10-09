package anthropic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
)

// rawBody returns the exact request bytes for req on m, as they are sent.
func rawBody(t *testing.T, m core.Model, req core.Request) []byte {
	t.Helper()
	raw, err := anthropic.BuildRequestJSON(req, m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func cached(v any) bool {
	obj, _ := v.(map[string]any)
	cc, _ := obj["cache_control"].(map[string]any)
	return cc != nil && cc["type"] == "ephemeral"
}

func blocksOf(t *testing.T, msg any) []any {
	t.Helper()
	content, _ := msg.(map[string]any)["content"].([]any)
	return content
}

// TS-10-23: a replayed tool_use input reaches the wire as the model wrote it,
// spacing and key order included.
func TestReplayedToolInputKeepsItsBytes_TS10_23(t *testing.T) {
	raw := json.RawMessage(`{"b": 1,  "a":  2}`)
	m := *testModel()
	body := rawBody(t, m, core.Request{Messages: core.Messages{
		core.UserMessage{Content: core.Content{core.TextBlock{Text: "go"}}},
		core.AssistantMessage{Content: core.Content{core.ToolUseBlock{ID: "call_1", Name: "tool", Input: raw}},
			StopReason: core.StopReasonToolUse, Provider: m.Provider, API: m.API, Model: m.ID},
		core.ToolResultMessage{ToolUseID: "call_1", ToolName: "tool", Content: core.Content{core.TextBlock{Text: "ok"}}},
	}})
	if !strings.Contains(string(body), `"input":`+string(raw)) {
		t.Fatalf("the tool_use input was re-encoded; want %s verbatim in\n%s", raw, body)
	}
}

// TS-10-24: one breakpoint on the last system block and one on the last tool,
// none on the others.
func TestSystemAndToolBreakpoints_TS10_24(t *testing.T) {
	m := *testModel()
	w := decode(t, rawBody(t, m, core.Request{
		System:   []core.ContentBlock{core.TextBlock{Text: "one"}, core.TextBlock{Text: "two"}},
		Messages: userTurn(),
		Tools:    core.ToolWires(sampleTools()),
	}))
	sys, _ := w["system"].([]any)
	if len(sys) != 2 || cached(sys[0]) || !cached(sys[1]) {
		t.Fatalf("system = %v; want cache_control on the last block only", sys)
	}
	tools, _ := w["tools"].([]any)
	if len(tools) != 2 || cached(tools[0]) || !cached(tools[1]) {
		t.Fatalf("tools = %v; want cache_control on the last tool only", tools)
	}
}

// TS-10-25: one breakpoint on the last block of the prefix and one on the
// last block of the last user message.
func TestPrefixAndUserBreakpoints_TS10_25(t *testing.T) {
	m := *testModel()
	w := decode(t, rawBody(t, m, core.Request{
		Prefix: core.Messages{
			core.UserMessage{Content: core.Content{core.TextBlock{Text: "project brief"}, core.TextBlock{Text: "spec"}}},
			core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "understood"}},
				StopReason: core.StopReasonStop, Provider: m.Provider, API: m.API, Model: m.ID},
		},
		Messages: core.Messages{core.UserMessage{Content: core.Content{
			core.TextBlock{Text: "first"}, core.TextBlock{Text: "last"}}}},
	}))
	msgs, _ := w["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("%d wire messages, want prefix (2) then history (1): %v", len(msgs), msgs)
	}
	prefixLast := blocksOf(t, msgs[1])
	if !cached(prefixLast[len(prefixLast)-1]) {
		t.Fatalf("prefix's last block = %v, want cache_control", prefixLast[len(prefixLast)-1])
	}
	for _, b := range blocksOf(t, msgs[0]) {
		if cached(b) {
			t.Fatalf("an earlier prefix block carries cache_control: %v", b)
		}
	}
	user := blocksOf(t, msgs[2])
	if cached(user[0]) || !cached(user[1]) {
		t.Fatalf("user blocks = %v, want cache_control on the last only", user)
	}
}
