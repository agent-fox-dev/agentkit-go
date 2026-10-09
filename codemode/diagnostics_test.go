package codemode_test

import (
	"context"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

func lineOf(t *testing.T, res core.ToolResult) int {
	t.Helper()
	l, ok := res.Data["line"].(int)
	if !ok {
		t.Fatalf("line = %#v, want an int", res.Data["line"])
	}
	return l
}

// TS-08-39: a syntax error is syntax_error with its line.
func TestDiagnosticsSyntaxError_TS08_39(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, "x = 1\ndef foo(\n  return 1\n")
	if res.OK || res.Error != "syntax_error" || !strings.Contains(res.Detail, "syntax") || lineOf(t, res) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Text, "line 3") || res.Data["error"] != "syntax_error" {
		t.Fatalf("text = %q", res.Text)
	}
}

// TS-08-40: a runtime error is runtime_error with the line it happened on,
// whether at top level, inside a function, or an undefined name.
func TestDiagnosticsRuntimeError_TS08_40(t *testing.T) {
	for _, tc := range []struct {
		script string
		line   int
		detail string
	}{
		{"x = 1 / 0\n", 1, "zero"},
		{"def f(d):\n    return d['missing']\n\ny = 2\nf({})\n", 2, "missing"},
		{"y = 1\nz = undefined_name + 1\n", 2, "undefined"},
		{"x = 'a' + 1\n", 1, "unknown binary op"},
	} {
		res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, tc.script)
		if res.OK || res.Error != "runtime_error" || !strings.Contains(res.Detail, tc.detail) || lineOf(t, res) != tc.line {
			t.Fatalf("%q: result = %+v", tc.script, res)
		}
	}
}

// A malformed call — positional arguments, an argument JSON cannot carry, a
// bad parallel entry — is invalid_arguments, with its line.
func TestDiagnosticsInvalidArguments(t *testing.T) {
	for _, script := range []string{
		"x = 1\nlookup(1)\n",
		"x = 1\nlookup(f=lookup)\n",
		"x = 1\nparallel([(lookup,)])\n",
	} {
		res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, script, leaf("lookup"))
		if res.OK || res.Error != "invalid_arguments" || lineOf(t, res) != 2 {
			t.Fatalf("%q: result = %+v", script, res)
		}
	}
}

// TS-08-43: a context cancelled before or during the script aborts it.
func TestDiagnosticsCancelled_TS08_43(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := run(t, ctx, codemode.Options{}, &fakeCaller{}, "for i in range(1000):\n    pass\n")
	if res.OK || res.Error != "aborted" || res.Detail != "Operation aborted" {
		t.Fatalf("before: result = %+v", res)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"stop": func(core.ToolUseBlock) (core.ToolResult, error) { cancel2(); return core.OKResult(nil), nil },
	}}
	res = run(t, ctx2, codemode.Options{MaxSteps: 1 << 40}, fc, "stop()\nwhile True:\n    pass\n", leaf("stop"))
	if res.OK || res.Error != "aborted" || res.Detail != "Operation aborted" {
		t.Fatalf("during: result = %+v", res)
	}
}
