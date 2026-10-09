package codemode_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

func okTool(core.ToolUseBlock) (core.ToolResult, error) {
	return core.OKResult(map[string]any{"ok": true}), nil
}

// TS-08-28: a script past MaxTimeout is stopped as a timeout. MaxSteps is
// raised so the step limit does not end the loop first.
func TestLimitsTimeout_TS08_28(t *testing.T) {
	start := time.Now()
	res := run(t, context.Background(), codemode.Options{MaxTimeout: 50 * time.Millisecond, MaxSteps: 1 << 40},
		&fakeCaller{}, "print('started')\nwhile True:\n    pass\n")
	if res.OK || res.Error != "timeout" || !strings.Contains(res.Detail, "50ms") {
		t.Fatalf("result = %+v", res)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v to stop", time.Since(start))
	}
	if res.Data["partial_output"] != "started" {
		t.Fatalf("partial output = %q", res.Data["partial_output"])
	}
}

// TS-08-29: a script past MaxSteps is stopped, naming the limit.
func TestLimitsSteps_TS08_29(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{MaxSteps: 100}, &fakeCaller{}, "for x in range(1000):\n    pass\n")
	if res.OK || res.Error != "step_limit_exceeded" || !strings.Contains(res.Detail, "100") {
		t.Fatalf("result = %+v", res)
	}
	// Well under the limit, the same loop finishes.
	if res := run(t, context.Background(), codemode.Options{}, &fakeCaller{}, "for x in range(1000):\n    pass\n"); !res.OK {
		t.Fatalf("result = %+v", res)
	}
}

// TS-08-30: MaxCalls counts single calls and parallel calls together, and the
// excess call is not made.
func TestLimitsCalls_TS08_30(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"ping": okTool}}
	res := run(t, context.Background(), codemode.Options{MaxCalls: 3}, fc, "for i in range(5):\n    ping()\n", leaf("ping"))
	if res.OK || res.Error != "call_limit_exceeded" || !strings.Contains(res.Detail, "3") || len(fc.calls) != 3 {
		t.Fatalf("result = %+v, %d calls", res, len(fc.calls))
	}
	fc = &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"ping": okTool}}
	res = run(t, context.Background(), codemode.Options{MaxCalls: 3}, fc, "ping()\nparallel([call(ping), call(ping), call(ping)])\n", leaf("ping"))
	if res.OK || res.Error != "call_limit_exceeded" || len(fc.calls) != 1 {
		t.Fatalf("parallel over budget: result = %+v, %d calls", res, len(fc.calls))
	}
}

// TS-08-31: output past MaxOutputBytes stops the script, naming the limit.
func TestLimitsOutput_TS08_31(t *testing.T) {
	res := run(t, context.Background(), codemode.Options{MaxOutputBytes: 200, DisableSpill: true}, &fakeCaller{},
		"print('A' * 300)\nprint('after')\n")
	if res.OK || res.Error != "output_limit_exceeded" || !strings.Contains(res.Detail, "200") ||
		strings.Contains(res.Text, "after") {
		t.Fatalf("result = %+v", res)
	}
	// A return value counts towards the limit too.
	res = run(t, context.Background(), codemode.Options{MaxOutputBytes: 200, DisableSpill: true}, &fakeCaller{},
		"result = 'B' * 300\n")
	if res.OK || res.Error != "output_limit_exceeded" {
		t.Fatalf("return value: result = %+v", res)
	}
}

// TS-08-32: a limit keeps the partial output and the calls that completed,
// in Data and in Text.
func TestLimitsKeepPartialOutputAndCalls_TS08_32(t *testing.T) {
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){"ping": okTool}}
	res := run(t, context.Background(), codemode.Options{MaxOutputBytes: 100, DisableSpill: true}, fc,
		"print('step 1 completed')\nping(id=1)\nprint('A' * 500)\n", leaf("ping"))
	if res.OK || res.Error != "output_limit_exceeded" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Data["partial_output"].(string), "step 1 completed") {
		t.Fatalf("partial output = %q", res.Data["partial_output"])
	}
	calls := res.Data["calls_completed"].([]any)
	if len(calls) != 1 || calls[0].(map[string]any)["tool"] != "ping" {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(res.Text, "step 1 completed") || !strings.Contains(res.Text, "ping(id=1)") ||
		!strings.Contains(res.Text, "output_limit_exceeded") {
		t.Fatalf("text = %q", res.Text)
	}
}
