package gateway

import (
	"errors"
	"fmt"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// refusalErr is the error a Create or SyncGit refusal becomes: a
// *sandbox.SpecError for an UNSUPPORTED field, else an error matching the
// sentinel for its kind. Each carries the gateway's message.
func refusalErr(provider string, r *sandboxpb.Refusal) error {
	msg := r.GetMessage()
	switch r.GetKind() {
	case sandboxpb.Refusal_UNSUPPORTED:
		if r.GetField() != "" {
			return &sandbox.SpecError{Provider: provider, Field: r.GetField(), Reason: refusalReason(r)}
		}
		return fmt.Errorf("%w: %s", sandbox.ErrUnsupported, msg)
	case sandboxpb.Refusal_NO_AGENT:
		return fmt.Errorf("%w: %s", sandbox.ErrNoAgent, msg)
	case sandboxpb.Refusal_NO_PUBLISHED_IMAGE:
		return fmt.Errorf("%w: %s", sandbox.ErrNoPublishedImage, msg)
	case sandboxpb.Refusal_PROTOCOL:
		return fmt.Errorf("%w: %s", sandbox.ErrProtocol, msg)
	case sandboxpb.Refusal_CAPACITY:
		return fmt.Errorf("%w: %s", ErrCapacity, msg)
	}
	return errors.New(msg)
}

// refusalReason is a SpecError's reason: the refusal's reason, followed by
// the gateway's message when that says more.
func refusalReason(r *sandboxpb.Refusal) string {
	switch {
	case r.GetMessage() == "" || r.GetMessage() == r.GetReason():
		return r.GetReason()
	case r.GetReason() == "":
		return r.GetMessage()
	}
	return r.GetReason() + " (gateway: " + r.GetMessage() + ")"
}
