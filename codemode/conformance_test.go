package codemode_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

// A value that contains itself is refused, as a return value and as a tool
// argument, instead of recursing until the process dies.
func TestCyclicValuesAreRefused(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"ping": okTool}}
	for _, script := range []string{
		"a = []\na.append(a)\nresult = a\n",
		"d = {}\nd['self'] = d\nresult = d\n",
		"a = []\na.append(a)\nping(v=a)\n",
	} {
		res := run(t, context.Background(), codemode.Options{}, fc, script, leaf("ping"))
		if res.OK || !strings.Contains(res.Detail, "contains itself") {
			t.Fatalf("%q: result = %+v", script, res)
		}
	}
	if len(fc.calls) != 0 {
		t.Fatalf("%d calls made with a cyclic argument", len(fc.calls))
	}
}

// Calls that ran before a cancellation are in the ledger, even though the
// dispatcher reports the cancellation as results rather than an error.
func TestLedgerKeepsCallsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"ping": okTool,
		"stop": func(core.ToolUseBlock) (core.ToolResult, error) {
			cancel()
			return core.ErrResult("aborted", "Operation aborted"), nil
		},
	}}
	res := run(t, ctx, codemode.Options{}, fc, "ping(i=1)\nparallel([call(ping, i=2), call(stop)])\n", leaf("ping"), leaf("stop"))
	if res.OK || res.Error != "aborted" {
		t.Fatalf("result = %+v", res)
	}
	if calls := res.Data["calls_completed"].([]any); len(calls) != 3 {
		t.Fatalf("calls = %+v, want all three dispatched calls", calls)
	}
}

// A code-mode tool behind a wrapper is refused even when a tool of the same
// name is also bound directly.
func TestNestingCheckIsNotFooledByNames(t *testing.T) {
	inner, _, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{Name: "read_file"})
	if err != nil {
		t.Fatal(err)
	}
	wrapper := leaf("wrapper")
	wrapper.ReachableTools = []core.Tool{inner}
	_, _, err = codemode.New([]core.Tool{leaf("read_file"), wrapper}, codemode.Options{})
	if err == nil || !strings.Contains(err.Error(), "codemode: cannot bind code_mode tool inside code_mode") {
		t.Fatalf("err = %v", err)
	}
}

// A parallel call over the remaining budget makes none of its calls, however
// many batches it would take.
func TestParallelOverBudgetMakesNoCalls(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"ping": okTool}}
	res := run(t, context.Background(), codemode.Options{MaxCalls: 4, MaxConcurrentCalls: 2}, fc,
		"parallel([call(ping), call(ping), call(ping), call(ping), call(ping)])\n", leaf("ping"))
	if res.OK || res.Error != "call_limit_exceeded" || len(fc.calls) != 0 {
		t.Fatalf("result = %+v, %d calls", res, len(fc.calls))
	}
}

// A tool description cannot end the docstring early.
func TestSignatureEscapesDocstringQuotes(t *testing.T) {
	tl := leaf("q")
	tl.Description = `says """hi"""`
	if sig := codemode.RenderSignature(tl); strings.Count(sig, `"""`) != 2 {
		t.Fatalf("signature = %s", sig)
	}
}
