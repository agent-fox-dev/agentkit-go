package ollama

import (
	"github.com/ollama/ollama/api"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// nettedUsage is the usage of a turn that ended in the done chunk r. The input
// is the prompt count less the cached count (Ollama's prompt count includes the
// cached tokens), floored at 0; the cache read is set only when the cached count
// is positive; the output is the eval count. Ollama reports no reasoning count,
// so none is set.
//
// Ollama runs on the embedder's hardware, so a run on it costs nothing: whenever
// the chunk reported a count the cost is set to 0 with its presence bit, which
// keeps the agent from pricing the turn again and the run from being flagged
// unpriced, for a model the catalog lists and one it does not. The SDK's prompt
// and eval counts are plain integers, so a chunk with neither (and no cached
// count) is read as one that reported nothing, and gets no token fields and no
// cost.
//
// The request is counted either way.
func nettedUsage(r api.ChatResponse) core.Usage {
	u := core.Usage{Requests: 1}
	prompt, eval := int64(max(r.PromptEvalCount, 0)), int64(max(r.EvalCount, 0))
	var cached int64
	if r.PromptEvalCachedCount != nil {
		cached = int64(max(*r.PromptEvalCachedCount, 0))
	}
	if prompt == 0 && eval == 0 && r.PromptEvalCachedCount == nil {
		return u
	}
	u.SetField(core.UsageInputTokens, max(prompt-cached, 0))
	u.SetField(core.UsageOutputTokens, eval)
	if cached > 0 {
		u.SetField(core.UsageCacheReadTokens, cached)
	}
	u.SetCost(0)
	return u
}
