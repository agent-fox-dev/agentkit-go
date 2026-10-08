package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestObservabilityExampleRunsKeyless runs the example and checks that every
// channel reported what its README says it does.
func TestObservabilityExampleRunsKeyless(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"session start  support-7f3a",
		"OnError        agentkit: panic in OnTurnStart: a bug in my metrics code",
		"turn 1 end    1 tool results",
		"session end    stop end_turn",
		`"error_code":"blocked_by_policy"`,
		`"kind":"session_end","session":"support-7f3a","stop_reason":"end_turn","cost_usd":0.0071`,
		"agentkit.tool_call     tool_name=lookup_order is_error=false",
		"agentkit.model_call    stop_reason=stop",
		`{"type":"agent_start","session_id":"support-7f3a"`,
		`{"type":"agent_done","stop_reason":"end_turn"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q\n%s", want, out.String())
		}
	}
	// The two lookups of the same order share an arguments hash.
	if n := strings.Count(out.String(), `"args_hash":"sha256:a6ca6025f652fa70…"`); n != 2 {
		t.Errorf("want 2 audit records sharing the lookup hash, got %d", n)
	}
}
