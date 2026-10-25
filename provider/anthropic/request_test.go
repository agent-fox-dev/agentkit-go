package anthropic_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

func blockTypes(body map[string]any) string {
	var msgs []string
	for i, bs := range blocks(body) {
		var types []string
		for _, b := range bs {
			types = append(types, b["type"].(string))
		}
		msgs = append(msgs, objects(body["messages"])[i]["role"].(string)+":"+strings.Join(types, ","))
	}
	return strings.Join(msgs, " ")
}

// The provider sends exactly the bytes BuildRequestJSONWith returns for the
// same options, so the request tests below test what goes on the wire.
func TestTheProviderSendsTheBuiltRequestBytes(t *testing.T) {
	m := testModel()
	m.Efforts = map[core.Effort]*string{core.EffortHigh: new("2048")}
	req := core.Request{
		System:   []core.ContentBlock{core.TextBlock{Text: "be brief"}},
		Messages: core.Messages{user("go"), reply(toolUse("c1", "edit", `{"b": 1,  "a":  2}`)), result("c1", "ok")},
		Tools:    []core.ToolWire{{Name: "edit", InputSchema: schema.Object(schema.Prop("b", schema.Int()))}},
		Effort:   core.EffortHigh,
	}
	opts := anthropic.Options{NonStrictTools: []string{"edit"}, CacheRetention: core.CacheRetentionLong}
	want, err := anthropic.BuildRequestJSONWith(req, *m, opts)
	if err != nil {
		t.Fatal(err)
	}
	x := run(t, m, req, opts, 200, streamFixture())
	if string(x.body) != string(want) {
		t.Fatalf("sent\n%s\nbuilt\n%s", x.body, want)
	}
	if x.req.Method != "POST" || x.req.URL.String() != "https://api.anthropic.com/v1/messages" ||
		x.req.Header.Get("anthropic-version") != anthropic.APIVersion || x.req.Header.Get("x-api-key") != testKey {
		t.Fatalf("request = %s %s %v", x.req.Method, x.req.URL, x.req.Header)
	}
}

// A streamed tool call is replayed with the bytes the model streamed, spacing
// and key order included: re-encoding them shifts the prompt-cache prefix.
func TestAStreamedToolCallIsReplayedByteForByte(t *testing.T) {
	const raw = `{"path": "foo/bar.go",   "query": "func Test"}`
	half := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	x := run(t, testModel(), core.Request{Messages: core.Messages{user("find")}}, anthropic.Options{}, 200, sse(
		"message_start", messageStart("claude-test"),
		"content_block_start", `{"index":0,"content_block":{"type":"tool_use","id":"call_1","name":"search","input":{}}}`,
		"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":`+half(raw[:17])+`}}`,
		"content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":`+half(raw[17:])+`}}`,
		"content_block_stop", `{"index":0}`,
		"message_delta", `{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		"message_stop", `{}`,
	))
	out, err := anthropic.BuildRequestJSON(core.Request{Messages: core.Messages{
		user("find"), *x.msg, result("call_1", "ok")}}, *testModel())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"input":`+raw) {
		t.Fatalf("the replayed input is not the streamed bytes %s:\n%s", raw, out)
	}
}

// Consecutive turns of one role are one wire turn, a tool result is a user
// block, and a turn with nothing left to send is skipped rather than sent
// with empty content.
func TestMessagesAreCoalescedIntoAlternatingTurns(t *testing.T) {
	body := wireBody(t, testModel(), core.Request{Messages: core.Messages{
		user("first"), user("second"),
		reply(core.TextBlock{Text: "reading"}, toolUse("c1", "read", `{}`), toolUse("c2", "read", `{}`)),
		result("c1", "one"), result("c2", "two"),
		user(""),
		reply(core.TextBlock{Text: ""}),
		user("last"),
	}}, anthropic.Options{CacheRetention: core.CacheRetentionNone})

	if got, want := blockTypes(body), "user:text,text assistant:text,tool_use,tool_use user:tool_result,tool_result,text"; got != want {
		t.Fatalf("messages = %s, want %s", got, want)
	}
	if b, _ := json.Marshal(body); strings.Contains(string(b), `"content":[]`) {
		t.Fatalf("an empty content array was sent: %s", b)
	}
}

