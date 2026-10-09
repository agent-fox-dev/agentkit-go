package provider

import "github.com/agent-fox-dev/agentkit-go/core"

// ToolResultText renders a tool result as the single string a wire with no
// is_error flag carries — the OpenAI Chat Completions and Responses wires.
// An error result is marked in the text, because the model is the only
// consumer that can act on it and the wire gives it nothing else to tell a
// failed tool from one that printed the same words. It is the rule the
// Ollama adapter applies for the same reason.
func ToolResultText(v core.ToolResultMessage) string {
	text := v.Content.Text()
	if v.IsError && text != "" {
		return "Error: " + text
	}
	return text
}
