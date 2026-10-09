package codemode_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

// echoArgs answers with the call's arguments.
func echoArgs(b core.ToolUseBlock) (core.ToolResult, error) {
	var m map[string]any
	_ = json.Unmarshal(b.Input, &m)
	return core.OKResult(m), nil
}

// TS-08-24: call(tool, **kwargs) is a descriptor holding both.
func TestCallDescriptor_TS08_24(t *testing.T) {
	fc := &fakeCaller{}
	res := run(t, context.Background(), codemode.Options{}, fc, `
def main():
    c = call(my_tool, key="val", n=2)
    return [type(c), c.tool == my_tool, c.kwargs["key"], c.kwargs["n"]]
`, leaf("my_tool"))
	if got := rv(t, res); got != `["call_descriptor",true,"val",2]` || len(fc.calls) != 0 {
		t.Fatalf("return value = %s, %d calls made", got, len(fc.calls))
	}
	for _, script := range []string{"call(1)", "call(my_tool, 1)", "call(len)"} {
		if r := run(t, context.Background(), codemode.Options{}, fc, script, leaf("my_tool")); r.OK {
			t.Fatalf("%s succeeded", script)
		}
	}
}

// TS-08-25: parallel takes descriptors, (tool, kwargs) pairs (by function or
// name) and {"tool", "args"} dicts, and dispatches them in batches of at most
// MaxConcurrentCalls.
func TestParallelBatches_TS08_25(t *testing.T) {
	fc := &fakeCaller{concurrent: true, fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"work": echoArgs}}
	res := run(t, context.Background(), codemode.Options{MaxConcurrentCalls: 2}, fc, `
def main():
    return parallel([call(work, i=1), (work, {"i": 2}), {"tool": work, "args": {"i": 3}}, ["work", {"i": 4}], call(work, i=5)])
`, leaf("work"))
	if got := rv(t, res); got != `[{"i":1},{"i":2},{"i":3},{"i":4},{"i":5}]` {
		t.Fatalf("return value = %s", got)
	}
	if fc.maxBatch != 2 || len(fc.calls) != 5 {
		t.Fatalf("largest batch %d, calls %d; want 2 and 5", fc.maxBatch, len(fc.calls))
	}
	// A malformed entry or an unbound tool fails before any call is made.
	for _, script := range []string{
		`parallel([call(work, i=1), (work,)])`,
		`parallel([call(work, i=1), ("nope", {})])`,
		`parallel([call(work, i=1), {"tool": work}])`,
		`parallel(call(work, i=1))`,
	} {
		fc2 := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"work": echoArgs}}
		if r := run(t, context.Background(), codemode.Options{}, fc2, script, leaf("work")); r.OK || len(fc2.calls) != 0 {
			t.Fatalf("%s: OK %v with %d calls", script, r.OK, len(fc2.calls))
		}
	}
}

// TS-08-26: results come back in call order, whatever order they finish in,
// and the calls run concurrently.
func TestParallelPreservesOrder_TS08_26(t *testing.T) {
	var active, peak atomic.Int32
	fc := &fakeCaller{concurrent: true, fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"delayed": func(b core.ToolUseBlock) (core.ToolResult, error) {
			var a struct{ Delay, ID int }
			_ = json.Unmarshal(b.Input, &a)
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(time.Duration(a.Delay) * time.Millisecond)
			active.Add(-1)
			return core.OKResult(map[string]any{"id": a.ID}), nil
		},
	}}
	res := run(t, context.Background(), codemode.Options{}, fc, `
def main():
    return parallel([call(delayed, delay=50, id=1), call(delayed, delay=10, id=2), call(delayed, delay=30, id=3)])
`, leaf("delayed"))
	if got := rv(t, res); got != `[{"id":1},{"id":2},{"id":3}]` {
		t.Fatalf("return value = %s", got)
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrency %d, want the calls to overlap", peak.Load())
	}
}

// TS-08-27: a terminate vote inside parallel stops the script and the later
// batches, and the result carries Terminate.
func TestParallelTerminated_TS08_27(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"normal_tool":      echoArgs,
		"terminating_tool": func(core.ToolUseBlock) (core.ToolResult, error) { return core.ToolResult{}, core.ErrTerminated },
	}}
	res := run(t, context.Background(), codemode.Options{MaxConcurrentCalls: 3}, fc, `
parallel([call(normal_tool), call(terminating_tool), call(normal_tool)])
parallel([call(normal_tool), call(normal_tool)])
print("unreachable")
`, leaf("normal_tool"), leaf("terminating_tool"))
	if !res.Terminate || res.OK || strings.Contains(res.Text, "unreachable") || len(fc.calls) > 3 {
		t.Fatalf("result = %+v, %d calls", res, len(fc.calls))
	}
}
