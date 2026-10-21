package openrouter

import (
	"strconv"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// bodyReasoning is the request's reasoning object: one of an effort name or a
// token budget.
type bodyReasoning struct {
	Effort    string `json:"effort,omitempty"`
	MaxTokens *int   `json:"max_tokens,omitempty"`
}

// reasoningFor returns the reasoning object for a requested effort, or nil to
// send none so the vendor's default applies. The wire value follows the model's
// Efforts map as the Anthropic provider reads it: with no map the effort is
// sent by its own name; with a map, a level that is absent or nil sends
// nothing, and a present level sends its value. A value that is a positive
// integer is a token budget, sent as max_tokens; any other value is an effort
// name. No effort is moved to another level.
func reasoningFor(m core.Model, effort core.Effort) *bodyReasoning {
	if effort == "" {
		return nil
	}
	wire := string(effort)
	if m.Efforts != nil {
		v := m.Efforts[effort]
		if v == nil {
			return nil
		}
		wire = strings.ToLower(strings.TrimSpace(*v))
	}
	if wire == "" {
		return nil
	}
	if n, err := strconv.Atoi(wire); err == nil && n > 0 {
		return &bodyReasoning{MaxTokens: &n}
	}
	return &bodyReasoning{Effort: wire}
}
