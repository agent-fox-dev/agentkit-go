package codemode_test

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"go.starlark.net/starlark"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

func failing(code, detail string) func(core.ToolUseBlock) (core.ToolResult, error) {
	return func(core.ToolUseBlock) (core.ToolResult, error) { return core.ErrResult(code, detail), nil }
}

// TS-08-20: a failed call returns a ToolError; nothing is raised and the
// script carries on.
func TestToolErrorIsReturnedNotRaised_TS08_20(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"lookup": failing("not_found", "item missing")}}
	res := run(t, context.Background(), codemode.Options{}, fc, `
def main():
    err = lookup(id=1)
    after = "continued"
    return [type(err), after, err.ok, bool(err)]
`, leaf("lookup"))
	if got := rv(t, res); got != `["tool_error","continued",false,false]` {
		t.Fatalf("return value = %s", got)
	}
}

// TS-08-21: for any failed result, the ToolError carries its code, detail and
// data, with ok False and is_error True.
func TestToolErrorAttributes_TS08_21(t *testing.T) {
	r := rand.New(rand.NewSource(21))
	for i := range 50 {
		tr := core.ErrResult(fmt.Sprintf("code_%d", r.Intn(100)), fmt.Sprintf("detail %d é", i))
		if r.Intn(2) == 0 {
			tr.Data = map[string]any{"exit_code": r.Intn(5), "output": "partial"}
		}
		te := codemode.NewToolError(tr)
		if te.Type() != "tool_error" {
			t.Fatalf("type = %s", te.Type())
		}
		attr := func(name string) starlark.Value {
			v, err := te.Attr(name)
			if err != nil || v == nil {
				t.Fatalf("attr %s: %v, %v", name, v, err)
			}
			return v
		}
		if attr("ok") != starlark.False || attr("is_error") != starlark.True ||
			attr("error") != starlark.String(tr.Error) || attr("detail") != starlark.String(tr.Detail) {
			t.Fatalf("attributes of %+v wrong", tr)
		}
		data := attr("data")
		if tr.Data == nil && data != starlark.None {
			t.Fatalf("data = %v, want None", data)
		}
		if tr.Data != nil {
			d, ok := data.(*starlark.Dict)
			if !ok {
				t.Fatalf("data = %v", data)
			}
			if v, found, _ := d.Get(starlark.String("output")); !found || v != starlark.String("partial") {
				t.Fatalf("data.output = %v", v)
			}
		}
	}
}

// TS-08-22: a ToolError can be indexed like a dict.
func TestToolErrorIndexing_TS08_22(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"failing_tool": failing("failed", "detail msg")}}
	res := run(t, context.Background(), codemode.Options{}, fc, `
def main():
    e = failing_tool()
    return [e["error"], e["detail"], e["ok"], e["is_error"], e.get("error") if hasattr(e, "get") else e["error"]]
`, leaf("failing_tool"))
	if got := rv(t, res); got != `["failed","detail msg",false,true,"failed"]` {
		t.Fatalf("return value = %s", got)
	}
	bad := run(t, context.Background(), codemode.Options{}, fc, "e = failing_tool()\nx = e[\"nope\"]\n", leaf("failing_tool"))
	if bad.OK {
		t.Fatal("indexing an unknown key succeeded")
	}
}

// TS-08-23: is_error is True for a ToolError and False for anything else.
func TestToolErrorIsErrorHelper_TS08_23(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"failing_tool": failing("failed", "x"),
		"ok_tool": func(core.ToolUseBlock) (core.ToolResult, error) {
			return core.OKResult(map[string]any{"error": "x"}), nil
		},
	}}
	res := run(t, context.Background(), codemode.Options{}, fc, `
def main():
    return [is_error(failing_tool()), is_error({"error": "mock"}), is_error(None), is_error(123), is_error(ok_tool())]
`, leaf("failing_tool"), leaf("ok_tool"))
	if got := rv(t, res); got != `[true,false,false,false,false]` {
		t.Fatalf("return value = %s", got)
	}
}
