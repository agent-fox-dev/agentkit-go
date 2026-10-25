package codemode

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Each limit stops the script with an error that names it and its value, and
// keeps what the script printed before it stopped.
func TestALimitStopsTheScriptNamingIt(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		opts         Options
		code, detail string
	}{
		{"wall time", "print('started')\nwhile True:\n    pass\n",
			Options{MaxTimeout: 50 * time.Millisecond, MaxSteps: 1 << 40}, "timeout", "50ms time limit (MaxTimeout)"},
		{"steps", "print('started')\nfor x in range(1000):\n    pass\n",
			Options{MaxSteps: 100}, "step_limit_exceeded", "100 execution steps (MaxSteps)"},
		{"calls", "print('started')\nfor i in range(5):\n    ping()\n",
			Options{MaxCalls: 3}, "call_limit_exceeded", "more than 3 tool calls (MaxCalls)"},
		{"printed output", "print('started')\nprint('A' * 300)\nprint('after')\n",
			Options{MaxOutputBytes: 200, DisableSpill: true}, "output_limit_exceeded", "200 bytes (MaxOutputBytes)"},
		{"a return value", "print('started')\nresult = 'B' * 300\n",
			Options{MaxOutputBytes: 200, DisableSpill: true}, "output_limit_exceeded", "200 bytes (MaxOutputBytes)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := caller(map[string]answer{"ping": okTool})
			res := runScript(t, context.Background(), tc.opts, fc, tc.script, leaf("ping"))
			if res.OK || res.Error != tc.code || !strings.Contains(res.Detail, tc.detail) || strings.Contains(res.Text, "after") {
				t.Fatalf("result = %+v", res)
			}
			if !strings.HasPrefix(res.Data["partial_output"].(string), "started") {
				t.Fatalf("partial output = %q", res.Data["partial_output"])
			}
		})
	}
	// Well under the default limits, the same loop finishes.
	if res := runScript(t, context.Background(), Options{}, caller(nil), "for x in range(1000):\n    pass\n"); !res.OK {
		t.Fatalf("result = %+v", res)
	}
}

// MaxCalls counts single and parallel calls together, and a parallel call
// over the remaining budget makes none of its calls, however many batches it
// would take.
func TestTheCallBudgetIsCheckedBeforeAnyCallOfAParallel(t *testing.T) {
	for _, tc := range []struct {
		script string
		calls  int
	}{
		{"ping()\nparallel([call(ping), call(ping), call(ping)])\n", 1},
		{"parallel([call(ping), call(ping), call(ping), call(ping), call(ping)])\n", 0},
	} {
		fc := caller(map[string]answer{"ping": okTool})
		res := runScript(t, context.Background(), Options{MaxCalls: 4 - tc.calls, MaxConcurrentCalls: 2}, fc, tc.script, leaf("ping"))
		if res.Error != "call_limit_exceeded" || len(fc.calls) != tc.calls {
			t.Fatalf("%q: result = %+v, %d calls", tc.script, res, len(fc.calls))
		}
	}
}

// A context already cancelled when the script starts, or cancelled while it
// runs, aborts it; the caller cancelling is not a timeout.
func TestACancelledContextAbortsTheScript(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fc := caller(nil)
	res := runScript(t, ctx, Options{}, fc, "ping()\n", leaf("ping"))
	if res.Error != "aborted" || res.Detail != "Operation aborted" || len(fc.calls) != 0 {
		t.Fatalf("before: result = %+v", res)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	fc = caller(map[string]answer{"stop": func(core.ToolUseBlock) (core.ToolResult, error) { cancel2(); return core.OKResult(nil), nil }})
	res = runScript(t, ctx2, Options{MaxSteps: 1 << 40}, fc, "stop()\nwhile True:\n    pass\n", leaf("stop"))
	if res.Error != "aborted" || res.Detail != "Operation aborted" {
		t.Fatalf("during: result = %+v", res)
	}
}
