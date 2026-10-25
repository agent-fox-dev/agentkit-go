package codemode

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Every nested call, single or in parallel, is in the ledger in order, with
// its tool, arguments, outcome and error code.
func TestTheLedgerRecordsEveryCallInOrder(t *testing.T) {
	fc := caller(map[string]answer{"good": okTool, "bad": failing("nope", "failed on purpose")})
	res := runScript(t, context.Background(), Options{}, fc,
		"good(i=0)\nbad(i=1)\nparallel([call(bad, i=2), call(good, i=3)])\n", leaf("good"), leaf("bad"))
	want := `[{"arguments":{"i":0},"ok":true,"tool":"good"},{"arguments":{"i":1},"error":"nope","ok":false,"tool":"bad"},` +
		`{"arguments":{"i":2},"error":"nope","ok":false,"tool":"bad"},{"arguments":{"i":3},"ok":true,"tool":"good"}]`
	if !res.OK || ledger(t, res) != want {
		t.Fatalf("result = %+v\nledger %s\nwant   %s", res, ledger(t, res), want)
	}
}

// A script that fails after making calls reports them, with their arguments
// sorted by name, next to what it printed: their side effects stand.
func TestAFailedScriptReportsTheCallsItCompleted(t *testing.T) {
	fc := caller(map[string]answer{"ping": okTool, "bad": failing("nope", "x")})
	res := runScript(t, context.Background(), Options{}, fc,
		"print('step 1')\nping(id=1, b='x')\nbad()\nfail(\"now\")\n", leaf("ping"), leaf("bad"))
	if res.OK || len(fc.calls) != 2 || ledger(t, res) != `[{"arguments":{"b":"x","id":1},"ok":true,"tool":"ping"},`+
		`{"arguments":{},"error":"nope","ok":false,"tool":"bad"}]` {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{"Output before it stopped:\nstep 1", "Calls completed (2):",
		`- ping(b="x", id=1) ok`, "- bad() failed: nope"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("text lacks %q:\n%s", want, res.Text)
		}
	}
	if none := runScript(t, context.Background(), Options{}, caller(nil), "fail('x')\n"); !strings.HasSuffix(none.Text, "Calls completed: none") {
		t.Fatalf("text = %q", none.Text)
	}
}

// Calls that ran before a cancellation are in the ledger, even though the
// dispatcher reports the cancellation as results rather than an error.
func TestTheLedgerKeepsCallsCutShortByACancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fc := caller(map[string]answer{"ping": okTool, "stop": func(core.ToolUseBlock) (core.ToolResult, error) {
		cancel()
		return core.ErrResult("aborted", "Operation aborted"), nil
	}})
	res := runScript(t, ctx, Options{}, fc, "ping(i=1)\nparallel([call(ping, i=2), call(stop)])\n", leaf("ping"), leaf("stop"))
	if res.Error != "aborted" || strings.Count(ledger(t, res), `"tool"`) != 3 {
		t.Fatalf("result = %+v, ledger %s", res, ledger(t, res))
	}
}
