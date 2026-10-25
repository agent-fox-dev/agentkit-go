package core

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNewToolUseKeepsAnObjectsBytesAndNormalizesEmptyInput(t *testing.T) {
	for in, want := range map[string]string{``: `{}`, " \t\n": `{}`, ` {"z":1, "a":2} `: `{"z":1, "a":2}`} {
		b, err := NewToolUse("1", "t", json.RawMessage(in))
		if err != nil || string(b.Input) != want || b.ID != "1" || b.Name != "t" {
			t.Errorf("NewToolUse(%q) = %+v, %v; want input %s", in, b, err, want)
		}
	}
	if b, err := NewToolUse("1", "t", nil); err != nil || string(b.Input) != "{}" {
		t.Errorf("NewToolUse(nil) = %s, %v", b.Input, err)
	}
	// Bytes after the object would make a replayed request body invalid JSON.
	for _, in := range []string{`{"a":1}]`, `{"a":1}}`, `{"a":1} {}`, `[1,2]`, `"s"`, `{"a":`} {
		if _, err := NewToolUse("1", "t", []byte(in)); err == nil {
			t.Errorf("NewToolUse(%s) accepted it", in)
		}
	}
}

func TestToolUseInputMapKeepsNumbersVerbatim(t *testing.T) {
	m := toolUse(t, "1", "t", `{"n":9007199254740993,"o":{"f":1.50}}`).InputMap()
	if m["n"] != json.Number("9007199254740993") || m["o"].(map[string]any)["f"] != json.Number("1.50") {
		t.Fatalf("InputMap() = %#v", m)
	}
	if (ToolUseBlock{Input: json.RawMessage(`[`)}).InputMap() != nil {
		t.Fatal("an undecodable input must map to nil")
	}
}

func TestMessagesReportTheirRoleAndBlocksTheirType(t *testing.T) {
	for _, c := range []struct {
		m    Message
		want Role
	}{{UserMessage{}, RoleUser}, {AssistantMessage{}, RoleAssistant}, {ToolResultMessage{}, RoleToolResult}} {
		if c.m.Role() != c.want {
			t.Errorf("%T.Role() = %q, want %q", c.m, c.m.Role(), c.want)
		}
	}
	for _, c := range []struct {
		b    ContentBlock
		want BlockType
	}{{TextBlock{}, BlockText}, {ThinkingBlock{}, BlockThinking}, {ToolUseBlock{}, BlockToolUse}} {
		if c.b.BlockType() != c.want {
			t.Errorf("%T.BlockType() = %q, want %q", c.b, c.b.BlockType(), c.want)
		}
	}
}

func TestContentTextConcatenatesOnlyTextBlocks(t *testing.T) {
	c := Content{TextBlock{Text: "a"}, ThinkingBlock{Thinking: "no"}, toolUse(t, "1", "t", `{}`), TextBlock{Text: "b"}}
	if got := c.Text(); got != "ab" {
		t.Fatalf("Text() = %q, want ab", got)
	}
}

func TestMessageCloneSharesNothingMutable(t *testing.T) {
	exit := 3
	usage := Usage{InputTokens: 1}
	msgs := Messages{
		UserMessage{Content: Content{TextBlock{Text: "u"}, toolUse(t, "1", "t", `{"a":1}`)}},
		AssistantMessage{Content: Content{ThinkingBlock{Thinking: "th"}, toolUse(t, "2", "t", `{"b":2}`)}},
		ToolResultMessage{ToolUseID: "2", Content: Content{TextBlock{Text: "r"}}, AddedToolNames: []string{"x"},
			Usage: &usage, Metadata: &ToolMetadata{ExitCode: &exit, Outcome: "exit"}},
	}
	before := msgs.Clone()
	c := msgs.Clone()
	if !reflect.DeepEqual(c, msgs) {
		t.Fatalf("Clone() = %+v, want an equal copy", c)
	}
	c[0].(UserMessage).Content[0] = TextBlock{Text: "changed"}
	c[0].(UserMessage).Content[1].(ToolUseBlock).Input[2] = 'X'
	c[1].(AssistantMessage).Content[1].(ToolUseBlock).Input[2] = 'X'
	tr := c[2].(ToolResultMessage)
	tr.Content[0], tr.AddedToolNames[0] = TextBlock{Text: "changed"}, "changed"
	tr.Usage.InputTokens, tr.Metadata.Outcome, *tr.Metadata.ExitCode = 9, "changed", 9
	if !reflect.DeepEqual(msgs, before) {
		t.Fatalf("mutating a clone changed the original:\n%+v\nwant\n%+v", msgs, before)
	}
	if Messages(nil).Clone() != nil || Content(nil).Clone() != nil {
		t.Fatal("a nil slice clones to nil")
	}
	if c := (ToolResultMessage{}).Clone().(ToolResultMessage); c.Usage != nil || c.Metadata != nil {
		t.Fatalf("an empty result cloned to %+v", c)
	}
}
