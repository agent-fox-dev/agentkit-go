package openrouter_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// sent streams req to a provider for m and returns the body it sent.
func sent(t *testing.T, m core.Model, req core.Request) map[string]any {
	t.Helper()
	srv := newServer(t, okTurn())
	run(t, newProvider(t, srv, m), req)
	return srv.body(t, 0)
}

func TestTheBodyCarriesTheModelTheStreamFlagsAndOneSystemMessage(t *testing.T) {
	m := testModel()
	m.ID = "Vendor/Model:Free-v1.2"
	req := userRequest("hi")
	req.System = []core.ContentBlock{core.TextBlock{Text: "A"}, core.TextBlock{}, core.TextBlock{Text: "B"}}
	body := sent(t, m, req)
	if body["model"] != "Vendor/Model:Free-v1.2" || body["stream"] != true {
		t.Errorf("model = %v, stream = %v", body["model"], body["stream"])
	}
	if so, _ := body["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", body["stream_options"])
	}
	msgs := messagesOf(body)
	if roles(msgs) != "system,user" || msgs[0]["content"] != "A\n\nB" {
		t.Errorf("messages = %v, want one system message A\\n\\nB, then the user", msgs)
	}
	if got := roles(messagesOf(sent(t, m, userRequest("hi")))); got != "user" {
		t.Errorf("roles without a system prompt = %s, want user", got)
	}
}

// The prefix comes first, a user message without text is skipped, each tool
// result is a tool message with the call's id, and no cache marker is sent.
func TestMessagesKeepTheirOrderAndToolResultsBecomeToolMessages(t *testing.T) {
	okResult := result("c1", "read", "contents")
	errResult := result("c2", "write", "denied")
	errResult.IsError = true
	srv := newServer(t, okTurn())
	run(t, newProvider(t, srv, testModel()), core.Request{
		Prefix: core.Messages{userMsg("prefix")},
		Messages: core.Messages{
			core.UserMessage{},
			core.AssistantMessage{Content: core.Content{toolCall("c1", "read", `{}`), toolCall("c2", "write", `{}`)}, StopReason: core.StopReasonToolUse},
			okResult, errResult,
			userMsg("last"),
		},
	})
	msgs := messagesOf(srv.body(t, 0))
	if got := roles(msgs); got != "user,assistant,tool,tool,user" {
		t.Fatalf("roles = %s", got)
	}
	if msgs[0]["content"] != "prefix" || msgs[4]["content"] != "last" {
		t.Errorf("user contents = %v and %v", msgs[0]["content"], msgs[4]["content"])
	}
	for i, r := range []core.ToolResultMessage{okResult, errResult} {
		if m := msgs[2+i]; m["tool_call_id"] != r.ToolUseID || m["content"] != provider.ToolResultText(r) {
			t.Errorf("tool message %d = %v, want call %s and %q", i, m, r.ToolUseID, provider.ToolResultText(r))
		}
	}
	if !strings.HasPrefix(msgs[3]["content"].(string), "Error: ") {
		t.Errorf("the error result's content is %q, want an Error: prefix", msgs[3]["content"])
	}
	if bytes.Contains(srv.Requests()[0].Body, []byte("cache_control")) {
		t.Error("the body contains cache_control")
	}
}

// An assistant message replays its tool calls' ids (another provider's
// included) and arguments byte for byte, has no content without text, and is
// skipped when it has nothing to send.
func TestAssistantMessagesReplayToolCallsByteForByte(t *testing.T) {
	const args = `{"b": 1,  "a":[1, 2]}`
	msgs := messagesOf(sent(t, testModel(), core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Model: "claude-other", Content: core.Content{core.TextBlock{Text: "reading"}, toolCall("toolu_01abc", "read", args)}},
		result("toolu_01abc", "read", "r"),
		core.AssistantMessage{StopReason: core.StopReasonStop},
		core.AssistantMessage{Content: core.Content{toolCall("c2", "read", `{}`)}},
		result("c2", "read", "r2"),
	}}))
	if got := roles(msgs); got != "user,assistant,tool,assistant,tool" {
		t.Fatalf("roles = %s, want the empty assistant message skipped", got)
	}
	call := msgs[1]["tool_calls"].([]any)[0].(map[string]any)
	fn := call["function"].(map[string]any)
	if call["id"] != "toolu_01abc" || call["type"] != "function" || fn["name"] != "read" || fn["arguments"] != args {
		t.Errorf("tool call = %v, want toolu_01abc, function, read and the arguments as recorded", call)
	}
	if msgs[1]["content"] != "reading" || msgs[2]["tool_call_id"] != "toolu_01abc" {
		t.Errorf("assistant content = %v, result id = %v", msgs[1]["content"], msgs[2]["tool_call_id"])
	}
	if _, has := msgs[3]["content"]; has {
		t.Errorf("a message with only tool calls has content %v", msgs[3]["content"])
	}
}

