package agentkit

import (
	"context"
	"fmt"
	"time"

	"github.com/agentfox/agentkit-go/core"
)

// Deferred submission, REQ-PROV-19 as scoped by OQ-11 option (b): the type,
// the stop reason and the redemption call ship; the POLLER does not. Nothing
// here sleeps, retries, or watches a handle — when to come back is the
// embedder's decision, and a library that guessed would be spending someone
// else's latency budget on a schedule it invented.
//
// The shape of a deferred session:
//
//	if !reg.SupportsDeferred(model.API) { /* submit normally */ }
//	cfg.RequestOptions.Deferred = &core.DeferredRequest{Window: 24 * time.Hour}
//	res, _ := agent.Run(ctx, prompt)          // ends RunStopDeferred
//	h, _ := res.DeferredHandle()              // durable; persisted in the log
//	                                          // ... a day and a restart later:
//	res, err = agent.RedeemDeferred(ctx, h)   // the answer, and the run from it

// SupportsDeferred reports whether this agent's wire can accept a background
// submission (REQ-PROV-19's capability probe). Ask BEFORE submitting: a
// provider that ignores the field answers immediately, and a caller that
// assumed otherwise waits for a handle that is never coming.
func (a *Agent) SupportsDeferred() bool {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	reg := cfg.Providers
	if reg == nil {
		reg = DefaultProviders()
	}
	return reg.SupportsDeferred(cfg.Model.API)
}

// RedeemDeferred collects the answer for a handle and runs the loop from it,
// exactly as a live turn would land: the answer is appended to history and the
// session log, its tool calls are executed, TurnEnd fires, the stop policy is
// consulted, and the loop carries on to the model's next turn if the answer
// asked for one. It returns that run's result. A redeemed answer is most often
// a tool call — the first turn of a coding agent usually is — and appending it
// unexecuted left a transcript no later call could continue.
//
// It is NOT a poller: one call, one attempt. A handle whose `PollAfterMS` has
// not elapsed since it was issued (DeferredHandle.PollReadyAt) is refused
// rather than sent, because a provider that told us when to come back has
// already answered the question of whether it is ready, and burning the
// request to hear "not yet" is a round trip for nothing. A handle with no
// IssuedAt, from an older log, cannot be judged and is not refused.
//
// A fetch that ends in error or ABORTED is not an answer: it is returned as
// an error and nothing is recorded, so the handle can be redeemed again.
//
// The run slot is claimed for the duration, so redemption cannot interleave
// with a live turn writing to the same history (REQ-LOOP-15).
func (a *Agent) RedeemDeferred(ctx context.Context, h core.DeferredHandle) (core.RunResult, error) {
	if h.IsZero() {
		return core.RunResult{}, fmt.Errorf("agentkit: RedeemDeferred: empty handle")
	}
	now := time.Now()
	if h.Expired(now) {
		return core.RunResult{}, fmt.Errorf(
			"agentkit: deferred handle %q expired at %s; the answer is gone and the request must be re-issued",
			h.ID, h.ExpiresAt.Format(time.RFC3339))
	}
	if ready, ok := h.PollReadyAt(); ok && now.Before(ready) {
		return core.RunResult{}, fmt.Errorf(
			"agentkit: deferred handle %q is not due until %s (poll_after_ms %d); redeem it then",
			h.ID, ready.Format(time.RFC3339), h.PollAfterMS)
	}

	rctx, cancel, pending, err := a.claimSlot(ctx)
	if err != nil {
		return core.RunResult{}, err
	}
	defer cancel()
	defer a.releaseSlot()
	// Anything the slot claim drained goes back: redemption is not a turn and
	// must not swallow a steering message meant for the next one.
	if len(pending) > 0 {
		a.mu.Lock()
		a.steering = append(pending, a.steering...)
		a.mu.Unlock()
	}

	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	reg := cfg.Providers
	if reg == nil {
		reg = DefaultProviders()
	}

	// The handle's own model, not the agent's current one: a session whose
	// model changed after the submission (REQ-SESS-03) must still redeem
	// against the wire holding the answer.
	model := cfg.Model
	if model == nil || model.ID != h.ModelID || model.API != h.API {
		return core.RunResult{}, fmt.Errorf(
			"agentkit: deferred handle %q was issued for model %q on api %q, and this agent is on %q; "+
				"redeem it from an agent configured for the issuing model",
			h.ID, h.ModelID, h.API, cfg.Model.ID)
	}

	a.setPhase(core.PhaseCallingModel)
	defer a.setPhase(core.PhaseIdle)

	stream := reg.FetchDeferred(rctx, model, h, core.ProviderStreamOptions{CacheRetention: cfg.CacheRetention})
	msg := stream.Result()
	if msg == nil {
		err := stream.Err()
		if err == nil {
			err = fmt.Errorf("agentkit: the provider ended the redemption stream with no message")
		}
		return core.RunResult{}, err
	}
	switch msg.StopReason {
	case core.StopReasonError:
		return core.RunResult{}, fmt.Errorf("agentkit: redeeming deferred handle %q: %s", h.ID, msg.ErrorMessage)
	case core.StopReasonAborted:
		return core.RunResult{}, fmt.Errorf("agentkit: redeeming deferred handle %q: %w", h.ID, a.abortError(rctx))
	}

	// The redeemed answer replaces nothing: it is appended, so the transcript
	// reads submission-then-answer and the append-only log stays true. The
	// loop records it, executes its tool calls, and carries on from it.
	s := core.NewEventStream(cfg.StreamOptions)
	res, err := a.runLoop(rctx, s, nil, nil, msg)
	s.End(core.StreamResult{Message: lastAssistant(res.Messages), Result: &res, Err: err})
	return res, err
}
