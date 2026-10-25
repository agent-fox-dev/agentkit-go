package codemode

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// Keyword arguments reach the dispatcher as one JSON object, and the result's
// Data comes back as Starlark values.
func TestABoundToolTakesKeywordsAndReturnsItsData(t *testing.T) {
	fc := caller(map[string]answer{"search": func(core.ToolUseBlock) (core.ToolResult, error) {
		return core.OKResult(map[string]any{"count": 42, "hits": []string{"a", "b"},
			"meta": map[string]any{"ratio": 0.5, "ok": true, "none": nil}}), nil
	}})
	res := runScript(t, context.Background(), Options{}, fc, `
def main():
    r = search(query="go", limit=3, tags=["x", "y"], opts={"deep": True}, ratio=1.5, missing=None)
    return [r["count"], r["hits"], r["meta"]["ratio"], r["meta"]["ok"], r["meta"]["none"]]
`, leaf("search"))
	if got := rv(t, res); got != `[42,["a","b"],0.5,true,null]` {
		t.Fatalf("return value = %s", got)
	}
	if len(fc.calls) != 1 || fc.calls[0].Name != "search" || fc.calls[0].ID != "codemode_1" ||
		!testutil.SameJSON(fc.calls[0].Input, []byte(`{"limit":3,"missing":null,"opts":{"deep":true},"query":"go","ratio":1.5,"tags":["x","y"]}`)) {
		t.Fatalf("calls = %+v", fc.calls)
	}
}

func TestABoundToolWithoutDataReturnsItsText(t *testing.T) {
	fc := caller(map[string]answer{"ping": func(core.ToolUseBlock) (core.ToolResult, error) {
		return core.ToolResult{OK: true, Text: "done"}, nil
	}})
	if got := rv(t, runScript(t, context.Background(), Options{}, fc, "result = ping()[\"text\"]\n", leaf("ping"))); got != `"done"` {
		t.Fatalf("return value = %s", got)
	}
}

// A call the script cannot make as written fails before the dispatcher sees
// it: positional arguments, or an argument JSON cannot carry.
func TestAMalformedCallIsRefusedBeforeItIsMade(t *testing.T) {
	for _, tc := range []struct{ script, detail string }{
		{`fetch("http://example.com")`, "fetch only accepts keyword arguments"},
		{`fetch(f=fetch)`, "fetch: argument f: cannot pass a builtin_function_or_method"},
		{"a = []\na.append(a)\nfetch(v=a)\n", "contains itself"},
	} {
		fc := caller(nil)
		res := runScript(t, context.Background(), Options{}, fc, tc.script, leaf("fetch"))
		if res.Error != "invalid_arguments" || !strings.Contains(res.Detail, tc.detail) || len(fc.calls) != 0 {
			t.Fatalf("%q: result = %+v, %d calls", tc.script, res, len(fc.calls))
		}
	}
}

// Outside an agent run there is no dispatcher, and the script says so instead
// of running the tool around the agent's interceptors.
func TestABoundToolWithoutADispatcherFails(t *testing.T) {
	res := runScript(t, context.Background(), Options{}, nil, "lookup()\n", leaf("lookup"))
	if res.OK || !strings.Contains(res.Detail, core.ErrNoNestedCaller.Error()) {
		t.Fatalf("result = %+v", res)
	}
}

// A terminate vote during a call stops the script at once, and the code-mode
// result carries the vote.
func TestATerminateVoteStopsTheScript(t *testing.T) {
	fc := caller(map[string]answer{
		"finish": func(core.ToolUseBlock) (core.ToolResult, error) { return core.ToolResult{}, core.ErrTerminated },
		"after":  okTool,
	})
	res := runScript(t, context.Background(), Options{}, fc, "finish()\nafter()\nprint('unreachable')\n", leaf("finish"), leaf("after"))
	if !res.Terminate || res.Error != "terminated" || strings.Contains(res.Text, "unreachable") || len(fc.calls) != 1 {
		t.Fatalf("result = %+v, calls %d", res, len(fc.calls))
	}
}

// A call cancelled under the script aborts it, whether the dispatcher reports
// the cancellation as an error or, as the agent's does, as results.
func TestACancelledCallAbortsTheScript(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(cancel func()) answer
	}{
		{"as an error", func(cancel func()) answer {
			return func(core.ToolUseBlock) (core.ToolResult, error) { cancel(); return core.ToolResult{}, context.Canceled }
		}},
		{"as a result", func(cancel func()) answer {
			return func(core.ToolUseBlock) (core.ToolResult, error) {
				cancel()
				return core.ErrResult("aborted", "Operation aborted"), nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fc := caller(map[string]answer{"slow_op": tc.answer(cancel)})
			res := runScript(t, ctx, Options{}, fc, "slow_op()\nslow_op()\n", leaf("slow_op"))
			if res.Error != "aborted" || res.Detail != "Operation aborted" || len(fc.calls) != 1 {
				t.Fatalf("result = %+v, calls %d", res, len(fc.calls))
			}
		})
	}
}
