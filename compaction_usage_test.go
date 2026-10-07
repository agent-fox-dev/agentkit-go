package agentkit

import (
	"context"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/stop"
)

// Issue #74 §1: a summarization request is spend like any other. Usage the
// context transform reports through core.ReportUsage reaches Agent.Usage,
// and so the stop policies that read it.
func TestContextTransformUsageCountsAgainstTheAgent(t *testing.T) {
	var summary core.Usage
	summary.SetField(core.UsageInputTokens, 600_000)
	summary.SetCost(9.5)
	transform := func(ctx context.Context, msgs core.Messages) core.Messages {
		core.ReportUsage(ctx, summary)
		return msgs
	}

	a := newTestAgent(t, &scripted{}, func(c *core.AgentConfig) { c.TransformContext = transform })
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := a.Usage(); got.CostUSD < 9.5 || got.InputTokens < 600_000 {
		t.Fatalf("Agent.Usage() = %+v; the summarization's $9.50 and 600K input tokens are missing", got)
	}

	b := newTestAgent(t, &scripted{}, func(c *core.AgentConfig) {
		c.TransformContext = transform
		c.StopPolicy = stop.OverBudget(5)
	})
	res, err := b.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != core.RunStopBudgetExceeded {
		t.Fatalf("stop reason %q: OverBudget($5) must see a $9.50 summarization", res.StopReason)
	}
}

// Outside an agent, reporting usage is a no-op, not a panic.
func TestReportUsageWithoutAReporterIsANoOp(t *testing.T) {
	core.ReportUsage(context.Background(), core.Usage{CostUSD: 1})
}

// Issue #75 §1: usage a middleware reports for an attempt it discarded (a
// retried 5xx that was billed) reaches Agent.Usage: the loop installs the
// usage reporter around the model call as well as around the transform.
func TestMiddlewareUsageCountsAgainstTheAgent(t *testing.T) {
	var discarded core.Usage
	discarded.SetCost(0.75)
	a := newTestAgent(t, &scripted{}, func(c *core.AgentConfig) {
		c.Middleware = []core.Middleware{func(next core.Handler) core.Handler {
			return func(ctx context.Context, req core.Request) *core.EventStream {
				core.ReportUsage(ctx, discarded)
				return next(ctx, req)
			}
		}}
	})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := a.Usage().CostUSD; got < 0.75 {
		t.Fatalf("Agent.Usage().CostUSD = %v; the discarded attempt's $0.75 is missing", got)
	}
}
