package ollama

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// chatMessages encodes the request's messages in the order Ollama reads them:
// one system message holding the text blocks of the system prompt, then the
// prefix, then the transcript. The transcript is repaired first
// (provider.RepairToolPairing) so that every tool call has a result; when the
// repair changed anything it is reported through Options.Warnf. The caller's
// messages are not changed.
func (c *Client) chatMessages(req core.Request) []api.Message {
	out := []api.Message{}
	if sys := systemText(req.System); sys != "" {
		out = append(out, api.Message{Role: "system", Content: sys})
	}
	history, rep := provider.RepairToolPairing(req.Messages)
	if rep.Changed() {
		c.warnf("ollama: repaired the transcript: %s", rep)
	}
	for _, part := range []core.Messages{req.Prefix, history} {
		for _, msg := range part {
			if enc, ok := c.encodeMessage(msg); ok {
				out = append(out, enc)
			}
		}
	}
	return out
}

// systemText joins the text blocks of the system prompt with a blank line.
func systemText(blocks []core.ContentBlock) string {
	var parts []string
	for _, blk := range blocks {
		if t, ok := blk.(core.TextBlock); ok && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// encodeMessage encodes one transcript message. It reports false for a message
// that has nothing to send: an assistant message with no text, no thinking to
// replay and no tool call.
func (c *Client) encodeMessage(msg core.Message) (api.Message, bool) {
	switch v := msg.(type) {
	case core.UserMessage:
		return api.Message{Role: "user", Content: v.Content.Text()}, true
	case core.AssistantMessage:
		return c.encodeAssistant(v)
	case core.ToolResultMessage:
		return api.Message{
			Role:       "tool",
			Content:    provider.ToolResultText(v),
			ToolName:   v.ToolName,
			ToolCallID: v.ToolUseID,
		}, true
	}
	return api.Message{}, false
}

// encodeAssistant encodes an assistant message: its text, its calls and, when the
// model that wrote it is the one this provider is for, its thinking. Thinking of
// any other model is meaningless to this one, and is never sent as content.
func (c *Client) encodeAssistant(v core.AssistantMessage) (api.Message, bool) {
	out := api.Message{Role: "assistant", Content: v.Content.Text()}
	if v.Model == c.model.ID {
		out.Thinking = thinkingText(v.Content)
	}
	for i, tu := range core.ExtractToolUse(&v) {
		out.ToolCalls = append(out.ToolCalls, api.ToolCall{
			ID:       tu.ID,
			Function: api.ToolCallFunction{Index: i, Name: tu.Name, Arguments: c.arguments(tu)},
		})
	}
	if out.Content == "" && out.Thinking == "" && len(out.ToolCalls) == 0 {
		return api.Message{}, false
	}
	return out, true
}

// thinkingText is the thinking of a message's blocks, concatenated, without the
// redacted ones, whose text is not for any model to read back.
func thinkingText(content core.Content) string {
	var b strings.Builder
	for _, blk := range content {
		if tb, ok := blk.(core.ThinkingBlock); ok && !tb.Redacted {
			b.WriteString(tb.Thinking)
		}
	}
	return b.String()
}

// arguments is the arguments of a call as the SDK's insertion-ordered map, with
// the keys in the order of the call's Input and each value as it was written: a
// number is not read through a float, and a nested object keeps its key order.
// Input is the authoritative bytes of the call, and it is not always the SDK's
// own rendering. Input that is not a JSON object is sent as no arguments.
func (c *Client) arguments(tu core.ToolUseBlock) api.ToolCallFunctionArguments {
	args := api.NewToolCallFunctionArguments()
	err := eachMember(tu.Input, func(key string, value json.RawMessage) { args.Set(key, value) })
	if err != nil {
		c.warnf("ollama: tool call %s (%s) has input that is not a JSON object; sending no arguments: %v", tu.ID, tu.Name, err)
		return api.NewToolCallFunctionArguments()
	}
	return args
}

// eachMember calls fn with each member of the JSON object raw, in the order it is
// written, with the value as written.
func eachMember(raw []byte, fn func(key string, value json.RawMessage)) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return errors.New("it does not start with {")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		fn(key, value)
	}
	_, err := dec.Token()
	return err
}
