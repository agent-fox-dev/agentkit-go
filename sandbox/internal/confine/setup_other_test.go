//go:build !linux

package confine

import (
	"errors"
	"reflect"
	"runtime"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// Off Linux, Setup still refuses unsafe flags, and otherwise applies nothing
// and says why.
func TestSetupOffLinuxAppliesNothing(t *testing.T) {
	var ue *UsageError
	if _, err := Setup(Inputs{Workdir: "/"}); !errors.As(err, &ue) {
		t.Errorf("Setup(--workdir /) = %v, want a usage error", err)
	}
	info, err := Setup(Inputs{Workdir: t.TempDir(), Mode: sandbox.NetworkNone})
	if want := (sandbox.LandlockInfo{Note: "Landlock is Linux-only (GOOS=" + runtime.GOOS + ")"}); err != nil || !reflect.DeepEqual(info, want) {
		t.Errorf("Setup = %+v, %v; want %+v", info, err, want)
	}
}
