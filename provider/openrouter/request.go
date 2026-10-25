package openrouter

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// chatBody is the request body: OpenRouter's Chat Completions request, written
// by AgentKit and not by the SDK, which would re-encode tool schemas and
// replayed reasoning (see
// docs/adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md).
type chatBody struct {
	Model               string         `json:"model"`
	Messages            []bodyMessage  `json:"messages"`
	Tools               []bodyTool     `json:"tools,omitempty"`
	ToolChoice          string         `json:"tool_choice,omitempty"`
	MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	Stop                []string       `json:"stop,omitempty"`
	Reasoning           *bodyReasoning `json:"reasoning,omitempty"`
	Stream              bool           `json:"stream"`
	StreamOptions       streamOptions  `json:"stream_options"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// bodyMessage is one message. Content is a pointer so that a message without
// text leaves it out, and a tool message with empty text still sends it.
type bodyMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []bodyToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	// ReasoningDetails are the fragments of the message's earlier reasoning,
	// replayed as they were received.
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}

type bodyToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function bodyFunction `json:"function"`
}

type bodyFunction struct {
	Name string `json:"name"`
	// Arguments is the call's input as the model wrote it.
	Arguments string `json:"arguments"`
}

type bodyTool struct {
	Type     string         `json:"type"`
	Function bodyToolSchema `json:"function"`
}

type bodyToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// emptyParameters declares a tool that has no schema.
const emptyParameters = `{"type":"object","properties":{}}`

// buildBody encodes the request. The prefix and the history are repaired first
// (provider.RepairToolPairing) so that every tool call has a result; each part
// the repair changed is reported through Options.Warnf.
func (c *client) buildBody(req core.Request) ([]byte, error) {
	m := c.model
	b := chatBody{
		Model:               m.ID,
		Messages:            []bodyMessage{},
		MaxCompletionTokens: completionLimit(m, req),
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		Stop:                req.StopSequences,
		Reasoning:           reasoningFor(m, req.Effort),
		Stream:              true,
		StreamOptions:       streamOptions{IncludeUsage: true},
	}
	if sys := systemText(req.System); sys != "" {
		b.Messages = append(b.Messages, bodyMessage{Role: "system", Content: &sys})
	}
	for _, part := range []struct {
		name string
		msgs core.Messages
	}{{"prefix", req.Prefix}, {"history", req.Messages}} {
		msgs, rep := provider.RepairToolPairing(part.msgs)
		if rep.Changed() {
			c.warnf("openrouter: repaired the %s: %s", part.name, rep)
		}
		for _, msg := range msgs {
			if enc, ok := encodeMessage(m, msg); ok {
				b.Messages = append(b.Messages, enc)
			}
		}
	}
	for _, t := range req.Tools {
		params := json.RawMessage(emptyParameters)
		if t.InputSchema != nil {
			raw, err := t.InputSchema.MarshalJSON()
			if err != nil {
				return nil, err
			}
			params = raw
		}
		b.Tools = append(b.Tools, bodyTool{Type: "function", Function: bodyToolSchema{
			Name: t.Name, Description: t.Description, Parameters: params,
		}})
	}
	switch req.ToolChoice {
	case core.ToolChoiceAuto:
		b.ToolChoice = "auto"
	case core.ToolChoiceNone:
		b.ToolChoice = "none"
	}

	// <, > and & are written as they are, not as \u003c and the like: the same
	// to a JSON reader, and a smaller body.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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

// completionLimit is max_completion_tokens: the request's bound capped at the
// model's output cap, else the cap, else nothing.
func completionLimit(m core.Model, req core.Request) *int {
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		n := *req.MaxTokens
		if m.MaxOutputTokens > 0 {
			n = min(n, m.MaxOutputTokens)
		}
		return &n
	}
	if m.MaxOutputTokens > 0 {
		n := m.MaxOutputTokens
		return &n
	}
	return nil
}

// encodeMessage encodes one transcript message for the model m. It reports false
// for a message that has nothing to send: a user message without text, and an
// assistant message without text, tool calls or reasoning to replay.
func encodeMessage(m core.Model, msg core.Message) (bodyMessage, bool) {
	switch v := msg.(type) {
	case core.UserMessage:
		text := v.Content.Text()
		if text == "" {
			return bodyMessage{}, false
		}
		return bodyMessage{Role: "user", Content: &text}, true
	case core.AssistantMessage:
		out := bodyMessage{Role: "assistant"}
		if text := v.Content.Text(); text != "" {
			out.Content = &text
		}
		for _, tu := range core.ExtractToolUse(&v) {
			out.ToolCalls = append(out.ToolCalls, bodyToolCall{ID: tu.ID, Type: "function",
				Function: bodyFunction{Name: tu.Name, Arguments: string(tu.Input)}})
		}
		// Reasoning is replayed to the model that produced it, as fragments and
		// never as text.
		if v.Model == m.ID {
			out.ReasoningDetails = replayedFragments(v.Content)
		}
		if out.Content == nil && len(out.ToolCalls) == 0 && out.ReasoningDetails == nil {
			return bodyMessage{}, false
		}
		return out, true
	case core.ToolResultMessage:
		text := provider.ToolResultText(v)
		return bodyMessage{Role: "tool", ToolCallID: v.ToolUseID, Content: &text}, true
	}
	return bodyMessage{}, false
}

// replayedFragments is the reasoning_details of an assistant message: the
// fragments of its thinking blocks, in block order. A block's fragments are its
// Signature read as a JSON array; one that is empty or not an array adds
// nothing. It returns nil when there is nothing to replay.
func replayedFragments(c core.Content) json.RawMessage {
	var all []json.RawMessage
	for _, blk := range c {
		tb, ok := blk.(core.ThinkingBlock)
		if !ok || tb.Signature == "" {
			continue
		}
		var frags []json.RawMessage
		if err := json.Unmarshal([]byte(tb.Signature), &frags); err != nil {
			continue
		}
		all = append(all, frags...)
	}
	if len(all) == 0 {
		return nil
	}
	raw, err := json.Marshal(all)
	if err != nil {
		return nil
	}
	return raw
}
