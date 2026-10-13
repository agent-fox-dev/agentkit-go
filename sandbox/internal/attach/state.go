package attach

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// phase is where a sandbox is in its life.
type phase int

const (
	live phase = iota
	unavailable
	closed
)

// state is a sandbox's phase and, once unavailable, why.
type state struct {
	mu    sync.Mutex
	phase phase
	cause string
}

// fail makes a live sandbox unavailable for good.
func (st *state) fail(cause string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.phase == live {
		st.phase, st.cause = unavailable, cause
	}
}

// close marks the sandbox closed and reports whether it was not already.
func (st *state) close() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.phase == closed {
		return false
	}
	st.phase = closed
	return true
}

// gate is nil for a live sandbox, an error matching ErrUnavailable for an
// unavailable one, and ErrClosed for a closed one.
func (st *state) gate() error {
	st.mu.Lock()
	defer st.mu.Unlock()
	switch st.phase {
	case unavailable:
		return fmt.Errorf("%w: %s", sandbox.ErrUnavailable, st.cause)
	case closed:
		return sandbox.ErrClosed
	}
	return nil
}

// classify turns an RPC error into the error the caller sees. A done caller
// context is the caller's own doing. InvalidArgument, FailedPrecondition,
// NotFound and Unimplemented are the agent's deliberate refusals and leave
// the sandbox live; anything else is a transport failure, which makes it
// unavailable.
func (s *sb) classify(ctx context.Context, rpc string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("sandbox: %s: %w", rpc, ctx.Err())
	}
	switch status.Code(err) {
	case codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound:
		return fmt.Errorf("sandbox: agent refused %s: %s", rpc, status.Convert(err).Message())
	case codes.Unimplemented:
		return fmt.Errorf("sandbox: agent protocol %d.%d does not implement %s", s.agent.ProtocolMajor, s.agent.ProtocolMinor, rpc)
	}
	s.fail(fmt.Sprintf("%s: %v", rpc, err))
	return fmt.Errorf("%w: %s: %v", sandbox.ErrUnavailable, rpc, err)
}

// Live is the state of a sandbox this package attached, as its own gate
// reports it: nil while live, an error matching sandbox.ErrUnavailable once
// unavailable, and sandbox.ErrClosed once closed. A provider that wraps an
// attached sandbox uses it to refuse work on a dead one before running
// anything. A Sandbox this package did not create is an error.
func Live(s sandbox.Sandbox) error {
	x, ok := s.(*sb)
	if !ok {
		return fmt.Errorf("sandbox: %T was not attached by this package", s)
	}
	return x.gate()
}
