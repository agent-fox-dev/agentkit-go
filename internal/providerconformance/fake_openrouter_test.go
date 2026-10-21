package providerconformance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// openRouterVendor is OpenRouter's Chat Completions API: server-sent chunks at
// POST <base>/chat/completions, where the base is <server>/api/v1.
var openRouterVendor = vendor{
	name: "openrouter",
	path: "/api/v1/chat/completions",
	env: func(url string) map[string]string {
		return map[string]string{"OPENROUTER_API_KEY": "sk-or-conformance", "OPENROUTER_BASE_URL": url + "/api/v1"}
	},
	render: renderOpenRouter,
	decode: decodeOpenRouter,
}

// renderOpenRouter answers one request the way Chat Completions does: a chunk
// per delta (content, reasoning_details fragments, tool-call argument
// fragments), a chunk with the finish_reason, a usage chunk whose prompt_tokens
// include the cache reads and which carries the cost, and [DONE]. The tool-call
// stop is tool_calls, the normal one stop.
func renderOpenRouter(w http.ResponseWriter, model string, t logicalTurn) {
	if t.end == endStatus {
		writeJSONStatus(w, t.status, map[string]any{
			"error": map[string]any{"code": t.status, "message": t.message},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(choices []any, extra map[string]any) map[string]any {
		c := map[string]any{
			"id": "gen-conformance", "object": "chat.completion.chunk", "created": 1,
			"model": model, "choices": choices,
		}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	delta := func(d map[string]any, finish any) []any {
		return []any{map[string]any{"index": 0, "delta": d, "finish_reason": finish}}
	}

	calls := 0
blocks:
	for i, b := range t.blocks {
		cut := t.end == endCutOff && i == len(t.blocks)-1
		var chunks []map[string]any
		switch b.kind {
		case blockText:
			for _, part := range halves(b.text) {
				chunks = append(chunks, map[string]any{"role": "assistant", "content": part})
			}
		case blockThinking:
			for _, part := range halves(b.text) {
				chunks = append(chunks, map[string]any{"reasoning_details": []any{
					map[string]any{"type": "reasoning.text", "text": part, "format": "unknown", "index": 0},
				}})
			}
		case blockToolCall:
			for j, part := range halves(b.args) {
				fn := map[string]any{"arguments": part}
				call := map[string]any{"index": calls, "function": fn}
				if j == 0 {
					call["id"], call["type"], fn["name"] = b.id, "function", b.name
				}
				chunks = append(chunks, map[string]any{"tool_calls": []any{call}})
			}
			calls++
		}
		for j, d := range chunks {
			writeSSE(w, "", chunk(delta(d, nil), nil))
			if cut && j == 0 {
				break blocks
			}
		}
	}

	switch t.end {
	case endCutOff:
		return
	case endInStream:
		writeSSE(w, "", chunk(delta(map[string]any{"content": ""}, "error"), map[string]any{
			"error": map[string]any{"code": 502, "message": t.message},
		}))
		return
	}
	finish := "stop"
	if t.end == endToolStop {
		finish = "tool_calls"
	}
	u := t.usageOrDefault()
	usage := map[string]any{
		"prompt_tokens":         u.prompt,
		"completion_tokens":     u.output,
		"total_tokens":          u.prompt + u.output,
		"prompt_tokens_details": map[string]any{"cached_tokens": u.cacheRead},
	}
	if u.cost != 0 {
		usage["cost"] = u.cost
	}
	writeSSE(w, "", chunk(delta(map[string]any{}, finish), nil))
	writeSSE(w, "", chunk([]any{}, map[string]any{"usage": usage}))
	fmt.Fprint(w, "data: [DONE]\n\n")
	flush(w)
}

// decodeOpenRouter summarizes a Chat Completions request: the effort is
// reasoning.effort, the tools are tools[].function.name, the tool results are
// the role tool messages, and an assistant message holds its content, its
// tool_calls and the reasoning_details it replays.
func decodeOpenRouter(body []byte) (requestSummary, error) {
	var req struct {
		Model     string `json:"model"`
		Reasoning struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID       string          `json:"tool_call_id"`
			ReasoningDetails json.RawMessage `json:"reasoning_details"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return requestSummary{}, fmt.Errorf("openrouter request: %w", err)
	}
	s := requestSummary{Model: req.Model, Effort: req.Reasoning.Effort}
	for _, t := range req.Tools {
		s.Tools = append(s.Tools, t.Function.Name)
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			s.UserTexts = append(s.UserTexts, wireContent(m.Content))
		case "tool":
			s.Results = append(s.Results, toolResult{CallID: m.ToolCallID, Text: wireContent(m.Content)})
		case "assistant":
			a := assistantSummary{Text: wireContent(m.Content)}
			for _, c := range m.ToolCalls {
				a.ToolCalls = append(a.ToolCalls, toolCall{ID: c.ID, Name: c.Function.Name, Args: json.RawMessage(c.Function.Arguments)})
			}
			if len(m.ReasoningDetails) > 0 && string(m.ReasoningDetails) != "null" {
				a.RawThinking = m.ReasoningDetails
				var frags []struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(m.ReasoningDetails, &frags); err != nil {
					return requestSummary{}, fmt.Errorf("openrouter request: reasoning_details: %w", err)
				}
				var b strings.Builder
				for _, f := range frags {
					b.WriteString(f.Text)
				}
				a.Thinking = b.String()
			}
			s.Assistants = append(s.Assistants, a)
		}
	}
	return s, nil
}
