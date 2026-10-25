package agentkit

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
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
		a, aerr := New(Config{Client: faux.New(), Model: testModel, Toolset: c.ts})
		if r != nil || err == nil || err.Error() != c.want || a != nil || aerr == nil || aerr.Error() != c.want {
			t.Errorf("%s: NewRunner = %v, %v; New = %v; want %q from both", c.name, r, err, aerr, c.want)
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

func TestCall_RunsOneCallAsABatchOfOne(t *testing.T) {
	var guards guardLog
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{echo("echo", nil)}, Guard: guards.guard, Events: events.add})
	for _, in := range []string{`{"v":"x"}`, "", "  \n"} {
		if res := r.Call(context.Background(), "echo", json.RawMessage(in)); !res.OK {
			t.Errorf("Call(%q) = %+v", in, res)
		}
	}
	seen := guards.calls()
	id := regexp.MustCompile(`^call_[0-9a-f]{16}$`)
	g := seen[0]
	if !id.MatchString(g.ToolUseID) || g.ToolUseID == seen[1].ToolUseID || g.ToolName != "echo" || g.Tool.Name != "echo" ||
		g.Arguments["v"] != "x" || string(g.RawInput) != `{"v":"x"}` || len(g.Batch) != 1 || g.Batch[0].ID != g.ToolUseID ||
		g.Index != 0 || g.Assistant != nil || g.TurnCount != 0 || g.ParentToolUseID != "" {
		t.Errorf("the guard saw %+v", g)
	}
	if string(seen[1].RawInput) != "{}" || string(seen[2].RawInput) != "{}" {
		t.Errorf("an empty input reached the guard as %q and %q, want {}", seen[1].RawInput, seen[2].RawInput)
	}
	starts, ends, results := executions(events.all())
	if starts[g.ToolUseID] != 1 || ends[g.ToolUseID] != 1 || results[g.ToolUseID] != 1 || len(events.all()) != 9 {
		t.Errorf("events = %v, want start, end and result for each call", events.all())
	}
}

func TestCall_AnswersEveryOutcomeWithACode(t *testing.T) {
	var guards guardLog
	guards.decide = func(in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{Block: in.ToolName == "blocked", Reason: "no way"}
	}
	r := mustRunner(t, Toolset{Tools: []core.Tool{echo("echo", nil), echo("blocked", nil)}, Guard: guards.guard})
	done, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []struct {
		ctx             context.Context
		name, in        string
		code, detail    string
		guardSawTheCall bool
	}{
		{context.Background(), "missing", `{}`, "unknown_tool", `no tool named "missing" is available in this run; available tools: blocked, echo`, false},
		{context.Background(), "echo", `not json`, "invalid_arguments", "", false},
		{context.Background(), "echo", `["a"]`, "invalid_arguments", "", false},
		{context.Background(), "echo", `{"v":{"not":"a string"}}`, "invalid_arguments", "", false},
		{context.Background(), "blocked", `{}`, core.BlockErrorCode, "no way", true},
		{done, "echo", `{}`, "aborted", "Operation aborted", false},
	} {
		before := len(guards.calls())
		res := r.Call(c.ctx, c.name, json.RawMessage(c.in))
		if res.OK || res.Error != c.code || !strings.HasPrefix(res.Detail, c.detail) || (len(guards.calls()) > before) != c.guardSawTheCall {
			t.Errorf("Call(%s, %s) = %+v; want %s %q", c.name, c.in, res, c.code, c.detail)
		}
	}
}

func TestBatch_UsesTheCallsIDsVerbatimAndRunsNothingForNoCalls(t *testing.T) {
	var guards guardLog
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{echo("echo", nil)}, Guard: guards.guard, Events: events.add})
	batch := []core.ToolUseBlock{use(t, "a", "echo", `{}`), use(t, "", "echo", `{}`)}
	if res := r.Batch(context.Background(), batch...); len(res) != 2 || !res[0].OK || !res[1].OK {
		t.Fatalf("Batch = %+v", res)
	}
	for i, g := range guards.calls() {
		if g.ToolUseID != batch[i].ID || g.Index != i || !reflect.DeepEqual(g.Batch, batch) {
			t.Errorf("the guard saw call %d as %+v", i, g)
		}
	}
	if starts, _, _ := executions(events.all()); starts["a"] != 1 || starts[""] != 1 {
		t.Errorf("start events by id %v, want the ids as given", starts)
	}
	n := len(events.all())
	if res := r.Batch(context.Background()); len(res) != 0 || len(events.all()) != n {
		t.Errorf("zero calls gave %v and %d events", res, len(events.all())-n)
	}
}

