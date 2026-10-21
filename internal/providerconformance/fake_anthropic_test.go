package providerconformance

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// anthropicVendor is the Anthropic Messages API: server-sent events at
// POST /v1/messages.
var anthropicVendor = vendor{
	name: "anthropic",
	path: "/v1/messages",
	env: func(url string) map[string]string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-ant-conformance", "ANTHROPIC_BASE_URL": url}
	},
	render: renderAnthropic,
	decode: decodeAnthropic,
}

// renderAnthropic answers one request the way the Messages API does: a
// message_start whose usage holds the input net of the cache reads, a block per
// content block with its deltas, a message_delta with the stop reason and the
// output tokens, and message_stop. The tool-use stop is tool_use, the normal one
// end_turn.
func renderAnthropic(w http.ResponseWriter, model string, t logicalTurn) {
	if t.end == endStatus {
		writeJSONStatus(w, t.status, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": anthropicErrorType(t.status), "message": t.message},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	u := t.usageOrDefault()
	writeSSE(w, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_conformance", "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":            u.prompt - u.cacheRead,
				"cache_read_input_tokens": u.cacheRead,
				"output_tokens":           1,
			},
		},
	})
	for i, b := range t.blocks {
		cut := t.end == endCutOff && i == len(t.blocks)-1
		renderAnthropicBlock(w, i, b, cut)
	}
	switch t.end {
	case endCutOff:
		return
	case endInStream:
		writeSSE(w, "error", map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": t.message},
		})
		return
	}
	stop := "end_turn"
	if t.end == endToolStop {
		stop = "tool_use"
	}
	writeSSE(w, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": u.output},
	})
	writeSSE(w, "message_stop", map[string]any{"type": "message_stop"})
}

// renderAnthropicBlock writes the events of block i: its start, its deltas
// (text and thinking in two, tool arguments in two fragments), the signature of
// a thinking block, and its stop. A cut block ends after its first delta, with
// no stop.
func renderAnthropicBlock(w http.ResponseWriter, i int, b block, cut bool) {
	var start map[string]any
	var deltas []map[string]any
	switch b.kind {
	case blockText:
		start = map[string]any{"type": "text", "text": ""}
		for _, part := range halves(b.text) {
			deltas = append(deltas, map[string]any{"type": "text_delta", "text": part})
		}
	case blockThinking:
		start = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
		for _, part := range halves(b.text) {
			deltas = append(deltas, map[string]any{"type": "thinking_delta", "thinking": part})
		}
		if b.signature != "" {
			deltas = append(deltas, map[string]any{"type": "signature_delta", "signature": b.signature})
		}
	case blockToolCall:
		start = map[string]any{"type": "tool_use", "id": b.id, "name": b.name, "input": map[string]any{}}
		for _, part := range halves(b.args) {
			deltas = append(deltas, map[string]any{"type": "input_json_delta", "partial_json": part})
		}
	}
	writeSSE(w, "content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": start})
	for j, d := range deltas {
		writeSSE(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": i, "delta": d})
		if cut && j == 0 {
			return
		}
	}
	writeSSE(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
}

// anthropicErrorType is the error type the API names for an HTTP status.
func anthropicErrorType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 404:
		return "not_found_error"
	case 429:
		return "rate_limit_error"
	case 529:
		return "overloaded_error"
	}
	return "api_error"
}

// anthropicBlock is a content block of a request message.
type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

// decodeAnthropic summarizes a Messages request: the effort is
// output_config.effort, the tools are tools[].name, a user message holds the
// user's text blocks and the tool_result blocks, and an assistant message holds
// text, thinking with its signature and tool_use blocks.
func decodeAnthropic(body []byte) (requestSummary, error) {
	var req struct {
		Model        string `json:"model"`
		OutputConfig struct {
			Effort string `json:"effort"`
		} `json:"output_config"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return requestSummary{}, fmt.Errorf("anthropic request: %w", err)
	}
	s := requestSummary{Model: req.Model, Effort: req.OutputConfig.Effort}
	for _, t := range req.Tools {
		s.Tools = append(s.Tools, t.Name)
	}
	for _, m := range req.Messages {
		var blocks []anthropicBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			// A message whose content is a plain string.
			blocks = []anthropicBlock{{Type: "text", Text: wireContent(m.Content)}}
		}
		switch m.Role {
		case "user":
			for _, b := range blocks {
				switch b.Type {
				case "text":
					s.UserTexts = append(s.UserTexts, b.Text)
				case "tool_result":
					s.Results = append(s.Results, toolResult{CallID: b.ToolUseID, Text: wireContent(b.Content)})
				}
			}
		case "assistant":
			var a assistantSummary
			for _, b := range blocks {
				switch b.Type {
				case "text":
					a.Text += b.Text
				case "thinking":
					a.Thinking += b.Thinking
					a.RawThinking = rawJSON(struct {
						Thinking  string `json:"thinking"`
						Signature string `json:"signature"`
					}{b.Thinking, b.Signature})
				case "tool_use":
					a.ToolCalls = append(a.ToolCalls, toolCall{ID: b.ID, Name: b.Name, Args: b.Input})
				}
			}
			s.Assistants = append(s.Assistants, a)
		}
	}
	return s, nil
}
