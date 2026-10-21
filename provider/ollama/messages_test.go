package ollama_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
	"github.com/agent-fox-dev/agentkit-go/provider/ollama"
)

// wireMessage is a message of the request body, with the tool calls' arguments
// kept as written.
type wireMessage struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	Thinking  string `json:"thinking"`
	ToolName  string `json:"tool_name"`
	ToolCall  string `json:"tool_call_id"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Function struct {
			Index     int             `json:"index"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// sentMessages streams req and returns the messages of the request body.
func sentMessages(t *testing.T, c *ollama.Client, srv *recorder, req core.Request) []wireMessage {
	t.Helper()
	if o := run(t, c, req); o.err != nil {
		t.Fatal(o.err)
	}
	reqs := srv.Requests()
	var body struct {
		Messages []wireMessage `json:"messages"`
	}
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
		t.Fatalf("the request body is not JSON: %v", err)
	}
	return body.Messages
}

func roles(msgs []wireMessage) []string {
	var out []string
	for _, m := range msgs {
		out = append(out, m.Role)
	}
	return out
}

func userText(s string) core.UserMessage {
	return core.UserMessage{Content: core.Content{core.TextBlock{Text: s}}}
}

func call(id, name, input string) core.ToolUseBlock {
	b, _ := core.NewToolUse(id, name, json.RawMessage(input))
	return b
}

func toolResult(id, name, text string, isError bool) core.ToolResultMessage {
	return core.ToolResultMessage{ToolUseID: id, ToolName: name, Content: core.Content{core.TextBlock{Text: text}}, IsError: isError}
}

// plainServer answers every request with one text turn.
func plainServer(t *testing.T) *recorder {
	t.Helper()
	return newRecorder(t, lines(chunk("ok", ""), doneChunk("served-model", "stop")))
}

// TS-28-12: the messages are the system prompt, the prefix and then the
// transcript, and the system message is left out when its text is empty.
func TestMessageOrder_TS28_12(t *testing.T) {
	srv := plainServer(t)
	c := newClient(t, srv, testModel())
	req := core.Request{
		System:   []core.ContentBlock{core.TextBlock{Text: "A"}, core.TextBlock{Text: "B"}},
		Prefix:   core.Messages{userText("prefix")},
		Messages: core.Messages{userText("history")},
	}
	got := sentMessages(t, c, srv, req)
	if !reflect.DeepEqual(roles(got), []string{"system", "user", "user"}) || got[0].Content != "A\n\nB" ||
		got[1].Content != "prefix" || got[2].Content != "history" {
		t.Errorf("messages = %+v, want system \"A\\n\\nB\", the prefix, then the transcript", got)
	}

	req.System = []core.ContentBlock{core.TextBlock{Text: ""}}
	got = sentMessages(t, c, srv, req)
	if !reflect.DeepEqual(roles(got), []string{"user", "user"}) || got[0].Content != "prefix" {
		t.Errorf("with no system text, messages = %+v, want the prefix then the transcript", got)
	}
}

// brokenTranscript has an assistant call that nothing answered.
func brokenTranscript() core.Messages {
	return core.Messages{
		userText("go"),
		core.AssistantMessage{Model: "test-model:1b", StopReason: core.StopReasonToolUse,
			Content: core.Content{core.TextBlock{Text: "calling"}, call("call_1", "read", `{"path":"a"}`)}},
		userText("again"),
	}
}

