package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestDeferredExampleRunsKeyless runs the example and checks the lifecycle its
// README describes: probe, clean deferred end, handle in the log, the
// poll-after refusal, a not-ready fetch, and a redemption that runs the loop.
func TestDeferredExampleRunsKeyless(t *testing.T) {
	var out bytes.Buffer
	if err := run(&out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"anthropic-messages   SupportsDeferred = false",
		"batch                SupportsDeferred = true",
		"run ended: stop=deferred, err=nil, handle present=true",
		"last assistant turn holds handle job_001: true",
		`deferred handle "job_001" is not due yet`,
		"batch: job job_001 is still running",
		"after the job ran: 2 turns, stop=end_turn",
		`tool_result {"data":{"count":3`,
		"assistant   (receipt job_001, stop deferred)",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q\n%s", want, out.String())
		}
	}
}
