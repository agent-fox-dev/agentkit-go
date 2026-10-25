package codemode

import (
	"context"
	"strings"
	"testing"

	"go.starlark.net/starlark"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// A failed call returns a ToolError instead of raising, and the script
// carries on: a falsy value with ok False, is_error True, the code, the detail
// and the tool's data, as attributes or dict keys.
func TestAFailedCallIsAToolErrorValue(t *testing.T) {
	fc := caller(map[string]answer{"lookup": func(core.ToolUseBlock) (core.ToolResult, error) {
		r := core.ErrResult("not_found", "item missing")
		r.Data = map[string]any{"exit_code": 2}
		return r, nil
	}})
	res := runScript(t, context.Background(), Options{}, fc, `
err = lookup(id=1)
def main():
    after = "continued"
    return [type(err), after, err.ok, err.is_error, bool(err), err.error, err.detail, err.data["exit_code"],
            err["error"], err["detail"], err["ok"], err["is_error"], dir(err), str(err), hasattr(err, "other")]
`, leaf("lookup"))
	want := `["tool_error","continued",false,true,false,"not_found","item missing",2,"not_found","item missing",false,true,` +
		`["data","detail","error","is_error","ok"],"tool_error(error=\"not_found\", detail=\"item missing\")",false]`
	if got := rv(t, res); got != want {
		t.Fatalf("return value = %s\nwant %s", got, want)
	}
	for _, script := range []string{"e = lookup()\nx = e[\"nope\"]\n", "e = lookup()\nx = e[1]\n", "x = {lookup(): 1}\n"} {
		if r := runScript(t, context.Background(), Options{}, fc, script, leaf("lookup")); r.OK {
			t.Fatalf("%q succeeded", script)
		}
	}
}

// A ToolError's data is None without Data and the Starlark form of Data with
// it; returned from a script, it is a JSON object.
func TestAToolErrorCarriesTheResultsData(t *testing.T) {
	if d, _ := NewToolError(core.ErrResult("c", "d")).Attr("data"); d != starlark.None {
		t.Fatalf("data = %v, want None", d)
	}
	r := core.ErrResult("c", "d")
	r.Data = map[string]any{"output": "partial"}
	if d, _ := NewToolError(r).Attr("data"); d.String() != `{"output": "partial"}` {
		t.Fatalf("data = %v", d)
	}
	fc := caller(map[string]answer{"bad": failing("nope", "gone")})
	if got := rv(t, runScript(t, context.Background(), Options{}, fc, "result = bad()\n", leaf("bad"))); got !=
		`{"data":null,"detail":"gone","error":"nope","is_error":true,"ok":false}` {
		t.Fatalf("return value = %s", got)
	}
}

// is_error is True for a ToolError and False for anything else, a successful
// result that happens to have an error key included.
func TestIsErrorRecognisesOnlyAToolError(t *testing.T) {
	fc := caller(map[string]answer{"failing_tool": failing("failed", "x"), "ok_tool": func(core.ToolUseBlock) (core.ToolResult, error) {
		return core.OKResult(map[string]any{"error": "x"}), nil
	}})
	res := runScript(t, context.Background(), Options{}, fc,
		`result = [is_error(failing_tool()), is_error({"error": "mock"}), is_error(None), is_error(123), is_error(ok_tool())]`,
		leaf("failing_tool"), leaf("ok_tool"))
	if got := rv(t, res); got != `[true,false,false,false,false]` {
		t.Fatalf("return value = %s", got)
	}
	if res := runScript(t, context.Background(), Options{}, fc, "is_error()\n"); res.Error != "runtime_error" ||
		!strings.Contains(res.Detail, "is_error: got 0 arguments") {
		t.Fatalf("is_error(): %+v", res)
	}
}
