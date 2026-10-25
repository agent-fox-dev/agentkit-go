package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// call(tool, **kwargs) is an inert descriptor holding both: nothing runs until
// parallel does.
func TestCallMakesADescriptorWithoutCalling(t *testing.T) {
	fc := caller(nil)
	res := runScript(t, context.Background(), Options{}, fc, `
c = call(my_tool, key="val", n=2)
def main():
    return [type(c), c.tool == my_tool, c.kwargs["key"], c.kwargs["n"], str(call("my_tool", k="v")),
            dir(c), bool(c), hasattr(c, "other")]
`, leaf("my_tool"))
	if got := rv(t, res); got != `["call_descriptor",true,"val",2,"call(my_tool, **{\"k\": \"v\"})",["kwargs","tool"],true,false]` ||
		len(fc.calls) != 0 {
		t.Fatalf("return value = %s, %d calls made", got, len(fc.calls))
	}
	for _, script := range []string{"call(1)", "call(my_tool, 1)", "call(len)", "call('nope')", "x = {call(my_tool): 1}"} {
		if r := runScript(t, context.Background(), Options{}, fc, script, leaf("my_tool")); r.OK {
			t.Fatalf("%s succeeded", script)
		}
	}
}

// parallel takes descriptors, (tool, kwargs) pairs (the tool as a function or
// a name) and {"tool", "args"} dicts, and dispatches them in batches of at
// most MaxConcurrentCalls.
func TestParallelTakesEveryCallFormInBatches(t *testing.T) {
	fc := &fakeCaller{concurrent: true, fn: map[string]answer{"work": echoArgs}}
	res := runScript(t, context.Background(), Options{MaxConcurrentCalls: 2}, fc, `
def main():
    return parallel([call(work, i=1), (work, {"i": 2}), {"tool": work, "args": {"i": 3}}, ["work", {"i": 4}], call(work, i=5)])
`, leaf("work"))
	if got := rv(t, res); got != `[{"i":1},{"i":2},{"i":3},{"i":4},{"i":5}]` {
		t.Fatalf("return value = %s", got)
	}
	if fc.maxBatch != 2 || len(fc.calls) != 5 {
		t.Fatalf("largest batch %d, calls %d; want 2 and 5", fc.maxBatch, len(fc.calls))
	}
}

// Every entry is checked before any call runs.
func TestAMalformedParallelMakesNoCalls(t *testing.T) {
	for _, tc := range []struct{ script, detail string }{
		{`parallel()`, "parallel: got 0 arguments"},
		{`parallel(call(work, i=1))`, "parallel takes a list of calls, got call_descriptor"},
		{`parallel("work")`, "parallel takes a list of calls, got string"},
		{`parallel([call(work), (work,)])`, "call 1: want call(tool, ...)"},
		{`parallel([call(work), 1])`, "call 1: want call(tool, ...)"},
		{`parallel([call(work), ("nope", {})])`, `call 1: "nope" is not a bound tool`},
		{`parallel([call(work), (len, {})])`, "call 1: len is not a bound tool"},
		{`parallel([call(work), (1, {})])`, "call 1: want a bound tool, got int"},
		{`parallel([call(work), {"tool": work}])`, `a dict call needs "tool" and "args"`},
		{`parallel([call(work), (work, [1])])`, "the arguments must be a dict, got list"},
		{`parallel([call(work), (work, {1: 2})])`, "argument names must be strings, got int"},
		{`parallel([call(work), call(work, f=work)])`, "call 1: work: argument f"},
	} {
		fc := caller(map[string]answer{"work": echoArgs})
		res := runScript(t, context.Background(), Options{}, fc, tc.script, leaf("work"))
		if res.OK || !strings.Contains(res.Detail, tc.detail) || len(fc.calls) != 0 {
			t.Fatalf("%s: result = %+v, %d calls", tc.script, res, len(fc.calls))
		}
	}
}

// Results come back in call order whatever order the calls finish in: here
// each call waits for the next one to finish first, which only concurrent
// calls can do.
func TestParallelResultsKeepCallOrder(t *testing.T) {
	done := map[int]chan struct{}{1: make(chan struct{}), 2: make(chan struct{}), 3: make(chan struct{})}
	fc := &fakeCaller{concurrent: true, fn: map[string]answer{"step": func(b core.ToolUseBlock) (core.ToolResult, error) {
		var a struct{ ID int }
		_ = json.Unmarshal(b.Input, &a)
		defer close(done[a.ID])
		if next, ok := done[a.ID+1]; ok {
			select {
			case <-next:
			case <-time.After(5 * time.Second):
				return core.ErrResult("not_concurrent", "the next call never finished"), nil
			}
		}
		return core.OKResult(map[string]any{"id": a.ID}), nil
	}}}
	res := runScript(t, context.Background(), Options{}, fc,
		"result = parallel([call(step, id=1), call(step, id=2), call(step, id=3)])\n", leaf("step"))
	if got := rv(t, res); got != `[{"id":1},{"id":2},{"id":3}]` {
		t.Fatalf("return value = %s", got)
	}
}

// A terminate vote inside a batch stops the script and the later batches.
func TestATerminateVoteInParallelStopsLaterBatches(t *testing.T) {
	fc := caller(map[string]answer{
		"normal":    echoArgs,
		"terminate": func(core.ToolUseBlock) (core.ToolResult, error) { return core.ToolResult{}, core.ErrTerminated },
	})
	res := runScript(t, context.Background(), Options{MaxConcurrentCalls: 3}, fc, `
parallel([call(normal), call(terminate), call(normal)])
parallel([call(normal), call(normal)])
print("unreachable")
`, leaf("normal"), leaf("terminate"))
	if !res.Terminate || res.OK || strings.Contains(res.Text, "unreachable") || len(fc.calls) != 2 {
		t.Fatalf("result = %+v, %d calls", res, len(fc.calls))
	}
}

// A result whose Data Starlark cannot hold fails the script rather than
// handing it a partial value.
func TestAnUnconvertibleResultFailsTheScript(t *testing.T) {
	fc := caller(map[string]answer{"bad": func(core.ToolUseBlock) (core.ToolResult, error) {
		return core.OKResult(map[string]any{"f": func() {}}), nil
	}})
	for _, script := range []string{"bad()\n", "parallel([call(bad)])\n"} {
		if res := runScript(t, context.Background(), Options{}, fc, script, leaf("bad")); res.Error != "runtime_error" ||
			!strings.Contains(res.Detail, "unsupported type") {
			t.Fatalf("%q: result = %+v", script, res)
		}
	}
}
