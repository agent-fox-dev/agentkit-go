package confine

import (
	"errors"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// fakeProbe replaces the probe seam for the test with one returning abi and
// err, and counts its calls.
func fakeProbe(t *testing.T, abi int, err error) *int {
	t.Helper()
	calls := new(int)
	old := probeABI
	probeABI = func() (int, error) { *calls++; return abi, err }
	t.Cleanup(func() { probeABI = old })
	return calls
}

// TS-15-14: the report's KernelABI is the probe's value, unchanged by the cap
// at 4, and 0 after a probe error. 15-REQ-2.8.
func TestOutcomeKernelABI_TS15_14(t *testing.T) {
	for _, c := range []struct {
		probe    int
		err      error
		mode     sandbox.NetworkMode
		abi, kab int
		tcp      bool
	}{
		{6, nil, sandbox.NetworkNone, 4, 6, true},
		{8, nil, sandbox.NetworkUnrestricted, 4, 8, false},
		{3, nil, sandbox.NetworkNone, 3, 3, false},
		{1, nil, sandbox.NetworkNone, 0, 1, false},
		{5, errors.New("no"), sandbox.NetworkNone, 0, 0, false},
	} {
		calls := fakeProbe(t, c.probe, c.err)
		info, d := decide(c.mode)
		if info.ABI != c.abi || info.KernelABI != c.kab || info.TCP != c.tcp || d.ABI != c.abi || d.TCP != c.tcp || info.Note != d.Note {
			t.Errorf("probe %d/%v under %v: %+v, %+v; want ABI %d, KernelABI %d, TCP %v", c.probe, c.err, c.mode, info, d, c.abi, c.kab, c.tcp)
		}
		if *calls != 1 {
			t.Errorf("probe %d: the probe ran %d times", c.probe, *calls)
		}
	}
}