func TestToolResultContentIsEncodedAsTheWireRequires(t *testing.T) {
	cases := []struct {
		name    string
		results core.Messages
		want    string // the user turn's blocks, each with its content's types
	}{
		{"an empty result omits content", core.Messages{core.ToolResultMessage{ToolUseID: "c1"}}, "tool_result()"},
		{"empty text inside a result is dropped", core.Messages{core.ToolResultMessage{ToolUseID: "c1",
			Content: core.Content{core.TextBlock{}, core.TextBlock{Text: "a.go"}}}}, "tool_result(text)"},
		{"content that cannot nest follows every result", core.Messages{
			core.ToolResultMessage{ToolUseID: "c1", Content: core.Content{core.ThinkingBlock{Thinking: "x", Signature: "s"}}},
			core.ToolResultMessage{ToolUseID: "c2", Content: core.Content{core.TextBlock{Text: "b"}, toolUse("c9", "t", `{}`)}},
		}, "tool_result() tool_result(text) thinking() tool_use()"},
		{"content that cannot nest follows a later result that displaces nothing", core.Messages{
			core.ToolResultMessage{ToolUseID: "c1", Content: core.Content{core.ThinkingBlock{Thinking: "x", Signature: "s"}}},
			core.ToolResultMessage{ToolUseID: "c2", Content: core.Content{core.TextBlock{Text: "b"}}},
		}, "tool_result() tool_result(text) thinking()"},
	}
	for _, c := range cases {
		msgs := append(core.Messages{user("go"), reply(toolUse("c1", "t", `{}`), toolUse("c2", "t", `{}`))}, c.results...)
		body := wireBody(t, testModel(), core.Request{Messages: msgs}, anthropic.Options{CacheRetention: core.CacheRetentionNone})
		var got []string
		for _, b := range blocks(body)[2] {
			if b["tool_use_id"] == "c2" && b["is_error"] == true {
				continue // the synthetic answer for a call the case leaves unanswered
			}
			var inner []string
			for _, ib := range objects(b["content"]) {
				inner = append(inner, ib["type"].(string))
			}
			if _, has := b["content"]; !has || len(inner) > 0 {
				got = append(got, b["type"].(string)+"("+strings.Join(inner, ",")+")")
			} else {
				t.Errorf("%s: %v carries an empty content key", c.name, b)
			}
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%s: user turn = %s, want %s", c.name, strings.Join(got, " "), c.want)
		}
	}
}

// A replayed thinking block carries its required key even when it is empty,
// and a redacted one carries its data.
func TestReplayedThinkingCarriesItsRequiredKeys(t *testing.T) {
	out, rep, err := anthropic.BuildRequest(testModel(), core.Request{Messages: core.Messages{
		user("hi"),
		reply(core.ThinkingBlock{Signature: "SIG"}, core.ThinkingBlock{Redacted: true, Signature: "OPAQUE"},
			core.TextBlock{Text: "done"}),
	}}, core.CacheRetentionNone)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if rep.Changed() || !strings.Contains(string(raw),
		`[{"type":"thinking","thinking":"","signature":"SIG"},{"type":"redacted_thinking","data":"OPAQUE"},{"type":"text","text":"done"}]`) {
		t.Fatalf("report %s, body %s", rep, raw)
	}
}

func TestMaxTokensIsTheRequestBoundCappedAtTheModelsOutputCap(t *testing.T) {
	cases := []struct {
		name      string
		cap       int
		requested *int
		want      float64
	}{
		{"no request takes the cap", 4096, nil, 4096},
		{"a smaller request stands", 4096, new(100), 100},
		{"a larger request is capped", 4096, new(9999), 4096},
		{"zero takes the cap", 4096, new(0), 4096},
		{"no cap keeps the request", 0, new(500), 500},
		{"no cap and no request is never zero", 0, nil, anthropic.DefaultMaxTokens},
	}
	for _, c := range cases {
		m := testModel()
		m.MaxOutputTokens = c.cap
		body := wireBody(t, m, core.Request{MaxTokens: c.requested}, anthropic.Options{})
		if body["max_tokens"] != c.want {
			t.Errorf("%s: max_tokens = %v, want %v", c.name, body["max_tokens"], c.want)
		}
		if msgs, ok := body["messages"].([]any); !ok || len(msgs) != 0 {
			t.Errorf("%s: messages = %v, want [] rather than null", c.name, body["messages"])
		}
	}
}

