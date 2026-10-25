package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A script sees Starlark's safe builtins, the bound tools and the helpers;
// the dialect allows while loops, top-level control flow, reassigned globals
// and sets.
func TestAScriptSeesTheBuiltinsToolsAndHelpers(t *testing.T) {
	res := runScript(t, context.Background(), Options{}, caller(nil), `
n = 0
while n < 2:
    n += 1
n = n + len(set([1, 1]))
def main():
    return [n, type(parallel), type(call), type(is_error), type(lookup),
            sorted([3, 1]), list(enumerate(["a"])), list(zip([1], [2])), min(1, 2), max(1, 2), abs(-1),
            repr("x"), str(1), int("2"), float(1), bool(0), dict(a=1), hasattr("", "upper"),
            getattr("ab", "upper")(), "upper" in dir("")]
`, leaf("lookup"))
	want := `[3,"builtin_function_or_method","builtin_function_or_method","builtin_function_or_method",` +
		`"builtin_function_or_method",[1,3],[[0,"a"]],[[1,2]],1,2,1,"\"x\"","1",2,1,false,{"a":1},true,"AB",true]`
	if got := rv(t, res); got != want {
		t.Fatalf("return value = %s\nwant %s", got, want)
	}
}

// The script has no file system, network, environment, processes or clock:
// no host primitive is defined and load is refused.
func TestAScriptCannotReachTheHost(t *testing.T) {
	for _, name := range []string{"open", "os", "socket", "http", "environ", "subprocess", "time", "exec", "eval", "input", "__import__", "sys"} {
		res := runScript(t, context.Background(), Options{}, caller(nil), fmt.Sprintf("val = %s", name))
		if res.OK || !strings.Contains(res.Detail, "undefined: "+name) {
			t.Fatalf("%s: result = %+v, want undefined", name, res)
		}
	}
	res := runScript(t, context.Background(), Options{}, caller(nil), `load("math.star", "pi")`)
	if res.OK || !strings.Contains(res.Detail, "load is not permitted in code_mode scripts") {
		t.Fatalf("load: result = %+v", res)
	}
}

func TestEachRunStartsFromAFreshThread(t *testing.T) {
	cm, _, err := New(nil, Options{})
	must(t, err)
	first := cm.Execute(context.Background(), json.RawMessage(`{"script":"leftover = 1"}`))
	second := cm.Execute(context.Background(), json.RawMessage(`{"script":"result = leftover"}`))
	if !first.OK || second.OK || !strings.Contains(second.Detail, "undefined: leftover") {
		t.Fatalf("state leaked between runs: %+v / %+v", first, second)
	}
}

func TestTheToolRefusesInputWithoutAScript(t *testing.T) {
	cm, _, err := New(nil, Options{})
	must(t, err)
	for _, in := range []string{`{"script":"  \n"}`, `{}`, `{"script":1}`, `not json`} {
		if res := cm.Execute(context.Background(), json.RawMessage(in)); res.Error != "invalid_arguments" {
			t.Fatalf("%s: result = %+v, want invalid_arguments", in, res)
		}
	}
}

// The return value is main()'s result when the script defines main, else the
// global result, else None; one JSON cannot carry fails the script.
func TestTheReturnValueComesFromMainThenResult(t *testing.T) {
	for _, tc := range []struct{ script, want string }{
		{"def main():\n    return 'from_main'\nresult = 'from_result'\n", `"from_main"`},
		{"result = 'from_result'\n", `"from_result"`},
		{"x = 1\n", `null`},
		{"result = (1, 'two', 1.5)\n", `[1,"two",1.5]`},
	} {
		if got := rv(t, runScript(t, context.Background(), Options{}, caller(nil), tc.script)); got != tc.want {
			t.Fatalf("%q: return value = %s, want %s", tc.script, got, tc.want)
		}
	}
	res := runScript(t, context.Background(), Options{}, caller(nil), "result = len\n")
	if res.Error != "script_failed" || !strings.Contains(res.Detail, "the return value: cannot pass a builtin_function_or_method") {
		t.Fatalf("an unencodable return value: %+v", res)
	}
}
