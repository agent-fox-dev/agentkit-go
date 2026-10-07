package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/session"
)

// deferring is a provider that accepts a background submission: it answers
// the first call with a RECEIPT and no content, and redeems that receipt on
// FetchDeferred. It is the executable form of REQ-PROV-19's contract.
type deferring struct {
	handle    core.DeferredHandle
	redeemed  int
	sawWindow time.Duration
}

func (d *deferring) provider() core.APIProvider {
	return core.APIProvider{
		API: testAPI,
		Stream: func(ctx context.Context, m *core.Model, req core.Request, _ core.ProviderStreamOptions) *core.EventStream {
			st := core.NewEventStream(core.StreamOptions{})
			msg := core.AssistantMessage{
				StopReason: core.StopReasonStop,
				Content:    core.Content{core.TextBlock{Text: "immediate"}},
				Provider:   m.Provider, API: m.API, Model: m.ID,
			}
			if req.Deferred != nil {
				d.sawWindow = req.Deferred.Window
				h := d.handle
				msg = core.AssistantMessage{
					StopReason: core.StopReasonDeferred,
					Deferred:   &h,
					Provider:   m.Provider, API: m.API, Model: m.ID,
				}
			}
			st.Push(core.MessageEndEvent{Message: msg})
			st.End(core.StreamResult{Message: &msg})
			return st
		},
		FetchDeferred: func(ctx context.Context, m *core.Model, h core.DeferredHandle, _ core.ProviderStreamOptions) *core.EventStream {
			d.redeemed++
			st := core.NewEventStream(core.StreamOptions{})
			msg := core.AssistantMessage{
				StopReason: core.StopReasonStop,
				Content:    core.Content{core.TextBlock{Text: "the deferred answer for " + h.ID}},
				Provider:   m.Provider, API: m.API, Model: m.ID,
			}
			st.Push(core.MessageEndEvent{Message: msg})
			st.End(core.StreamResult{Message: &msg})
			return st
		},
	}
}

func testHandle() core.DeferredHandle {
	return core.DeferredHandle{
		Provider: "test", API: testAPI, ModelID: "test-model", ID: "job-1",
		ExpiresAt: time.Now().Add(24 * time.Hour),
		Data:      json.RawMessage(`{"queue":"batch"}`),
	}
}

// TestTheCapabilityIsProbedNotAssumed is REQ-PROV-19's first clause: a caller
// asks whether the wire supports deferral before submitting, because a
// provider that ignores the field answers immediately.
func TestTheCapabilityIsProbedNotAssumed(t *testing.T) {
	d := &deferring{handle: testHandle()}
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.Providers = core.ProviderRegistry{testAPI: d.provider()}
	})
	if !a.SupportsDeferred() {
		t.Fatal("a provider registering FetchDeferred must probe as supporting it")
	}

	plain := &scripted{}
	b := newTestAgent(t, plain, nil)
	if b.SupportsDeferred() {
		t.Fatal("a provider with no FetchDeferred must probe as NOT supporting it; " +
			"REQ-PROV-19 says the capability is probed, never assumed")
	}
}

// TestADeferredRunEndsWithItsReceipt: the run ends CLEAN holding a handle,
// not as an error and not as an empty completion (REQ-PROV-19, OQ-11).
func TestADeferredRunEndsWithItsReceipt(t *testing.T) {
	d := &deferring{handle: testHandle()}
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.Providers = core.ProviderRegistry{testAPI: d.provider()}
		c.RequestOptions.Deferred = &core.DeferredRequest{Window: 24 * time.Hour}
	})

	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatalf("a deferred submission is a clean end, not an error: %v", err)
	}
	if res.StopReason != core.RunStopDeferred {
		t.Fatalf("StopReason = %q, want deferred", res.StopReason)
	}
	if d.sawWindow != 24*time.Hour {
		t.Fatalf("the provider saw window %v; RequestOptions.Deferred must reach Request.Deferred", d.sawWindow)
	}
	h, ok := res.DeferredHandle()
	if !ok || h.ID != "job-1" {
		t.Fatalf("no handle on the result (%v, %v); the receipt is the only way back to the answer", h, ok)
	}
}

// TestADeferredStopWithNoHandleIsStillAnError: the empty-completion failure
// OQ-11 exists to foreclose. A stop reason with nothing to redeem is not a
// clean end.
func TestADeferredStopWithNoHandleIsStillAnError(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{{StopReason: core.StopReasonDeferred}}}
	a := newTestAgent(t, s, nil)
	_, err := a.Run(context.Background(), "go")
	if !errors.Is(err, core.ErrDeferredUnsupported) {
		t.Fatalf("err = %v, want ErrDeferredUnsupported", err)
	}
}

