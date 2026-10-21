package openrouter_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/openrouter"
)

// TS-27-20: Stream repairs the prefix and the history, tells Warnf once for
// each part it changed, sends the repaired transcript, and says nothing about a
// clean request.
func TestStreamRepairsAndWarns_TS27_20(t *testing.T) {
	srv := newReplay(t, okTurn())
	var warns []string
	c := newProvider(t, srv, func(o *openrouter.Options) {
		o.Warnf = func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }
	})
	broken := core.Request{
		Prefix: core.Messages{
			userMsg("prefix"),
			core.AssistantMessage{Content: core.Content{toolCall("c1", "read", `{}`)}, StopReason: core.StopReasonToolUse},
		},
		Messages: core.Messages{
			userMsg("q"),
			core.AssistantMessage{Content: core.Content{core.TextBlock{Text: "half"}}, StopReason: core.StopReasonError},
			userMsg("again"),
		},
	}
	run(t, c, broken)
	if len(warns) != 2 || !strings.Contains(warns[0], "prefix") || !strings.Contains(warns[1], "history") {
		t.Errorf("warnings = %q, want one for the prefix and one for the history", warns)
	}
	msgs := messagesOf(sentBody(t, srv, 0))
	if got, want := strings.Join(roles(msgs), ","), "user,assistant,tool,user,user"; got != want {
		t.Fatalf("roles = %s, want %s (a synthetic result, and the errored turn dropped)", got, want)
	}
	if msgs[2]["tool_call_id"] != "c1" || msgs[2]["content"] != "Error: No result provided" {
		t.Errorf("synthetic result = %v, want call c1 and Error: No result provided", msgs[2])
	}

	run(t, c, userRequest("clean"))
	if len(warns) != 2 {
		t.Errorf("a clean request added warnings: %q", warns[2:])
	}
}

// TS-27-21: a tool-call id another provider issued is sent as it is.
func TestForeignToolCallIDsPassThrough_TS27_21(t *testing.T) {
	srv := newReplay(t, okTurn())
	req := core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Model: "claude-other", Content: core.Content{toolCall("toolu_01abc", "read", `{}`)}, StopReason: core.StopReasonToolUse},
		core.ToolResultMessage{ToolUseID: "toolu_01abc", ToolName: "read", Content: core.Content{core.TextBlock{Text: "r"}}},
	}}
	run(t, newProvider(t, srv), req)
	msgs := messagesOf(sentBody(t, srv, 0))
	call := msgs[1]["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "toolu_01abc" || msgs[2]["tool_call_id"] != "toolu_01abc" {
		t.Errorf("ids = %v and %v, want toolu_01abc in both", call["id"], msgs[2]["tool_call_id"])
	}
}

// TS-27-22: reasoning_details is the concatenation of the thinking blocks'
// fragment arrays, in block order, for a message of the same model only; an
// empty or non-array signature contributes nothing.
func TestReasoningDetailsReplayForTheSameModelOnly_TS27_22(t *testing.T) {
	frag1 := `{"type":"reasoning.text","text":"one","signature":"s1","format":"anthropic-claude-v1","index":0,"id":"a","extra":{"k":[1,2]}}`
	frag2 := `{"type":"reasoning.encrypted","data":"AAAA==","format":"openai-responses-v1","index":1,"id":"b"}`
	frag3 := `{"type":"reasoning.summary","summary":"three","index":2}`
	srv := newReplay(t, okTurn())
	req := core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Model: "vendor/model", Content: core.Content{
			core.ThinkingBlock{Thinking: "one", Signature: "[" + frag1 + "," + frag2 + "]"},
			core.ThinkingBlock{Thinking: "two", Signature: "[" + frag3 + "]"},
			core.ThinkingBlock{Thinking: "three", Signature: ""},
			core.ThinkingBlock{Thinking: "four", Signature: "not json"},
			core.TextBlock{Text: "answer"},
		}, StopReason: core.StopReasonStop},
		userMsg("next"),
		core.AssistantMessage{Model: "other/model", Content: core.Content{
			core.ThinkingBlock{Thinking: "foreign", Signature: "[" + frag1 + "]"},
			core.TextBlock{Text: "foreign answer"},
		}, StopReason: core.StopReasonStop},
		userMsg("last"),
	}}
	run(t, newProvider(t, srv), req)

	msgs := messagesOf(sentBody(t, srv, 0))
	if got, want := strings.Join(roles(msgs), ","), "user,assistant,user,assistant,user"; got != want {
		t.Fatalf("roles = %s, want %s", got, want)
	}
	details, err := jsonText(msgs[1]["reasoning_details"])
	if err != nil || !jsonEqual(t, details, "["+frag1+","+frag2+","+frag3+"]") {
		t.Errorf("reasoning_details = %s (%v), want the three fragments in block order with every field", details, err)
	}
	if _, has := msgs[3]["reasoning_details"]; has {
		t.Errorf("a message from another model sent reasoning_details %v", msgs[3]["reasoning_details"])
	}
	if msgs[1]["content"] != "answer" {
		t.Errorf("content = %v, want only the answer text", msgs[1]["content"])
	}
}

// TS-27-23: thinking text never reaches a content field, and an assistant
// message that holds only thinking text and no signature is not sent.
func TestThinkingTextIsNeverContent_TS27_23(t *testing.T) {
	const marker = "UNIQUE-MARKER-7f3"
	srv := newReplay(t, okTurn())
	req := core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Model: "vendor/model", Content: core.Content{core.ThinkingBlock{Thinking: marker}}, StopReason: core.StopReasonStop},
		userMsg("again"),
		core.AssistantMessage{Model: "other/model", Content: core.Content{
			core.ThinkingBlock{Thinking: marker, Signature: `[{"type":"reasoning.text","text":"x"}]`},
			core.TextBlock{Text: "visible"},
		}, StopReason: core.StopReasonStop},
		userMsg("last"),
	}}
	run(t, newProvider(t, srv), req)

	body := srv.Requests()[0].Body
	var sent map[string]any
	if !jsonUnmarshal(t, body, &sent) {
		return
	}
	for _, m := range messagesOf(sent) {
		if c, _ := m["content"].(string); strings.Contains(c, marker) {
			t.Errorf("a %s message's content carries the thinking text", m["role"])
		}
	}
	if got, want := strings.Join(roles(messagesOf(sent)), ","), "user,user,assistant,user"; got != want {
		t.Errorf("roles = %s, want %s (the thinking-only assistant message is skipped)", got, want)
	}
}

// A message whose thinking has a signature and no text and no tool calls is
// still sent: the fragments are what is replayed.
func TestReasoningAloneIsReplayed(t *testing.T) {
	srv := newReplay(t, okTurn())
	req := core.Request{Messages: core.Messages{
		userMsg("q"),
		core.AssistantMessage{Model: "vendor/model", Content: core.Content{
			core.ThinkingBlock{Thinking: "t", Signature: `[{"type":"reasoning.encrypted","data":"AAAA=="}]`},
		}, StopReason: core.StopReasonStop},
		userMsg("again"),
	}}
	run(t, newProvider(t, srv), req)
	msgs := messagesOf(sentBody(t, srv, 0))
	if got, want := strings.Join(roles(msgs), ","), "user,assistant,user"; got != want {
		t.Fatalf("roles = %s, want %s", got, want)
	}
	if _, has := msgs[1]["content"]; has || msgs[1]["reasoning_details"] == nil {
		t.Errorf("assistant message = %v, want reasoning_details and no content", msgs[1])
	}
}
