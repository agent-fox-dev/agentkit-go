package sandbox

import (
	"errors"
	"fmt"
)

var (
	// ErrUnsupported: the spec sets a field the provider does not honour.
	ErrUnsupported = errors.New("sandbox: unsupported spec")
	// ErrProtocol: the agent speaks a different protocol major version.
	ErrProtocol = errors.New("sandbox: incompatible agent protocol")
	// ErrUnavailable: the sandbox met a transport failure, its agent ended,
	// or its TTL expired.
	ErrUnavailable = errors.New("sandbox: unavailable")
	// ErrClosed: the sandbox has been closed.
	ErrClosed = errors.New("sandbox: closed")
	// ErrToolMismatch: the agent's tool definitions differ from the host's.
	ErrToolMismatch = errors.New("sandbox: agent tool definitions differ from the host's")
	// ErrToolsConfigured: Tools was called again with different options.
	ErrToolsConfigured = errors.New("sandbox: tools already configured with different options")
	// ErrUnsafeArchive: a tar entry broke the entry rules.
	ErrUnsafeArchive = errors.New("sandbox: unsafe archive")
	// ErrNoAgent: the image has no executable agent at AgentPath. Container
	// providers return it, wrapped, after their missing-agent diagnostic.
	ErrNoAgent = errors.New("sandbox: the image has no executable agent at sandbox.AgentPath")
)

// SpecError is CheckSpec's refusal of a Spec field the provider's
// Capabilities do not cover. It matches ErrUnsupported.
type SpecError struct {
	Provider string
	// Field is the Spec field, such as Network.Mode or Resources.CPUs.
	Field  string
	Reason string
	value  string
}

func (e *SpecError) Error() string {
	if e.value == "" {
		// Built outside CheckSpec (attach's RequireLandlock refusal), where
		// the reason carries the facts.
		return fmt.Sprintf("sandbox: provider %q cannot honour Spec.%s: %s", e.Provider, e.Field, e.Reason)
	}
	return fmt.Sprintf("sandbox: provider %q cannot honour Spec.%s = %s: %s", e.Provider, e.Field, e.value, e.Reason)
}

// Is reports whether target is ErrUnsupported.
func (e *SpecError) Is(target error) bool { return target == ErrUnsupported }