// TestRedeemingAHandleAppendsTheAnswer: one call, one attempt, appended to
// history — and no poller anywhere.
func TestRedeemingAHandleAppendsTheAnswer(t *testing.T) {
	d := &deferring{handle: testHandle()}
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.Providers = core.ProviderRegistry{testAPI: d.provider()}
		c.RequestOptions.Deferred = &core.DeferredRequest{Window: time.Hour}
	})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := res.DeferredHandle()

	before := a.History().Len()
	rres, err := a.RedeemDeferred(context.Background(), h)
	if err != nil {
		t.Fatalf("RedeemDeferred: %v", err)
	}
	msg := lastAssistant(rres.Messages)
	if msg == nil {
		t.Fatal("the RunResult carries no assistant message")
	}
	if d.redeemed != 1 {
		t.Fatalf("the provider was called %d times; redemption is one attempt, never a poll loop", d.redeemed)
	}
	if !strings.Contains(msg.Content.Text(), "job-1") {
		t.Fatalf("answer = %q", msg.Content.Text())
	}
	if a.History().Len() != before+1 {
		t.Fatal("the redeemed answer must be appended to history")
	}
	if !a.Idle() {
		t.Fatal("the run slot must be released after redemption")
	}
}

// TestAnExpiredOrForeignHandleIsRefusedBeforeTheWire: two failures that must
// not become network calls — an expired receipt, and one issued for another
// model. Redeeming the latter would collect someone else's answer.
func TestAnExpiredOrForeignHandleIsRefusedBeforeTheWire(t *testing.T) {
	d := &deferring{handle: testHandle()}
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.Providers = core.ProviderRegistry{testAPI: d.provider()}
	})

	expired := testHandle()
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	if _, err := a.RedeemDeferred(context.Background(), expired); err == nil ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("err = %v, want an expiry refusal", err)
	}

	foreign := testHandle()
	foreign.ModelID = "some-other-model"
	if _, err := a.RedeemDeferred(context.Background(), foreign); err == nil {
		t.Fatal("a handle issued for another model must be refused")
	}
	if d.redeemed != 0 {
		t.Fatal("neither refusal may reach the wire")
	}
	// A zero ExpiresAt means the provider stated no expiry, not "expired at
	// the epoch" — the same distinction credential expiry draws.
	never := testHandle()
	never.ExpiresAt = time.Time{}
	if _, err := a.RedeemDeferred(context.Background(), never); err != nil {
		t.Fatalf("a handle with no stated expiry must be redeemable: %v", err)
	}
}

