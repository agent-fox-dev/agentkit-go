package codemode_test

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
)

// TS-08-41: every call is in the ledger, in order, with its tool, arguments,
// outcome and error code.
func TestLedgerRecordsEveryCall_TS08_41(t *testing.T) {
	r := rand.New(rand.NewSource(41))
	for iter := range 20 {
		fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
			"good": okTool, "bad": failing("nope", "failed on purpose"),
		}}
		var lines []string
		var want []string
		for i := range 1 + r.Intn(6) {
			name := []string{"good", "bad"}[r.Intn(2)]
			if r.Intn(3) == 0 {
				lines = append(lines, fmt.Sprintf("parallel([call(%s, i=%d)])", name, i))
			} else {
				lines = append(lines, fmt.Sprintf("%s(i=%d)", name, i))
			}
			want = append(want, name)
		}
		res := run(t, context.Background(), codemode.Options{}, fc, strings.Join(lines, "\n")+"\n", leaf("good"), leaf("bad"))
		if !res.OK {
			t.Fatalf("iteration %d: %+v", iter, res)
		}
		ledger := res.Data["calls_completed"].([]any)
		if len(ledger) != len(want) {
			t.Fatalf("iteration %d: %d entries, want %d", iter, len(ledger), len(want))
		}
		for i, e := range ledger {
			m := e.(map[string]any)
			args := m["arguments"].(map[string]any)
			if m["tool"] != want[i] || args["i"] != float64(i) {
				t.Fatalf("iteration %d: entry %d = %+v", iter, i, m)
			}
			if ok := m["tool"] == "good"; m["ok"] != ok || (!ok && m["error"] != "nope") {
				t.Fatalf("iteration %d: entry %d = %+v", iter, i, m)
			}
		}
	}
}

// TS-08-42: a script that fails after making calls reports them in Data and
// Text; their side effects stand.
func TestLedgerReportedOnFailure_TS08_42(t *testing.T) {
	var effects []string
	fc := &fakeCaller{fn: map[string]func(core.ToolUseBlock) (core.ToolResult, error){
		"ping": func(b core.ToolUseBlock) (core.ToolResult, error) {
			effects = append(effects, string(b.Input))
			return core.OKResult(nil), nil
		},
	}}
	// A runtime failure: an undefined name would be refused before the
	// script ran at all, since Starlark resolves names first.
	res := run(t, context.Background(), codemode.Options{}, fc, "ping(id=1)\nping(id=2)\nfail(\"now\")\n", leaf("ping"))
	if res.OK {
		t.Fatalf("result = %+v", res)
	}
	if calls := res.Data["calls_completed"].([]any); len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(res.Text, "ping(id=1)") || !strings.Contains(res.Text, "ping(id=2)") {
		t.Fatalf("text = %q", res.Text)
	}
	if len(effects) != 2 {
		t.Fatalf("side effects = %v", effects)
	}
}
