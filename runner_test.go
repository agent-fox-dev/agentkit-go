package agentkit

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/runner"
)

// cyclic is a tool that reaches itself through a second one, through shared
// slice storage: the only way tool values can form a cycle.
func cyclic() core.Tool {
	a := named("a", core.Tool{})
	b := named("b", a)
	a.ReachableTools[0] = b
	return a
}

func TestNewRunner_RefusesAToolsetItCannotRunAndNewAgrees(t *testing.T) {
	exec := func(context.Context, json.RawMessage) core.ToolResult { return core.OKResult(nil) }
	neither, both := core.Tool{Name: "t"}, core.Tool{Name: "u", Handler: noop, Execute: exec}
	self := named("self", core.Tool{})
	self.ReachableTools[0] = self
	finish := core.Tool{Name: "finish", Handler: noop, Terminating: true}
	for _, c := range []struct {
		name string
		ts   Toolset
		want string
	}{
		{"neither Handler nor Execute", Toolset{Tools: []core.Tool{neither, named("run_command")}}, `agentkit: tool "t" has neither Handler nor Execute`},
		{"both", Toolset{Tools: []core.Tool{both}}, `agentkit: tool "u" sets both Handler and Execute; exactly one`},
		{"one the policy adds", Toolset{Tools: []core.Tool{named("ok")}, Policy: core.ToolPolicy{CustomTools: []core.Tool{both}}}, `agentkit: tool "u" sets both Handler and Execute; exactly one`},
		{"one behind a wrapper", Toolset{Tools: []core.Tool{named("w", neither)}}, `agentkit: tool "t" has neither Handler nor Execute`},
		{"a cycle", Toolset{Tools: []core.Tool{cyclic()}}, "agentkit: reachable tools cycle detected: a -> b -> a"},
		{"a cycle the policy adds", Toolset{Tools: []core.Tool{named("ok")}, Policy: core.ToolPolicy{CustomTools: []core.Tool{cyclic()}}},
			"agentkit: reachable tools cycle detected: a -> b -> a"},
		{"a tool reaching itself", Toolset{Tools: []core.Tool{self}}, "agentkit: reachable tools cycle detected: self -> self"},
		{"a terminating tool behind a wrapper", Toolset{Tools: []core.Tool{named("outer", named("code_mode", finish))}},
			`agentkit: terminating tool "finish" cannot be reached through wrapper "code_mode"`},
	} {
		r, err := NewRunner(c.ts)
		rr, rerr := runner.New(c.ts)
		a, aerr := New(Config{Client: faux.New(), Model: testModel, Toolset: c.ts})
		if r != nil || err == nil || err.Error() != c.want || a != nil || aerr == nil || aerr.Error() != c.want ||
			rr != nil || rerr == nil || rerr.Error() != c.want {
			t.Errorf("%s: NewRunner = %v, %v; runner.New = %v; New = %v; want %q from all three", c.name, r, err, rerr, aerr, c.want)
		}
	}
	diamond := named("root", named("l", named("leaf")), named("r", named("leaf")))
	for _, ts := range []Toolset{{}, {Tools: []core.Tool{finish}}, {Tools: []core.Tool{diamond}}} {
		if _, err := NewRunner(ts); err != nil {
			t.Errorf("NewRunner(%v) = %v, want a runner", ts.Tools, err)
		}
	}
}

func TestNewRunner_KeepsThePolicysToolsInOrderAndCopiesThem(t *testing.T) {
	names := func(ts []core.Tool) (out []string) {
		for _, x := range ts {
			out = append(out, x.Name)
		}
		return out
	}
	alpha, beta, gamma := named("alpha"), named("beta"), named("gamma", named("child"))
	given := []core.Tool{alpha, beta}
	plain := mustRunner(t, Toolset{Tools: given})
	given[0] = named("changed")
	ts := Toolset{Tools: []core.Tool{alpha, beta, gamma}, Policy: core.ToolPolicy{Tools: []core.Tool{gamma, alpha}}}
	r, a := mustRunner(t, ts), mustNew(t, Config{Client: faux.New(), Toolset: ts})
	first := r.Tools()
	first[0] = named("overwritten")
	if got := names(plain.Tools()); !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Errorf("Tools() = %v after the caller changed its slice, want alpha, beta", got)
	}
	if got := names(r.Tools()); !slices.Equal(got, []string{"gamma", "alpha"}) {
		t.Errorf("Tools() = %v, want the policy's gamma, alpha whatever the caller did with a copy", got)
	}
	for _, got := range [][]core.Tool{r.Reachable(), a.ReachableTools(), a.Runner().Reachable()} {
		if !slices.Equal(names(got), []string{"gamma", "child", "alpha"}) {
			t.Errorf("reachable = %v, want gamma, child, alpha", names(got))
		}
	}
}

// The root package's Runner and Toolset are the runner package's: a value of
// one is a value of the other, whichever constructor made it.
func TestRunnerAndToolsetAreTheRunnerPackagesTypes(t *testing.T) {
	ts := runner.Toolset{Tools: []core.Tool{echo("echo", nil), named("other")}}
	var fromRunner *Runner
	fromRunner, err := runner.New(ts)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	fromRoot := mustRunner(t, ts)
	a := mustNew(t, Config{Client: faux.New(), Toolset: ts})
	var ofAgent *runner.Runner = a.Runner()
	if ofAgent == nil || ofAgent != a.Runner() {
		t.Fatalf("Agent.Runner() = %p then %p, want the same runner on every call", ofAgent, a.Runner())
	}
	want := fromRunner.Call(context.Background(), "echo", nil)
	if !want.OK {
		t.Fatalf("a runner from runner.New does not run its tool: %+v", want)
	}
	for name, r := range map[string]*runner.Runner{"NewRunner": fromRoot, "Agent.Runner": ofAgent} {
		got := r.Call(context.Background(), "echo", nil)
		if got.OK != want.OK || got.Text != want.Text || len(r.Tools()) != 2 || r.Tools()[0].Name != "echo" {
			t.Errorf("%s: Call = %+v with %d tools, want what runner.New's runner answers: %+v", name, got, len(r.Tools()), want)
		}
	}
}

func TestRunner_PassesTheCallersUsageReporterAndLeavesTheAgentsUsage(t *testing.T) {
	spend := tool("spend", func(ctx context.Context, _ json.RawMessage) core.ToolResult {
		core.ReportUsage(ctx, core.Usage{InputTokens: 7})
		return core.OKResult(nil)
	})
	a := mustNew(t, Config{Client: faux.New(), Toolset: Toolset{Tools: []core.Tool{spend}}})
	var got []core.Usage
	ctx := core.WithUsageReporter(context.Background(), func(u core.Usage) { got = append(got, u) })
	a.Runner().Call(ctx, "spend", nil)
	a.Runner().Call(context.Background(), "spend", nil)
	if len(got) != 1 || got[0].InputTokens != 7 || a.Usage().InputTokens != 0 {
		t.Errorf("the caller's reporter got %+v, the agent %+v", got, a.Usage())
	}
}