// Sampling knobs reach the wire as set, an explicit zero included, unless the
// model's row says it rejects them.
func TestSamplingReachesTheWireUnlessTheRowRejectsIt(t *testing.T) {
	req := core.Request{Temperature: new(0.0), TopP: new(0.9), StopSequences: []string{"END"}}
	for _, c := range []struct {
		compat string
		sent   bool
	}{
		{``, true},
		{`{"supports_sampling": true}`, true},
		{`{"supports_long_cache_retention": false}`, true},
		{`not json`, true},
		{`{"supports_sampling": false}`, false},
	} {
		m := testModel()
		m.Compat = json.RawMessage(c.compat)
		body := wireBody(t, m, req, anthropic.Options{})
		_, hasTemp := body["temperature"]
		_, hasTopP := body["top_p"]
		if hasTemp != c.sent || hasTopP != c.sent || (c.sent && (body["temperature"] != 0.0 || body["top_p"] != 0.9)) {
			t.Errorf("compat %q: temperature/top_p = %v/%v, want sent = %v", c.compat, body["temperature"], body["top_p"], c.sent)
		}
		if seq, _ := body["stop_sequences"].([]any); len(seq) != 1 || seq[0] != "END" {
			t.Errorf("compat %q: stop_sequences = %v", c.compat, body["stop_sequences"])
		}
	}
}

// Only auto and none reach the wire: a forced choice is a 400 on current
// models, and an unset choice is not auto.
func TestToolChoiceSendsOnlyAutoOrNone(t *testing.T) {
	for choice, want := range map[core.ToolChoice]any{
		core.ToolChoiceUnset: nil, core.ToolChoiceAuto: "auto", core.ToolChoiceNone: "none",
		"any": nil, "tool": nil, "search": nil,
	} {
		tc, _ := wireBody(t, testModel(), core.Request{ToolChoice: choice}, anthropic.Options{})["tool_choice"].(map[string]any)
		if (want == nil) != (tc == nil) || (tc != nil && (tc["type"] != want || len(tc) != 1)) {
			t.Errorf("tool choice %q went out as %v, want %v", choice, tc, want)
		}
	}
}

// A transcript from another model is replayed with its ids in this API's
// shape and its opaque material stripped, its results following their calls.
func TestAnotherModelsTranscriptIsReplayedInThisWiresShape(t *testing.T) {
	foreign := core.AssistantMessage{Model: "gpt-x", StopReason: core.StopReasonToolUse, Content: core.Content{
		core.ThinkingBlock{Thinking: "reasoned", Signature: "foreign-sig"},
		core.ToolUseBlock{ID: "call.1:" + strings.Repeat("x", 70), Name: "read", Input: json.RawMessage(`{}`), ThoughtSignature: "ts"},
	}}
	out, rep, err := anthropic.BuildRequest(testModel(), core.Request{Messages: core.Messages{
		user("go"), foreign, core.ToolResultMessage{ToolUseID: foreign.Content[1].(core.ToolUseBlock).ID, ToolName: "read"},
	}}, core.CacheRetentionNone)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	bs := blocks(decode(t, raw))
	id := "call_1_" + strings.Repeat("x", 57)
	if bs[1][0]["type"] != "text" || bs[1][0]["text"] != "reasoned" || bs[1][1]["id"] != id || bs[2][0]["tool_use_id"] != id {
		t.Fatalf("replayed as %s", raw)
	}
	if rep.RewrittenIDs != 1 || rep.DowngradedThinking != 1 || rep.StrippedSignatures != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestNormalizeToolCallIDKeepsTheAcceptedAlphabetWithinSixtyFourBytes(t *testing.T) {
	for in, want := range map[string]string{
		"":                      "",
		"toolu_01-AbC":          "toolu_01-AbC",
		"call.1:x/é":            "call_1_x__",
		strings.Repeat("a", 70): strings.Repeat("a", 64),
	} {
		if got := anthropic.NormalizeToolCallID(in); got != want {
			t.Errorf("NormalizeToolCallID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMapStopReason(t *testing.T) {
	for raw, want := range map[string]core.StopReason{
		"end_turn": core.StopReasonStop, "max_tokens": core.StopReasonLength, "tool_use": core.StopReasonToolUse,
		"stop_sequence": core.StopReasonStopSequence, "refusal": core.StopReasonRefusal,
		"pause_turn": core.StopReasonStop, "": core.StopReasonStop,
	} {
		if got := anthropic.MapStopReason(raw); got != want {
			t.Errorf("MapStopReason(%q) = %q, want %q", raw, got, want)
		}
	}
}
