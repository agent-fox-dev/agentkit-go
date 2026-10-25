package ollama_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// A call nothing answered gets a synthetic result, reported once through
// Warnf (or silently without one); the caller's messages are not changed and a
// clean transcript is not reported.
func TestABrokenTranscriptIsRepairedAndReported(t *testing.T) {
	broken := func() core.Messages {
		return core.Messages{userText("go"), core.AssistantMessage{Model: "test-model:1b",
			Content: core.Content{core.TextBlock{Text: "calling"}, call("call_1", "read", `{"path":"a"}`)}}, userText("again")}
	}
	srv := newServer(t, okTurn())
	warn, warns := collectWarnings()
	c := newClient(t, srv, testModel(), warn)
	msgs, before := broken(), broken()
	got := sentMessages(t, c, srv, core.Request{Messages: msgs})
	if roles(got) != "user,assistant,tool,user" || !strings.Contains(got[2].Content, provider.SyntheticResultText) ||
		got[2].ToolCallID != "call_1" || got[2].ToolName != "read" {
		t.Errorf("messages = %+v, want a synthetic result paired to call_1", got)
	}
	if len(*warns) != 1 || !strings.Contains((*warns)[0], "synthetic") || !reflect.DeepEqual(msgs, before) {
		t.Errorf("warnings = %q, caller's messages changed %v", *warns, !reflect.DeepEqual(msgs, before))
	}
	sentMessages(t, c, srv, userRequest("clean"))
	if quiet := sentMessages(t, newClient(t, srv, testModel()), srv, core.Request{Messages: broken()}); len(*warns) != 1 || len(quiet) != 4 {
		t.Errorf("warnings = %q, unwarned messages %d; want one warning and the repaired four", *warns, len(quiet))
	}
}

// The messages are one system message of the system text blocks (none when
// they are empty), the prefix, then the transcript: an assistant message with
// its text and its calls (ids and indexes), each result a tool message with
// the call's id, the tool's name and the Error prefix when it failed.
func TestTheTranscriptIsEncodedAfterTheSystemPromptAndThePrefix(t *testing.T) {
	srv := newServer(t, okTurn())
	c := newClient(t, srv, testModel())
	req := core.Request{
		System: []core.ContentBlock{core.TextBlock{Text: "A"}, core.TextBlock{}, core.TextBlock{Text: "B"}},
		Prefix: core.Messages{userText("prefix")},
		Messages: core.Messages{
			userText("go"),
			core.AssistantMessage{Model: "test-model:1b", Content: core.Content{
				core.TextBlock{Text: "two calls"}, call("call_a", "read", `{"p":1}`), call("call_b", "write", `{"p":2}`)}},
			toolResult("call_a", "read", "fine", false),
			toolResult("call_b", "write", "boom", true),
		},
	}
	got := sentMessages(t, c, srv, req)
	if roles(got) != "system,user,user,assistant,tool,tool" || got[0].Content != "A\n\nB" || got[1].Content != "prefix" ||
		got[2].Content != "go" || got[3].Content != "two calls" || len(got[3].ToolCalls) != 2 {
		t.Fatalf("messages = %+v", got)
	}
	for i, name := range []string{"read", "write"} {
		if tc := got[3].ToolCalls[i]; tc.ID != []string{"call_a", "call_b"}[i] || tc.Function.Index != i || tc.Function.Name != name {
			t.Errorf("tool call %d = %+v", i, tc)
		}
	}
	if got[4].ToolCallID != "call_a" || got[4].ToolName != "read" || got[4].Content != "fine" ||
		got[5].ToolCallID != "call_b" || got[5].ToolName != "write" || got[5].Content != "Error: boom" {
		t.Errorf("results = %+v, %+v", got[4], got[5])
	}
	req.System = []core.ContentBlock{core.TextBlock{}}
	if got := sentMessages(t, c, srv, req); roles(got) != "user,user,assistant,tool,tool" {
		t.Errorf("with no system text, roles = %s", roles(got))
	}
}

// Replayed arguments keep the key order of the call's Input and each value as
// written, nested key order and number text included; Input that is not an
// object is sent as no arguments, with a warning.
func TestReplayedArgumentsKeepTheInputsKeysAndBytes(t *testing.T) {
	for _, tc := range []struct {
		input, holds string // holds is what the sent arguments contain; "" for none
	}{
		{`{"zeta":0,"alpha":1,"é":2,"日本":3,"a b":4,"10":5,"9":6,"_":7}`, `{"zeta":0,"alpha":1,"é":2,"日本":3,"a b":4,"10":5,"9":6,"_":7}`},
		{`{"id":12345678901234567890,"price":1.50,"nested":{"z":1,"a":[3,{"y":0,"b":1}]},"text":"a<b&c"}`,
			`{"id":12345678901234567890,"price":1.50,"nested":{"z":1,"a":[3,{"y":0,"b":1}]},"text":`},
		{`[1,2]`, ""},
		{`{"a":1,}`, ""},
		{`{"a" 1}`, ""},
		{`{"a":1`, ""},
	} {
		srv := newServer(t, okTurn())
		warn, warns := collectWarnings()
		got := sentMessages(t, newClient(t, srv, testModel(), warn), srv, core.Request{Messages: core.Messages{
			userText("go"),
			core.AssistantMessage{Model: "test-model:1b", Content: core.Content{call("c", "f", tc.input)}},
			toolResult("c", "f", "ok", false),
		}})
		args := string(got[1].ToolCalls[0].Function.Arguments)
		if tc.holds != "" && (!strings.HasPrefix(args, tc.holds) || len(*warns) != 0) {
			t.Errorf("arguments = %s (warnings %q), want them to start %s", args, *warns, tc.holds)
		}
		if tc.holds == "" && (args != "{}" && args != "" || len(*warns) != 1 || !strings.Contains((*warns)[0], "not a JSON object")) {
			t.Errorf("%s: arguments %s, warnings %q; want none and a warning", tc.input, args, *warns)
		}
	}
}

// Thinking is replayed only from the unredacted blocks of the model's own
// messages, never as content; a message left with nothing to send is dropped.
func TestThinkingIsReplayedToTheSameModelOnly(t *testing.T) {
	srv := newServer(t, okTurn())
	got := sentMessages(t, newClient(t, srv, testModel()), srv, core.Request{Messages: core.Messages{
		userText("one"),
		core.AssistantMessage{Model: "test-model:1b", Content: core.Content{core.ThinkingBlock{Thinking: "mine1"},
			core.ThinkingBlock{Thinking: "redacted-secret", Redacted: true}, core.ThinkingBlock{Thinking: "mine2"}, core.TextBlock{Text: "answer one"}}},
		userText("two"),
		core.AssistantMessage{Model: "other-model", Content: core.Content{core.ThinkingBlock{Thinking: "foreign-thought"}, core.TextBlock{Text: "answer two"}}},
		core.AssistantMessage{Model: "test-model:1b", Content: core.Content{core.ThinkingBlock{Thinking: "redacted-secret", Redacted: true}}},
		core.AssistantMessage{Model: "other-model", Content: core.Content{core.ThinkingBlock{Thinking: "foreign-thought"}}},
		userText("three"),
	}})
	if roles(got) != "user,assistant,user,assistant,user" || got[1].Thinking != "mine1mine2" || got[1].Content != "answer one" ||
		got[3].Thinking != "" || got[3].Content != "answer two" {
		t.Fatalf("messages = %+v", got)
	}
	for _, s := range []string{"redacted-secret", "foreign-thought"} {
		if strings.Contains(string(srv.Requests()[0].Body), s) {
			t.Errorf("the body carries %q", s)
		}
	}
}
