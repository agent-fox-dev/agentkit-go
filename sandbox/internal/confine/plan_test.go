package confine

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-15-8: Plan returns the ABI, TCP flag and note of the decision table for
// every kernel ABI from 0 to 8, a probe error and every network mode.
// 15-REQ-2.1, 15-REQ-2.2. This file has no build constraint.
func TestPlanTable_TS15_8(t *testing.T) {
	boom := errors.New("boom")
	modes := []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkAllowlist, sandbox.NetworkUnrestricted}
	for _, mode := range modes {
		for _, c := range []struct {
			k    int
			err  error
			note string
		}{
			{0, boom, "Landlock unavailable: boom"},
			{0, nil, "kernel reports no Landlock support"},
			{1, nil, "kernel Landlock ABI 1 cannot permit cross-directory renames (needs ABI 2, Linux 5.19); not applied"},
		} {
			if got := Plan(c.k, c.err, mode); got != (Decision{Note: c.note}) {
				t.Errorf("Plan(%d, %v, %v) = %+v, want ABI 0 and %q", c.k, c.err, mode, got, c.note)
			}
		}
		for k := 2; k <= 8; k++ {
			got := Plan(k, nil, mode)
			wantABI := min(k, 4)
			wantTCP := k >= 4 && mode == sandbox.NetworkNone
			if got.ABI != wantABI || got.TCP != wantTCP {
				t.Errorf("Plan(%d, nil, %v) = %+v, want ABI %d TCP %v", k, mode, got, wantABI, wantTCP)
			}
			switch {
			case mode == sandbox.NetworkAllowlist:
				if !strings.Contains(got.Note, "allowlist") || !strings.Contains(got.Note, "provider") || got.TCP {
					t.Errorf("Plan(%d, nil, allowlist) note %q", k, got.Note)
				}
			case k <= 3 && mode == sandbox.NetworkNone:
				if want := fmt.Sprintf("kernel Landlock ABI %d cannot restrict TCP (needs ABI 4, Linux 6.7)", k); got.Note != want {
					t.Errorf("Plan(%d, nil, none) note %q, want %q", k, got.Note, want)
				}
			case k >= 4:
				if got.Note != "" {
					t.Errorf("Plan(%d, nil, %v) note %q, want empty", k, mode, got.Note)
				}
			}
		}
	}
}