// TS-28-19: the transcript is repaired before it is encoded, the repair is
// reported once through Warnf, and the caller's messages are not touched.
func TestTranscriptIsRepaired_TS28_19(t *testing.T) {
	var warnings []string
	srv := plainServer(t)
	c := newClient(t, srv, testModel(), func(o *ollama.Options) {
		o.Warnf = func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }
	})

	broken := brokenTranscript()
	before := append(core.Messages(nil), broken...)
	got := sentMessages(t, c, srv, core.Request{Messages: broken})
	if !reflect.DeepEqual(roles(got), []string{"user", "assistant", "tool", "user"}) ||
		!strings.Contains(got[2].Content, provider.SyntheticResultText) || got[2].ToolCall != "call_1" || got[2].ToolName != "read" {
		t.Errorf("messages = %+v, want a synthetic result paired to call_1", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "synthetic") {
		t.Errorf("warnings = %q, want one that reports the repair", warnings)
	}
	if !reflect.DeepEqual(broken, before) {
		t.Errorf("the caller's messages changed: %+v", broken)
	}

	warnings = nil
	sentMessages(t, c, srv, core.Request{Messages: core.Messages{userText("hello")}})
	if len(warnings) != 0 {
		t.Errorf("a clean transcript was reported: %q", warnings)
	}

	// With no Warnf the repair is made and nothing is reported.
	quiet := newClient(t, srv, testModel())
	if got := sentMessages(t, quiet, srv, core.Request{Messages: brokenTranscript()}); len(got) != 4 {
		t.Errorf("without Warnf, messages = %+v, want the repaired four", got)
	}
}

// TS-28-20 (integration): a user message, an assistant message with two calls
// and their results are encoded with ids, indexes, tool names and the Error
// prefix.
func TestMessagesAreEncoded_TS28_20(t *testing.T) {
	srv := plainServer(t)
	c := newClient(t, srv, testModel())
	msgs := core.Messages{
		userText("go"),
		core.AssistantMessage{Model: "test-model:1b", StopReason: core.StopReasonToolUse, Content: core.Content{
			core.TextBlock{Text: "two calls"}, call("call_a", "read", `{"p":1}`), call("call_b", "write", `{"p":2}`)}},
		toolResult("call_a", "read", "fine", false),
		toolResult("call_b", "write", "boom", true),
	}
	got := sentMessages(t, c, srv, core.Request{Messages: msgs})
	if !reflect.DeepEqual(roles(got), []string{"user", "assistant", "tool", "tool"}) {
		t.Fatalf("roles = %v", roles(got))
	}
	if got[0].Content != "go" || got[1].Content != "two calls" || len(got[1].ToolCalls) != 2 {
		t.Fatalf("user %q, assistant %+v", got[0].Content, got[1])
	}
	for i, want := range []struct{ id, name string }{{"call_a", "read"}, {"call_b", "write"}} {
		tc := got[1].ToolCalls[i]
		if tc.ID != want.id || tc.Function.Index != i || tc.Function.Name != want.name {
			t.Errorf("tool call %d = %+v, want id %s, index %d, name %s", i, tc, want.id, i, want.name)
		}
	}
	if got[2].ToolCall != "call_a" || got[2].ToolName != "read" || got[2].Content != "fine" {
		t.Errorf("first result = %+v", got[2])
	}
	if got[3].ToolCall != "call_b" || got[3].ToolName != "write" || got[3].Content != "Error: boom" {
		t.Errorf("second result = %+v, want the Error: prefix", got[3])
	}
}

// TS-28-21 (property): replayed arguments keep the key order of the call's Input,
// for keys that are not alphabetical, hold unicode and are nested.
func TestReplayedArgumentsKeepKeyOrder_TS28_21(t *testing.T) {
	r := rand.New(rand.NewSource(21))
	pool := []string{"zeta", "alpha", "mid", "é", "日本", "ключ", "k-1", "a b", "Zed", "10", "9", "_"}
	srv := plainServer(t)
	c := newClient(t, srv, testModel())

	for i := range 100 {
		r.Shuffle(len(pool), func(a, b int) { pool[a], pool[b] = pool[b], pool[a] })
		keys := append([]string(nil), pool[:1+r.Intn(len(pool)-1)]...)
		var parts []string
		for j, k := range keys {
			kb, _ := json.Marshal(k)
			val := fmt.Sprint(j)
			if r.Intn(3) == 0 {
				val = `{"y":1,"b":[2,{"z":0,"a":1}]}`
			}
			parts = append(parts, string(kb)+":"+val)
		}
		input := "{" + strings.Join(parts, ",") + "}"
		got := sentMessages(t, c, srv, core.Request{Messages: core.Messages{
			userText("go"),
			core.AssistantMessage{Model: "test-model:1b", StopReason: core.StopReasonToolUse, Content: core.Content{call("c", "f", input)}},
			toolResult("c", "f", "ok", false),
		}})
		if len(got) < 2 || len(got[1].ToolCalls) != 1 {
			t.Fatalf("case %d: messages = %+v, want the call replayed", i, got)
		}
		if order := keyOrder(t, got[1].ToolCalls[0].Function.Arguments); !reflect.DeepEqual(order, keys) {
			t.Fatalf("case %d: key order = %q, want %q (input %s)", i, order, keys, input)
		}
	}
}

