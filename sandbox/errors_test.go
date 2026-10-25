package sandbox_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// A SpecError a provider builds itself, with no value, states the field and
// the reason alone.
func TestSpecErrorWithoutAValueNamesTheFieldAndReason(t *testing.T) {
	err := fmt.Errorf("create: %w", &sandbox.SpecError{Provider: "podman", Field: "RequireLandlock", Reason: "the agent applied ABI 1"})
	if want := `create: sandbox: provider "podman" cannot honour Spec.RequireLandlock: the agent applied ABI 1`; err.Error() != want {
		t.Errorf("message %q, want %q", err, want)
	}
	if !errors.Is(err, sandbox.ErrUnsupported) || errors.Is(err, sandbox.ErrProtocol) {
		t.Errorf("%v matches the wrong sentinels", err)
	}
}
