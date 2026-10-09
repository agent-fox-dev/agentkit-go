package codemode_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-08-15: positional arguments are refused.
func TestBindingRejectsPositional_TS08_15(t *testing.T) {
	fc := &fakeCaller{}
	res := run(t, context.Background(), codemode.Options{}, fc, `fetch("http://example.com")`, leaf("fetch"))
	if res.OK || !strings.Contains(res.Detail, "fetch only accepts keyword arguments") || len(fc.calls) != 0 {
		t.Fatalf("result = %+v, calls %d", res, len(fc.calls))
	}
}

// TS-08-16: keyword arguments reach CallNested as JSON, and the result's Data
// comes back as Starlark values.
func TestBindingCallsNestedAndConvertsData_TS08_16(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"search": func(b core.ToolUseBlock) (core.ToolResult, error) {
			return core.OKResult(map[string]any{"count": 42, "hits": []string{"a", "b"},
				"meta": map[string]any{"ratio": 0.5, "ok": true, "none": nil}}), nil
		},
	}}
	res := run(t, context.Background(), codemode.Options{}, fc, `
def main():
    r = search(query="go", limit=3, tags=["x", "y"], opts={"deep": True}, ratio=1.5, missing=None)
    return [r["count"], r["hits"], r["meta"]["ratio"], r["meta"]["ok"], r["meta"]["none"]]
`, leaf("search"))
	if got := rv(t, res); got != `[42,["a","b"],0.5,true,null]` {
		t.Fatalf("return value = %s", got)
	}
	if len(fc.calls) != 1 || fc.calls[0].Name != "search" || fc.calls[0].ID == "" {
		t.Fatalf("calls = %+v", fc.calls)
	}
	var args map[string]any
	if err := json.Unmarshal(fc.calls[0].Input, &args); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(args)
	if string(b) != `{"limit":3,"missing":null,"opts":{"deep":true},"query":"go","ratio":1.5,"tags":["x","y"]}` {
		t.Fatalf("arguments = %s", b)
	}
}

// TS-08-17: a success with no Data is {"text": Text}.
func TestBindingNilDataIsText_TS08_17(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"ping": func(core.ToolUseBlock) (core.ToolResult, error) { return core.ToolResult{OK: true, Text: "done"}, nil },
	}}
	res := run(t, context.Background(), codemode.Options{}, fc, "def main():\n    return ping()[\"text\"]\n", leaf("ping"))
	if got := rv(t, res); got != `"done"` {
		t.Fatalf("return value = %s", got)
	}
}

// TS-08-18: ErrTerminated from CallNested stops the script and the code-mode
// result carries Terminate.
func TestBindingCallNestedTerminated_TS08_18(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"finish": func(core.ToolUseBlock) (core.ToolResult, error) { return core.ToolResult{}, core.ErrTerminated },
		"after":  func(core.ToolUseBlock) (core.ToolResult, error) { return core.OKResult(nil), nil },
	}}
	res := run(t, context.Background(), codemode.Options{}, fc, "finish()\nafter()\nprint('unreachable')\n", leaf("finish"), leaf("after"))
	if !res.Terminate || res.OK || strings.Contains(res.Text, "unreachable") || len(fc.calls) != 1 {
		t.Fatalf("result = %+v, calls %d", res, len(fc.calls))
	}
}

// TS-08-19: a call cancelled under the script aborts it.
func TestBindingCallNestedAborted_TS08_19(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"slow_op": func(core.ToolUseBlock) (core.ToolResult, error) { cancel(); return core.ToolResult{}, context.Canceled },
	}}
	res := run(t, ctx, codemode.Options{}, fc, "slow_op()\nslow_op()\n", leaf("slow_op"))
	if res.OK || res.Error != "aborted" || len(fc.calls) != 1 {
		t.Fatalf("result = %+v, calls %d", res, len(fc.calls))
	}
	// The real dispatcher reports cancellation as aborted results, not an
	// error; the script is aborted all the same.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	fc2 := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"slow_op": func(core.ToolUseBlock) (core.ToolResult, error) {
			cancel2()
			return core.ErrResult("aborted", "Operation aborted"), nil
		},
	}}
	if res := run(t, ctx2, codemode.Options{}, fc2, "slow_op()\nslow_op()\n", leaf("slow_op")); res.OK || res.Error != "aborted" || len(fc2.calls) != 1 {
		t.Fatalf("result = %+v, calls %d", res, len(fc2.calls))
	}
}

// A bound tool outside an agent run has no dispatcher, and the script says
// so instead of running the tool around the agent's interceptors.
func TestBindingWithoutDispatcherFails(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{}, nil, "lookup()\n", leaf("lookup"))
	if res.OK || !strings.Contains(res.Detail, core.ErrNoNestedCaller.Error()) {
		t.Fatalf("result = %+v", res)
	}
}

// Names a script cannot call — not an identifier, a keyword, or a name the
// runtime already uses — are refused at construction.
func TestNewRefusesUnbindableNames(t *testing.T) {
	for _, name := range []string{"my-tool", "1st", "for", "print", "parallel", "is_error", "main", "result"} {
		if _, _, err := codemode.New([]core.Tool{leaf(name)}, codemode.Options{}); err == nil ||
			!strings.Contains(err.Error(), "codemode: tool name") {
			t.Fatalf("%q: err = %v", name, err)
		}
	}
}