// A tool's parameters are its schema exactly as it marshals (key order kept,
// and <, > and & unescaped), a tool without one an empty object, with no
// strict flag.
func TestToolsAreDeclaredWithTheirSchemaAsItMarshals(t *testing.T) {
	s, err := schema.Parse([]byte(`{"type":"object","properties":{"zeta":{"type":"string","format":"uri"},` +
		`"alpha":{"type":"integer","minimum":1,"description":"a<b && c>d"},"mid":{"type":"array","items":{"type":"string"}}},` +
		`"required":["zeta"],"additionalProperties":false}`))
	if err != nil {
		t.Fatal(err)
	}
	params, _ := s.MarshalJSON()
	req := userRequest("hi")
	req.Tools = []core.ToolWire{{Name: "fetch", Description: "fetches", InputSchema: s}, {Name: "ping"}}
	srv := newServer(t, okTurn())
	run(t, newProvider(t, srv, testModel()), req)
	raw := srv.Requests()[0].Body
	want := `"tools":[{"type":"function","function":{"name":"fetch","description":"fetches","parameters":` + string(params) + `}},` +
		`{"type":"function","function":{"name":"ping","parameters":{"type":"object","properties":{}}}}]`
	if !bytes.Contains(raw, []byte(want)) || bytes.Contains(raw, []byte(`"strict"`)) {
		t.Errorf("body = %s\nwant it to hold %s and no strict flag", raw, want)
	}
}

// Each optional field is sent when the request sets it, a zero included, and
// left out otherwise; the completion bound is capped at the model's output cap.
func TestOptionalFieldsAreSentOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cap   int
		req   func(*core.Request)
		field string
		want  any // nil: absent
	}{
		{"tool choice auto", 4096, func(r *core.Request) { r.ToolChoice = core.ToolChoiceAuto }, "tool_choice", "auto"},
		{"tool choice none", 4096, func(r *core.Request) { r.ToolChoice = core.ToolChoiceNone }, "tool_choice", "none"},
		{"tool choice unset", 4096, func(*core.Request) {}, "tool_choice", nil},
		{"a bound over the cap", 131072, func(r *core.Request) { r.MaxTokens = ptr(200000) }, "max_completion_tokens", 131072},
		{"a bound under the cap", 131072, func(r *core.Request) { r.MaxTokens = ptr(4096) }, "max_completion_tokens", 4096},
		{"no bound", 32768, func(*core.Request) {}, "max_completion_tokens", 32768},
		{"no bound and no cap", 0, func(*core.Request) {}, "max_completion_tokens", nil},
		{"a bound and no cap", 0, func(r *core.Request) { r.MaxTokens = ptr(4096) }, "max_completion_tokens", 4096},
		{"temperature zero", 4096, func(r *core.Request) { r.Temperature = ptr(0.0) }, "temperature", 0},
		{"no temperature", 4096, func(*core.Request) {}, "temperature", nil},
		{"top_p zero", 4096, func(r *core.Request) { r.TopP = ptr(0.0) }, "top_p", 0},
		{"no top_p", 4096, func(*core.Request) {}, "top_p", nil},
		{"stop sequences", 4096, func(r *core.Request) { r.StopSequences = []string{"END"} }, "stop", []string{"END"}},
		{"no stop sequences", 4096, func(*core.Request) {}, "stop", nil},
	} {
		m := testModel()
		m.MaxOutputTokens = tc.cap
		req := userRequest("hi")
		tc.req(&req)
		got, has := sent(t, m, req)[tc.field]
		if tc.want == nil && has || tc.want != nil && mustJSON(got) != mustJSON(tc.want) {
			t.Errorf("%s: %s = %v (present %v), want %v", tc.name, tc.field, got, has, tc.want)
		}
	}
}

