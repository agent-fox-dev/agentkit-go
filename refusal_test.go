package agentkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// Issue #75 §3: a run that ends on a safety refusal says so. Control flow is
// unchanged (REQ-LOOP-01: a refusal does not short-circuit), but the run's
// stop reason is refusal, not a clean end_turn, and Run returns
// core.ErrRefusal carrying the provider's category.
func TestARunEndingOnARefusalReportsIt(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{{
		StopReason:    core.StopReasonRefusal,
		RawStopReason: "refusal",
		StopDetail:    "cyber: request declined",
	}}}
	res, err := newTestAgent(t, s, nil).Run(context.Background(), "go")
	if !errors.Is(err, core.ErrRefusal) {
		t.Fatalf("err = %v, want core.ErrRefusal", err)
	}
	if !strings.Contains(err.Error(), "cyber") {
		t.Fatalf("err = %q; it must carry the provider's category", err)
	}
	if res.StopReason != core.RunStopRefusal || res.LastReason != core.StopReasonRefusal {
		t.Fatalf("StopReason=%q LastReason=%q, want refusal/refusal", res.StopReason, res.LastReason)
	}
}