// TestAHandleSurvivesTheProcess is REQ-PROV-19's durability clause: the
// receipt is serialized into the session log like any other entry, so a
// submission outlives the binary that made it.
func TestAHandleSurvivesTheProcess(t *testing.T) {
	h := testHandle()
	msg := core.AssistantMessage{
		StopReason: core.StopReasonDeferred,
		Deferred:   &h,
		Provider:   "test", API: testAPI, Model: "test-model",
	}
	raw, err := session.EncodeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"job-1"`) || !strings.Contains(string(raw), `"batch"`) {
		t.Fatalf("the handle did not reach the log: %s", raw)
	}

	back, err := session.DecodeMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := back.(core.AssistantMessage)
	if !ok || got.Deferred == nil {
		t.Fatalf("the handle did not survive the round trip: %#v", back)
	}
	if got.Deferred.ID != h.ID || got.Deferred.ModelID != h.ModelID || got.Deferred.API != h.API {
		t.Fatalf("handle = %+v, want %+v", *got.Deferred, h)
	}
	if !got.Deferred.ExpiresAt.Equal(h.ExpiresAt) {
		t.Fatalf("expiry = %v, want %v", got.Deferred.ExpiresAt, h.ExpiresAt)
	}
	// Provider-opaque bytes are replayed unmodified, the same rule that
	// governs thinking signatures.
	if strings.TrimSpace(string(got.Deferred.Data)) != `{"queue":"batch"}` {
		t.Fatalf("data = %s, want the bytes as issued", got.Deferred.Data)
	}
}

// TestRedemptionRoutesOnTheHandlesOwnAPI: a session whose model changed after
// the submission (REQ-SESS-03) must still redeem against the wire holding the
// answer, not the wire it happens to be on now.
func TestRedemptionRoutesOnTheHandlesOwnAPI(t *testing.T) {
	d := &deferring{handle: testHandle()}
	reg := core.ProviderRegistry{testAPI: d.provider()}
	h := testHandle()
	h.API = core.API("some-other-wire")
	st := reg.FetchDeferred(context.Background(), testModel(), h, core.ProviderStreamOptions{})
	if err := st.Err(); !errors.Is(err, core.ErrDeferredUnsupportedAPI) {
		t.Fatalf("err = %v, want ErrDeferredUnsupportedAPI", err)
	}
	if d.redeemed != 0 {
		t.Fatal("a handle for an unregistered wire must not reach this provider")
	}
}

// Issue #80 §1: an ABORTED redemption is not an answer. It is returned as the
// abort error, and nothing is appended.
func TestAnAbortedRedemptionIsNotRecorded(t *testing.T) {
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.Providers = core.ProviderRegistry{testAPI: {
			API:    testAPI,
			Stream: (&scripted{}).stream,
			FetchDeferred: func(context.Context, *core.Model, core.DeferredHandle, core.ProviderStreamOptions) *core.EventStream {
				return core.ErrorStream(&core.AssistantMessage{StopReason: core.StopReasonAborted}, core.ErrAborted)
			},
		}}
	})
	before := a.History().Len()
	if _, err := a.RedeemDeferred(context.Background(), testHandle()); err == nil {
		t.Fatal("an aborted redemption returned no error")
	}
	if a.History().Len() != before {
		t.Fatal("the aborted marker was appended as though it were the answer")
	}
}

// Issue #80 §2: a redeemed answer that calls a tool is a turn like any other —
// the tool runs, its result is recorded, and the loop carries on to the
// model's next turn; RedeemDeferred returns that run's result.
func TestARedeemedToolCallIsExecuted(t *testing.T) {
	var calls atomic.Int32
	modelCalls := 0
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{echoTool("echo", &calls)}
		c.Providers = core.ProviderRegistry{testAPI: {
			API: testAPI,
			Stream: func(_ context.Context, m *core.Model, req core.Request, _ core.ProviderStreamOptions) *core.EventStream {
				modelCalls++
				msg := core.AssistantMessage{StopReason: core.StopReasonStop,
					Content: core.Content{core.TextBlock{Text: "after the tool"}}, Provider: m.Provider, API: m.API, Model: m.ID}
				return doneStream(&msg)
			},
			FetchDeferred: func(_ context.Context, m *core.Model, _ core.DeferredHandle, _ core.ProviderStreamOptions) *core.EventStream {
				tu, _ := core.NewToolUse("c1", "echo", json.RawMessage(`{"v":"x"}`))
				msg := core.AssistantMessage{StopReason: core.StopReasonToolUse, Content: core.Content{tu},
					Provider: m.Provider, API: m.API, Model: m.ID}
				return doneStream(&msg)
			},
		}}
	})
	res, err := a.RedeemDeferred(context.Background(), testHandle())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("the redeemed answer's tool ran %d times, want 1", calls.Load())
	}
	if modelCalls != 1 || res.LastReason != core.StopReasonStop {
		t.Fatalf("model calls after the batch = %d, last reason %q; the loop must carry on to the next turn",
			modelCalls, res.LastReason)
	}
	var sawResult bool
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" && !tr.IsError {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatal("the tool's real result is not in the run")
	}
}

// Issue #80 §3: a handle whose PollAfterMS has not elapsed since it was
// issued is refused before the wire; the loop stamps IssuedAt when the
// provider did not.
func TestAHandleIsNotRedeemedBeforeItsPollAfter(t *testing.T) {
	d := &deferring{handle: testHandle()}
	d.handle.PollAfterMS = 60_000
	a := newTestAgent(t, nil, func(c *core.AgentConfig) {
		c.Providers = core.ProviderRegistry{testAPI: d.provider()}
		c.RequestOptions.Deferred = &core.DeferredRequest{Window: time.Hour}
	})
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := res.DeferredHandle()
	if h.IssuedAt.IsZero() {
		t.Fatal("the loop did not stamp IssuedAt on a handle the provider left unstamped")
	}
	if _, err := a.RedeemDeferred(context.Background(), h); err == nil || !strings.Contains(err.Error(), "poll") {
		t.Fatalf("err = %v, want a refusal until PollAfterMS has elapsed", err)
	}
	if d.redeemed != 0 {
		t.Fatal("the refusal reached the wire")
	}

	h.IssuedAt = time.Now().Add(-2 * time.Minute)
	if _, err := a.RedeemDeferred(context.Background(), h); err != nil {
		t.Fatalf("a handle past its PollAfterMS must be redeemable: %v", err)
	}
	old := testHandle() // an older log's handle, with no IssuedAt
	old.PollAfterMS = 60_000
	if _, err := a.RedeemDeferred(context.Background(), old); err != nil {
		t.Fatalf("a handle with no IssuedAt cannot be judged and must not be refused: %v", err)
	}
}

// doneStream is a completed stream carrying msg.
func doneStream(msg *core.AssistantMessage) *core.EventStream {
	st := core.NewEventStream(core.StreamOptions{})
	st.Push(core.MessageEndEvent{Message: *msg})
	st.End(core.StreamResult{Message: msg})
	return st
}

// Issue #80 §3: IssuedAt, which PollAfterMS counts from, survives the log, so
// a restarted process can still tell whether a handle is due.
func TestIssuedAtSurvivesTheLog(t *testing.T) {
	h := testHandle()
	h.PollAfterMS = 30_000
	h.IssuedAt = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
	raw, err := session.EncodeMessage(core.AssistantMessage{StopReason: core.StopReasonDeferred, Deferred: &h})
	if err != nil {
		t.Fatal(err)
	}
	back, err := session.DecodeMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	got := back.(core.AssistantMessage).Deferred
	if got == nil || !got.IssuedAt.Equal(h.IssuedAt) || got.PollAfterMS != 30_000 {
		t.Fatalf("handle after the log = %+v; issued_at and poll_after_ms must survive", got)
	}
}
