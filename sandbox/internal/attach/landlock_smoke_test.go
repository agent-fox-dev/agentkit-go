package attach

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-15-44: RequireLandlock refuses an agent reporting ABI 3 without TCP
// under NetworkNone, destroys it once and returns a SpecError from the
// provider. 15-PATH-2, 15-REQ-6.5, 15-REQ-6.8.
func TestSmokeRequireLandlockRefused_TS15_44(t *testing.T) {
	note := "kernel Landlock ABI 3 cannot restrict TCP (needs ABI 4, Linux 6.7)"
	f := landlockAgent(2, &sandboxpb.LandlockReport{Abi: 3, KernelAbi: 3, Note: note})
	caps := sandbox.Capabilities{Provider: "fake", Network: []sandbox.NetworkMode{sandbox.NetworkNone}, Landlock: true}
	eff, err := sandbox.CheckSpec(caps, sandbox.Spec{RequireLandlock: true, Network: sandbox.Network{Mode: sandbox.NetworkNone}})
	if err != nil {
		t.Fatal(err)
	}
	destroyed := 0
	sb, err := Dial(context.Background(), f.Serve(), Config{Provider: "fake", Spec: eff,
		Destroy: func(context.Context) error { destroyed++; return nil }})
	t.Cleanup(f.Stop)
	var se *sandbox.SpecError
	if sb != nil || !errors.Is(err, sandbox.ErrUnsupported) || !errors.As(err, &se) || se.Provider != "fake" || se.Field != "RequireLandlock" {
		t.Fatalf("Dial = %v, %v", sb, err)
	}
	for _, want := range []string{"ABI 3", "kernel ABI 3", "TCP false", note} {
		if !strings.Contains(se.Reason, want) {
			t.Errorf("reason %q lacks %q", se.Reason, want)
		}
	}
	if destroyed != 1 {
		t.Errorf("the destroy step ran %d times", destroyed)
	}
	select {
	case <-f.Closed():
	case <-time.After(5 * time.Second):
		t.Error("the fake agent did not see its connection closed")
	}
}
