package codemode_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

// fakeCaller stands in for the agent's nested dispatcher: it answers each
// call with the function registered for its tool, and records the calls.
type fakeCaller struct {
	mu    sync.Mutex
	fn    map[string]func(core.ToolUseBlock) (core.ToolResult, error)
	calls []core.ToolUseBlock
}

func (f *fakeCaller) Call(_ context.Context, blocks ...core.ToolUseBlock) ([]core.ToolResult, error) {
	out := make([]core.ToolResult, len(blocks))
	for i, b := range blocks {
		f.mu.Lock()
		f.calls = append(f.calls, b)
		fn := f.fn[b.Name]
		f.mu.Unlock()
		if fn == nil {
			out[i] = core.ErrResult("unknown_tool", b.Name)
			continue
		}
		r, err := fn(b)
		if err != nil {
			return nil, err
		}
		out[i] = r
	}
	return out, nil
}

// run executes script in a code-mode tool bound to tools, with fc as the
// nested dispatcher.
func run(t *testing.T, ctx context.Context, opts codemode.Options, fc *fakeCaller, script string, tools ...core.Tool) core.ToolResult {
	t.Helper()
	cm, _, err := codemode.New(tools, opts)
	if err != nil {
		t.Fatal(err)
	}
	if fc != nil {
		ctx = core.WithNestedCaller(ctx, fc)
	}
	in, _ := json.Marshal(map[string]string{"script": script})
	return cm.Execute(ctx, in)
}

// rv renders a result's return value as JSON, for comparison.
func rv(t *testing.T, res core.ToolResult) string {
	t.Helper()
	if !res.OK {
		t.Fatalf("script failed: %s: %s", res.Error, res.Detail)
	}
	b, err := json.Marshal(res.Data["return_value"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TS-08-12: each run gets a fresh thread with the safe builtins, the bound
// tools and the helpers — and nothing left over from an earlier run.
func TestRuntimeGlobals_TS08_12(t *testing.T) {
	ctx := context.Background()
	res := run(t, ctx, codemode.Options{}, &fakeCaller{}, `
def main():
    return [len([1, 2]), type(parallel), type(call), type(is_error), type(lookup),
            sorted([3, 1]), list(enumerate(["a"])), list(zip([1], [2])), min(1, 2), max(1, 2), abs(-1),
            repr("x"), str(1), int("2"), float(1), bool(0), dict(a=1), hasattr("", "upper"),
            getattr("ab", "upper")(), "upper" in dir("")]
`, leaf("lookup"))
	want := `[2,"builtin_function_or_method","builtin_function_or_method","builtin_function_or_method",` +
		`"builtin_function_or_method",[1,3],[[0,"a"]],[[1,2]],1,2,1,"\"x\"","1",2,1,false,{"a":1},true,"AB",true]`
	if got := rv(t, res); got != want {
		t.Fatalf("return value = %s\nwant %s", got, want)
	}
	cm, _, err := codemode.New(nil, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	first := cm.Execute(ctx, json.RawMessage(`{"script":"leftover = 1"}`))
	second := cm.Execute(ctx, json.RawMessage(`{"script":"result = leftover"}`))
	if !first.OK || second.OK || !strings.Contains(second.Detail, "undefined: leftover") {
		t.Fatalf("state leaked between runs: %+v / %+v", first, second)
	}
}

// TS-08-13: load is refused.
func TestSandboxRefusesLoad_TS08_13(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, `load("math.star", "pi")`)
	if res.OK || !strings.Contains(res.Detail, "load is not permitted in code_mode scripts") {
		t.Fatalf("result = %+v", res)
	}
}

// TS-08-14: no host primitive is defined.
func TestSandboxHasNoHostPrimitives_TS08_14(t *testing.T) {
	for _, name := range []string{"open", "os", "socket", "http", "environ", "subprocess", "time", "exec", "eval", "input", "__import__", "sys"} {
		res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, fmt.Sprintf("val = %s", name))
		if res.OK || !strings.Contains(res.Detail, "undefined: "+name) {
			t.Fatalf("%s: result = %+v, want undefined", name, res)
		}
	}
}