// TestRunner_EventsSink checks what a sink may be: nil, panicking, re-entrant
// or an EventStream's Push, and that none of them changes a result.
func TestRunner_EventsSink(t *testing.T) {
	var r *Runner
	var reentered int
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		_, _ = core.CallNested(ctx, nested("first")...)
		return core.OKResult(nil)
	}, echo("first", nil))
	stream := core.NewEventStream(core.StreamOptions{})
	var mu sync.Mutex
	sinks := map[string]func(core.Event){
		"nil":       nil,
		"panicking": func(core.Event) { panic("sink broke") },
		"re-entrant": func(e core.Event) {
			if s, ok := e.(core.ToolExecutionStartEvent); ok && s.Name == "first" {
				mu.Lock()
				reentered++
				mu.Unlock()
				r.Call(context.Background(), "second", nil)
			}
		},
		"EventStream.Push": stream.Push,
	}
	var want []core.ToolResult
	for _, name := range []string{"nil", "panicking", "re-entrant", "EventStream.Push"} {
		r = mustRunner(t, Toolset{Tools: []core.Tool{echo("first", nil), echo("second", nil), w}, Events: sinks[name]})
		got := r.Batch(context.Background(), use(t, "c1", "first", `{}`), use(t, "c2", "wrap", `{}`), use(t, "c3", "missing", `{}`))
		if want == nil {
			want = got
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s sink: Batch = %+v, want %+v", name, got, want)
		}
	}
	stream.End(core.StreamResult{})
	var evs []core.Event
	for e := range stream.Events() {
		evs = append(evs, e)
	}
	if starts, ends, results := executions(evs); len(starts) != 4 || len(ends) != 4 || len(results) != 3 || reentered != 2 {
		t.Errorf("the stream saw %d/%d/%d calls start/end/result, re-entered %d; want 4/4/3 and 2", len(starts), len(ends), len(results), reentered)
	}
}

func TestRunner_IsSafeForConcurrentUse(t *testing.T) {
	w := wrapper("wrap", func(ctx context.Context) core.ToolResult {
		res, err := core.CallNested(ctx, nested("child", "child")...)
		return core.ToolResult{OK: err == nil && len(res) == 2}
	}, child("child"))
	var events eventLog
	r := mustRunner(t, Toolset{Tools: []core.Tool{echo("echo", nil), w}, Guard: allow, After: noopAfter, Events: events.add})
	var wg sync.WaitGroup
	for g := range 16 {
		wg.Go(func() {
			for i := range 10 {
				res := r.Batch(context.Background(), use(t, fmt.Sprint(g, i), "echo", `{}`), use(t, "w", "wrap", `{}`))
				if !res[0].OK || !res[1].OK || !r.Call(context.Background(), "echo", nil).OK {
					t.Errorf("goroutine %d: %+v", g, res)
				}
			}
		})
	}
	wg.Wait()
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

func TestRunner_RunsCodeModeAndTheToolsItBinds(t *testing.T) {
	ws, err := workspace.Open(testutil.Tree(t, map[string]string{"a.txt": "alpha content\n"}), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cm, _, err := codemode.New(all, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var events eventLog
	r := mustRunner(t, Toolset{Tools: append(all, cm), Guard: allow, Events: events.add})
	script := testutil.MustJSON(t, map[string]string{"script": "res = read_file(path=\"a.txt\")\nprint(res[\"content\"].strip())\n"})
	if res := r.Call(context.Background(), cm.Name, json.RawMessage(script)); !res.OK || !strings.Contains(res.LLMText(), "alpha content") {
		t.Fatalf("Call = %+v", res)
	}
	var parent string
	var nestedReads int
	for _, e := range events.all() {
		if s, ok := e.(core.ToolExecutionStartEvent); ok && s.Name == cm.Name {
			parent = s.ToolUseID
		} else if ok && s.Name == "read_file" && s.ParentToolUseID == parent && parent != "" {
			nestedReads++
		}
	}
	if nestedReads != 1 {
		t.Errorf("%d nested read_file calls under %q, want 1", nestedReads, parent)
	}
}
