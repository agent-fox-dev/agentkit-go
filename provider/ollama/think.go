package ollama

import (
	"strings"

	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// thinkFor is the think value for a requested effort, or nil to send none. The
// wire value follows the model's Efforts map as the Anthropic provider reads it:
// an empty effort, or a model that takes no thinking, sends none; with no map the
// effort is sent by its own name; with a map, a level that is absent or nil sends
// none, and a listed level sends its value, trimmed and lower-cased, as a string
// level. No effort is moved to another level.
func thinkFor(m core.Model, effort core.Effort) *api.ThinkValue {
	if effort == "" || m.ThinkingKind == core.ThinkingKindNone {
		return nil
	}
	level := string(effort)
	if m.Efforts != nil {
		v := m.Efforts[effort]
		if v == nil {
			return nil
		}
		level = strings.ToLower(strings.TrimSpace(*v))
	}
	if level == "" {
		return nil
	}
	return &api.ThinkValue{Value: level}
}
