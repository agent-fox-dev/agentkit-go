package providerconformance

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// ollamaVendor is Ollama's native chat API: NDJSON at POST /api/chat.
var ollamaVendor = vendor{
	name: "ollama",
	path: "/api/chat",
	env: func(url string) map[string]string {
		return map[string]string{"OLLAMA_HOST": url}
	},
	render: renderOllama,
	decode: decodeOllama,
	// The thinking is replayed as the text it was sent as.
	rawThinking: func(b block) json.RawMessage { return rawJSON(b.text) },
}

// renderOllama answers one request the way /api/chat does: a line per delta
// (content, thinking), a line per tool call carrying all of it, and a last line
// with done, the done_reason and the counts. The prompt count includes the
// cached tokens, and Ollama reports no cost. Both stops are done_reason stop.
func renderOllama(w http.ResponseWriter, model string, t logicalTurn) {
	if t.end == endStatus {
		writeJSONStatus(w, t.status, map[string]any{"error": t.message})
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	line := func(msg map[string]any) map[string]any {
		msg["role"] = "assistant"
		return map[string]any{"model": model, "created_at": "2026-10-21T10:00:00Z", "message": msg, "done": false}
	}

	calls := 0
blocks:
	for i, b := range t.blocks {
		cut := t.end == endCutOff && i == len(t.blocks)-1
		switch b.kind {
		case blockText:
			for j, part := range halves(b.text) {
				writeLine(w, line(map[string]any{"content": part}))
				if cut && j == 0 {
					break blocks
				}
			}
		case blockThinking:
			for j, part := range halves(b.text) {
				writeLine(w, line(map[string]any{"content": "", "thinking": part}))
				if cut && j == 0 {
					break blocks
				}
			}
		case blockToolCall:
			// Ollama sends a call whole, whatever the arguments' size.
			writeLine(w, line(map[string]any{"content": "", "tool_calls": []any{map[string]any{
				"id": b.id,
				"function": map[string]any{
					"index": calls, "name": b.name, "arguments": json.RawMessage(b.args),
				},
			}}}))
			calls++
			if cut {
				break blocks
			}
		}
	}

	switch t.end {
	case endCutOff:
		return
	case endInStream:
		writeLine(w, map[string]any{"error": t.message})
		return
	}
	u := t.usageOrDefault()
	last := line(map[string]any{"content": ""})
	last["done"], last["done_reason"] = true, "stop"
	last["prompt_eval_count"], last["prompt_eval_cached_count"], last["eval_count"] = u.prompt, u.cacheRead, u.output
	writeLine(w, last)
}

// decodeOllama summarizes a /api/chat request: the effort is think, the tools
// are tools[].function.name, a role tool message is a tool result, and an
// assistant message holds its content, its thinking and its tool_calls.
func decodeOllama(body []byte) (requestSummary, error) {
	var req struct {
		Model string          `json:"model"`
		Think json.RawMessage `json:"think"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Thinking  string          `json:"thinking"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return requestSummary{}, fmt.Errorf("ollama request: %w", err)
	}
	s := requestSummary{Model: req.Model}
	// think is a level ("medium") or, for models with a switch, a boolean.
	var level string
	if json.Unmarshal(req.Think, &level) == nil {
		s.Effort = level
	} else if len(req.Think) > 0 && string(req.Think) != "null" {
		s.Effort = string(req.Think)
	}
	for _, t := range req.Tools {
		s.Tools = append(s.Tools, t.Function.Name)
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			s.UserTexts = append(s.UserTexts, wireContent(m.Content))
			s.Order = append(s.Order, "user")
		case "tool":
			s.Results = append(s.Results, toolResult{CallID: m.ToolCallID, Text: wireContent(m.Content)})
			s.Order = append(s.Order, "tool")
		case "assistant":
			a := assistantSummary{Text: wireContent(m.Content), Thinking: m.Thinking}
			if m.Thinking != "" {
				a.RawThinking = rawJSON(m.Thinking)
			}
			for _, c := range m.ToolCalls {
				a.ToolCalls = append(a.ToolCalls, toolCall{ID: c.ID, Name: c.Function.Name, Args: c.Function.Arguments})
			}
			s.Assistants = append(s.Assistants, a)
			s.Order = append(s.Order, "assistant")
		}
	}
	return s, nil
}
