package codemode

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.starlark.net/starlark"
)

// A failed script is classified for the model with the line it failed on:
// syntax_error when it does not parse, runtime_error when it fails running or
// names something undefined, invalid_arguments for a malformed tool call.
func TestAScriptErrorIsClassifiedWithItsLine(t *testing.T) {
	for _, tc := range []struct {
		script, code string
		line         int
		detail       string
	}{
		{"x = 1\ndef foo(\n  return 1\n", "syntax_error", 3, "syntax error at script.star:3"},
		{"x = 1 / 0\n", "runtime_error", 1, "zero"},
		{"def f(d):\n    return d['missing']\n\ny = 2\nf({})\n", "runtime_error", 2, "missing"},
		{"y = 1\nz = undefined_name + 1\n", "runtime_error", 2, "undefined: undefined_name"},
		{"x = 'a' + 1\n", "runtime_error", 1, "unknown binary op"},
		{"x = 1\nlookup(1)\n", "invalid_arguments", 2, "keyword arguments"},
		{"x = 1\nlookup(f=lookup)\n", "invalid_arguments", 2, "argument f"},
		{"x = 1\nparallel([(lookup,)])\n", "invalid_arguments", 2, "parallel: call 0"},
	} {
		res := runScript(t, context.Background(), Options{}, caller(nil), tc.script, leaf("lookup"))
		if res.OK || res.Error != tc.code || res.Data["error"] != tc.code || res.Data["line"] != tc.line ||
			!strings.Contains(res.Detail, tc.detail) || !strings.HasPrefix(res.Text, "Script failed at line ") {
			t.Fatalf("%q: result = %+v", tc.script, res)
		}
	}
}

// An error inside nested functions carries the traceback; one at top level
// does not need it.
func TestADeepRuntimeErrorCarriesTheTraceback(t *testing.T) {
	res := runScript(t, context.Background(), Options{}, caller(nil), "def f():\n    return 1 / 0\ndef g():\n    return f()\ng()\n")
	if res.Error != "runtime_error" || res.Data["line"] != 2 || !strings.Contains(res.Text, "Traceback") {
		t.Fatalf("result = %+v", res)
	}
	if res := runScript(t, context.Background(), Options{}, caller(nil), "x = 1 / 0\n"); strings.Contains(res.Text, "Traceback") {
		t.Fatalf("a top-level error carries a traceback: %q", res.Text)
	}
}

func TestDiagnoseAnErrorWithoutAPosition(t *testing.T) {
	if d := diagnose(errors.New("odd")); d.code != "script_failed" || d.line != 0 || d.detail != "odd" {
		t.Fatalf("a plain error = %+v", d)
	}
	if d := diagnose(&starlark.EvalError{Msg: "no frames"}); d.code != "runtime_error" || d.line != 0 {
		t.Fatalf("an error with no call stack = %+v", d)
	}
}