// The prefix and the history are each repaired so every tool call has a
// result, with one warning per part changed and none for a clean request.
func TestABrokenTranscriptIsRepairedWithAWarningPerPart(t *testing.T) {
	srv := newServer(t, okTurn())
	warn, warns := collectWarnings()
	c := newProvider(t, srv, testModel(), warn)
	run(t, c, core.Request{
		Prefix: core.Messages{userMsg("prefix"), core.AssistantMessage{Content: core.Content{toolCall("c1", "read", `{}`)}}},
		Messages: core.Messages{
			userMsg("q"),
			core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "half"}}, StopReason: core.StopReasonError},
			userMsg("again"),
		},
	})
	if len(*warns) != 2 || !strings.Contains((*warns)[0], "prefix") || !strings.Contains((*warns)[1], "history") {
		t.Errorf("warnings = %q, want one for the prefix and one for the history", *warns)
	}
	msgs := messagesOf(srv.body(t, 0))
	if got := roles(msgs); got != "user,assistant,tool,user,user" {
		t.Fatalf("roles = %s, want a synthetic result and the errored turn dropped", got)
	}
	if msgs[2]["tool_call_id"] != "c1" || msgs[2]["content"] != "Error: No result provided" {
		t.Errorf("synthetic result = %v", msgs[2])
	}
	run(t, c, userRequest("clean"))
	if len(*warns) != 2 {
		t.Errorf("a clean request added warnings: %q", (*warns)[2:])
	}
}

// reasoning_details replays the thinking blocks' fragment arrays in block
// order, with every field, to the model that produced them only; thinking
// text never becomes content, and a message with only unsigned thinking is
// not sent while one with only signed thinking is.
func TestReasoningIsReplayedAsFragmentsToTheSameModelOnly(t *testing.T) {
	frag1 := `{"type":"reasoning.text","text":"one","signature":"s1","format":"anthropic-claude-v1","index":0,"id":"a","extra":{"k":[1,2]}}`
	frag2 := `{"type":"reasoning.encrypted","data":"AAAA==","format":"openai-responses-v1","index":1,"id":"b"}`
	frag3 := `{"type":"reasoning.summary","summary":"three","index":2}`
	const marker = "THINKING-MARKER"
	body := sent(t, testModel(), core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Model: "vendor/model", Content: core.Content{
			core.ThinkingBlock{Thinking: marker, Signature: "[" + frag1 + "," + frag2 + "]"},
			core.ThinkingBlock{Thinking: marker, Signature: "[" + frag3 + "]"},
			core.ThinkingBlock{Thinking: marker},
			core.ThinkingBlock{Thinking: marker, Signature: "not json"},
			core.TextBlock{Text: "answer"},
		}},
		userMsg("a"),
		core.AssistantMessage{Model: "other/model", Content: core.Content{
			core.ThinkingBlock{Thinking: marker, Signature: "[" + frag1 + "]"}, core.TextBlock{Text: "foreign"},
		}},
		userMsg("b"),
		core.AssistantMessage{Model: "vendor/model", Content: core.Content{core.ThinkingBlock{Thinking: marker}}},
		userMsg("c"),
		core.AssistantMessage{Model: "vendor/model", Content: core.Content{core.ThinkingBlock{Thinking: marker, Signature: "[" + frag2 + "]"}}},
		userMsg("d"),
	}})
	msgs := messagesOf(body)
	if got := roles(msgs); got != "user,assistant,user,assistant,user,user,assistant,user" {
		t.Fatalf("roles = %s, want the unsigned thinking-only message skipped", got)
	}
	if got := mustJSON(msgs[1]["reasoning_details"]); !testutil.SameJSON([]byte(got), []byte("["+frag1+","+frag2+","+frag3+"]")) {
		t.Errorf("reasoning_details = %s, want the three fragments in block order", got)
	}
	if _, has := msgs[3]["reasoning_details"]; has {
		t.Errorf("a message of another model replayed %v", msgs[3]["reasoning_details"])
	}
	if _, has := msgs[6]["content"]; has || msgs[6]["reasoning_details"] == nil {
		t.Errorf("signed thinking alone = %v, want reasoning_details and no content", msgs[6])
	}
	if msgs[1]["content"] != "answer" || strings.Contains(mustJSON(body), marker) {
		t.Errorf("content = %v; thinking text must never be sent", msgs[1]["content"])
	}
}
