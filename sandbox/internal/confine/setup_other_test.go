//go:build !linux

package confine

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// TS-15-13 (behaviour half): off Linux, Setup applies nothing and reports
// ABI 0 with the Linux-only note. 15-REQ-2.7.
func TestStubSetup_TS15_13(t *testing.T) {
	info, err := Setup(Inputs{Workdir: t.TempDir(), Mode: sandbox.NetworkNone})
	want := sandbox.LandlockInfo{Note: "Landlock is Linux-only (GOOS=" + runtime.GOOS + ")"}
	if err != nil || !reflect.DeepEqual(info, want) {
		t.Errorf("Setup = %+v, %v; want %+v", info, err, want)
	}
}
