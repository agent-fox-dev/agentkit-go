package local_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-15-27: local reports Capabilities.Landlock false, and its in-process
// agent reports ABI 0 with the no-confinement note. 15-REQ-4.7, 15-REQ-6.1.
func TestLocalLandlock_TS15_27(t *testing.T) {
	isolate(t)
	if local.Unisolated().Capabilities().Landlock {
		t.Error("local claims Capabilities.Landlock")
	}
	sb := create(t, sandbox.Spec{Network: unrestricted})
	want := sandbox.LandlockInfo{Note: "in-process agent (local provider): no confinement"}
	if got := sb.Agent().Landlock; !reflect.DeepEqual(got, want) {
		t.Errorf("Agent().Landlock = %+v, want %+v", got, want)
	}
	if (sandbox.Spec{}).RequireLandlock {
		t.Error("the zero Spec sets RequireLandlock")
	}
}

// TS-15-35: local refuses RequireLandlock with an error naming it that
// matches ErrUnsupported, and creates no sandbox directory. 15-REQ-6.4.
func TestLocalRefusesRequireLandlock_TS15_35(t *testing.T) {
	root := isolate(t)
	before := runtime.NumGoroutine()
	sb, err := local.Unisolated().Create(context.Background(), sandbox.Spec{Network: unrestricted, RequireLandlock: true})
	if sb != nil || err == nil || !strings.Contains(err.Error(), "RequireLandlock") || !errors.Is(err, sandbox.ErrUnsupported) {
		t.Fatalf("Create = %v, %v; want nil and an ErrUnsupported naming RequireLandlock", sb, err)
	}
	if ents, err := os.ReadDir(root); err == nil && len(ents) > 0 {
		t.Errorf("%s holds %v", root, ents)
	}
	time.Sleep(20 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("%d goroutines before, %d after: an agent was started", before, after)
	}
}
