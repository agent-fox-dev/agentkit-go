package openrouter

import (
	"github.com/OpenRouterTeam/go-sdk/models/components"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

// nettedUsage converts the usage a response reported into core.Usage, whose
// input is net of the cache counters and whose reasoning tokens are a subset of
// the output. A presence bit is set for the fields the response reported.
//
// The SDK types the three required counts as plain integers, so a count of 0
// cannot be told from an absent one and is not marked reported; the cache,
// cache-write and reasoning counts, and the cost, are optional in the SDK and
// are marked exactly.
func nettedUsage(u *components.ChatUsage) core.Usage {
	var out core.Usage
	if u == nil {
		return out
	}
	prompt, completion := max(u.PromptTokens, 0), max(u.CompletionTokens, 0)

	var cached, written int64
	var haveCached, haveWritten bool
	if det, ok := u.PromptTokensDetails.GetOrZero(); ok {
		if det.CachedTokens != nil {
			cached, haveCached = max(*det.CachedTokens, 0), true
		}
		if det.CacheWriteTokens != nil {
			written, haveWritten = max(*det.CacheWriteTokens, 0), true
		}
	}
	if prompt != 0 || haveCached || haveWritten {
		out.SetField(core.UsageInputTokens, max(prompt-cached-written, 0))
	}
	if haveCached {
		out.SetField(core.UsageCacheReadTokens, cached)
	}
	if haveWritten {
		out.SetField(core.UsageCacheWriteTokens, written)
	}
	if completion != 0 {
		out.SetField(core.UsageOutputTokens, completion)
	}
	if det, ok := u.CompletionTokensDetails.GetOrZero(); ok {
		if r, ok := det.ReasoningTokens.Get(); ok && r != nil {
			out.SetField(core.UsageReasoningTokens, min(max(*r, 0), completion))
		}
	}
	if u.TotalTokens != 0 {
		out.SetField(core.UsageTotalTokens, u.TotalTokens)
	}
	if c, ok := u.Cost.Get(); ok && c != nil {
		out.SetCost(*c)
	}
	return out
}

// priced reports whether any of the model's prices is non-zero.
func priced(m *core.Model) bool {
	return m.InputCostPerMillion != 0 || m.OutputCostPerMillion != 0 ||
		m.CacheReadCostPerMillion != 0 || m.CacheWriteCostPerMillion != 0
}

// finalUsage is the usage of the turn: what the response reported, netted;
// the one request, whatever the number of attempts; the model that served it
// when that is not the one asked for; and the cost. The cost is the one the
// response reported, else the model's price applied to the usage, else left
// unset, so a caller can tell an unpriced response from a free one. A response
// that reported no usage is left unreported and gets no cost.
func (d *decodeState) finalUsage() core.Usage {
	u := nettedUsage(d.usage)
	u.Requests = 1
	billModel, billed := provider.BillingModel(&d.model, d.responseModel, nil)
	u.BilledModel = billed
	if !u.Has(core.UsageCostUSD) && u.Reported() && priced(billModel) {
		u.SetCost(provider.ComputeCost(billModel, u))
	}
	return u
}
