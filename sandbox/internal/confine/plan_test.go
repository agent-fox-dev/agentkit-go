package confine

import (
	"errors"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

func TestPlanChoosesTheABITCPAndNote(t *testing.T) {
	none, allow, open := sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted
	const allowNote = "Landlock cannot express a host allowlist; TCP is left to the provider"
	for _, c := range []struct {
		k    int
		err  error
		mode sandbox.NetworkMode
		want Decision
	}{
		{4, errors.New("boom"), none, Decision{Note: "Landlock unavailable: boom"}},
		{0, nil, open, Decision{Note: "kernel reports no Landlock support"}},
		{1, nil, none, Decision{Note: "kernel Landlock ABI 1 cannot permit cross-directory renames (needs ABI 2, Linux 5.19); not applied"}},
		{2, nil, none, Decision{ABI: 2, Note: "kernel Landlock ABI 2 cannot restrict TCP (needs ABI 4, Linux 6.7)"}},
		{3, nil, none, Decision{ABI: 3, Note: "kernel Landlock ABI 3 cannot restrict TCP (needs ABI 4, Linux 6.7)"}},
		{4, nil, none, Decision{ABI: 4, TCP: true}},
		{7, nil, none, Decision{ABI: 4, TCP: true}},
		{3, nil, allow, Decision{ABI: 3, Note: allowNote}},
		{6, nil, allow, Decision{ABI: 4, Note: allowNote}},
		{2, nil, open, Decision{ABI: 2}},
		{8, nil, open, Decision{ABI: 4}},
	} {
		if got := Plan(c.k, c.err, c.mode); got != c.want {
			t.Errorf("Plan(%d, %v, %v) = %+v, want %+v", c.k, c.err, c.mode, got, c.want)
		}
	}
}
