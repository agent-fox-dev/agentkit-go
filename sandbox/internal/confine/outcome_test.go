package confine

import (
	"errors"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The outcome's KernelABI is the probed value, never capped, and 0 after a
// probe error; the kernel is probed once.
func TestDecideReportsTheKernelABIUncapped(t *testing.T) {
	for _, c := range []struct {
		probe int
		err   error
		want  sandbox.LandlockInfo
	}{
		{6, nil, sandbox.LandlockInfo{ABI: 4, KernelABI: 6, TCP: true}},
		{1, nil, sandbox.LandlockInfo{KernelABI: 1, Note: Plan(1, nil, sandbox.NetworkNone).Note}},
		{5, errors.New("no"), sandbox.LandlockInfo{Note: "Landlock unavailable: no"}},
	} {
		calls := fakeProbe(t, c.probe, c.err)
		info, d := decide(sandbox.NetworkNone)
		if !reflect.DeepEqual(info, c.want) || d != Plan(c.probe, c.err, sandbox.NetworkNone) || *calls != 1 {
			t.Errorf("probe %d/%v: %+v, %+v after %d probes; want %+v", c.probe, c.err, info, d, *calls, c.want)
		}
	}
}