// An argument is replayed as it was written: a number is not re-rendered through
// a float and a nested object keeps its key order, because Input is the
// authoritative bytes of the call.
func TestReplayedArgumentsKeepTheirBytes_TS28_21(t *testing.T) {
	srv := plainServer(t)
	c := newClient(t, srv, testModel())
	const input = `{"id":12345678901234567890,"price":1.50,"nested":{"z":1,"a":[3,{"y":0,"b":1}]},"text":"a<b&c"}`
	got := sentMessages(t, c, srv, core.Request{Messages: core.Messages{
		userText("go"),
		core.AssistantMessage{Model: "test-model:1b", StopReason: core.StopReasonToolUse, Content: core.Content{call("c", "f", input)}},
		toolResult("c", "f", "ok", false),
	}})
	if len(got) < 2 || len(got[1].ToolCalls) != 1 {
		t.Fatalf("messages = %+v, want the call replayed", got)
	}
	args := string(got[1].ToolCalls[0].Function.Arguments)
	for _, want := range []string{`"id":12345678901234567890`, `"price":1.50`, `"nested":{"z":1,"a":[3,{"y":0,"b":1}]}`} {
		if !strings.Contains(args, want) {
			t.Errorf("arguments = %s, want them to contain %s", args, want)
		}
	}
}

// TS-28-22: thinking is replayed only from unredacted blocks of messages the same
// model wrote, and never as content.
func TestThinkingReplay_TS28_22(t *testing.T) {
	srv := plainServer(t)
	c := newClient(t, srv, testModel())
	msgs := core.Messages{
		userText("one"),
		core.AssistantMessage{Model: "test-model:1b", Content: core.Content{
			core.ThinkingBlock{Thinking: "mine1"}, core.ThinkingBlock{Thinking: "redacted-secret", Redacted: true},
			core.ThinkingBlock{Thinking: "mine2"}, core.TextBlock{Text: "answer one"}}},
		userText("two"),
		core.AssistantMessage{Model: "other-model", Content: core.Content{
			core.ThinkingBlock{Thinking: "foreign-thought"}, core.TextBlock{Text: "answer two"}}},
		userText("three"),
	}
	got := sentMessages(t, c, srv, core.Request{Messages: msgs})
	if len(got) != 5 || got[1].Thinking != "mine1mine2" || got[1].Content != "answer one" {
		t.Fatalf("same-model message = %+v, want thinking \"mine1mine2\" and its text", got[1])
	}
	if got[3].Thinking != "" || got[3].Content != "answer two" {
		t.Errorf("other-model message = %+v, want its text and no thinking", got[3])
	}
	raw := string(srv.Requests()[0].Body)
	for _, s := range []string{"redacted-secret", "foreign-thought"} {
		if strings.Contains(raw, s) {
			t.Errorf("the request body carries %q", s)
		}
	}
}

// An assistant message with nothing to send (no text, no tool call, and thinking
// that is redacted or another model's) is left out rather than sent empty.
func TestAnAssistantMessageWithNothingToSendIsLeftOut_TS28_22(t *testing.T) {
	srv := plainServer(t)
	c := newClient(t, srv, testModel())
	got := sentMessages(t, c, srv, core.Request{Messages: core.Messages{
		userText("one"),
		core.AssistantMessage{Model: "test-model:1b", Content: core.Content{core.ThinkingBlock{Thinking: "secret", Redacted: true}}},
		core.AssistantMessage{Model: "other-model", Content: core.Content{core.ThinkingBlock{Thinking: "foreign"}}},
		userText("two"),
	}})
	if !reflect.DeepEqual(roles(got), []string{"user", "user"}) {
		t.Errorf("roles = %v, want the two user messages", roles(got))
	}
}
