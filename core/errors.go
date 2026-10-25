package core

import "errors"

// Sentinel errors, all comparable with errors.Is.
var (
	ErrMaxTurns       = errors.New("agentkit: maximum turns reached")
	ErrBudgetExceeded = errors.New("agentkit: budget exceeded")
	ErrToolRejected   = errors.New("agentkit: tool call rejected")
	ErrRefusal        = errors.New("agentkit: model refused the request")
	// ErrBusy: a conflicting operation was attempted while a turn was in
	// flight. The caller may retry; it is never queued.
	ErrBusy = errors.New("agentkit: agent is busy")
	// ErrAborted: the run stopped on a cancelled context, or a provider ended
	// its stream as aborted. On a cancellation the run's error wraps it
	// beside the context's own error, so errors.Is matches either.
	ErrAborted       = errors.New("agentkit: run aborted")
	ErrStreamOverrun = errors.New("agentkit: stream consumer overrun")

	// ErrNoNestedCaller: CallNested was used where no NestedCaller is
	// attached — a tool that declares no ReachableTools, or one invoked by
	// something that is neither an agent run nor a Runner.
	ErrNoNestedCaller = errors.New(
		"agentkit: no nested caller on the context; only a tool that declares ReachableTools can call other tools")
	// ErrTerminated: an interceptor voted to end the run during a nested
	// call. The wrapper should stop and return; its result carries the vote
	// to the batch whatever it returns.
	ErrTerminated = errors.New("agentkit: run terminated by an interceptor during a nested call")

	// Raised by the loop / session layer.
	ErrNotContinuable = errors.New("agentkit: transcript is not continuable")
	ErrSessionExists  = errors.New("agentkit: session log already exists at path")
	// ErrSessionNotEmpty makes the forbidden "construct, then patch the model
	// onto it" resume a runtime failure on the first line rather than a style
	// violation.
	ErrSessionNotEmpty = errors.New(
		"agentkit: session store is not empty; resume with LoadSession + NewAgentFromSession")

	// ErrUnguardedExecute: a toolset that reaches a shell tool (execute,
	// run_command) with no Guard is refused by agentkit.NewRunner, and so by
	// agentkit.New, before any request. A headless embedder with no policy
	// would otherwise hand the model an unrestricted shell by omission.
	// Supply an interceptor — guard.Restricted is the shipped starting point
	// — or pass guard.AllowAll to say so explicitly.
	ErrUnguardedExecute = errors.New(
		"agentkit: a shell tool is reachable but Toolset.Guard is nil; " +
			"supply an interceptor (guard.Restricted is the shipped starting point) " +
			"or guard.AllowAll to opt out explicitly")
)
