package core

import (
	"context"
	"encoding/json"
	"errors"
)

// Request is the canonical, provider-independent model call. Messages are the
// RAW canonical history: REQ-PROV-11 repair happens inside the provider, never
// here, because the loop is not running when a transcript is loaded from disk
// and no caller may be able to skip it. The model is the provider's: a
// ProviderClient is built for one.
type Request struct {
	System []ContentBlock
	// Prefix is sent after the system prompt and before Messages on every
	// request, with a cache breakpoint on its last block.
	Prefix   Messages
	Messages Messages
	// Tools is []ToolWire, not []Tool. That is the enforcement of REQ-TOOL-01.
	Tools         []ToolWire
	ToolChoice    ToolChoice
	MaxTokens     *int     // upper bound; capped at the model's output cap
	Temperature   *float64 // REQ-PROV-16 presence
	TopP          *float64
	StopSequences []string
	// Effort is the thinking effort; empty sends none.
	Effort Effort
}

// StreamEvent is one item of a provider stream: an event, and on the last
// item of a failed stream the error. The provider's final assistant message
// is the Message of the last MessageEndEvent, a failed turn included: its
// StopReason says it failed and ErrorMessage says why.
type StreamEvent struct {
	Event Event
	Err   error
}

// ProviderClient is a model behind a wire API. Stream sends one request and
// returns its events on a channel that is closed when the turn ends. It
// returns an error, and no channel, only when the request cannot be made at
// all — its context is already done, say; a failure once streaming has begun
// is the last item's Err.
type ProviderClient interface {
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}

// StreamChannel adapts an EventStream to a provider's channel: every event,
// then a last item carrying the stream's error, and its result message too
// when that was not the last event. The consumer must drain the channel.
func StreamChannel(s *EventStream) <-chan StreamEvent {
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		var last Event
		for e := range s.Events() {
			ch <- StreamEvent{Event: e}
			last = e
		}
		res := s.Wait()
		final := StreamEvent{Err: res.Err}
		if res.Message != nil {
			if end, ok := last.(MessageEndEvent); !ok || !sameMessage(end.Message, *res.Message) {
				final.Event = MessageEndEvent{Message: *res.Message}
			}
		}
		if final.Event != nil || final.Err != nil {
			ch <- final
		}
	}()
	return ch
}

// sameMessage reports whether two assistant messages are the same turn's
// result, without comparing their content block by block.
func sameMessage(a, b AssistantMessage) bool {
	return a.StopReason == b.StopReason && a.ErrorMessage == b.ErrorMessage &&
		len(a.Content) == len(b.Content) && a.Usage == b.Usage
}

// EventStreamOf adapts a provider's channel to an EventStream, so a consumer
// reads events and the result the same way whatever produced them. err is
// Stream's own error: it ends the stream at once.
func EventStreamOf(ch <-chan StreamEvent, err error) *EventStream {
	if err != nil {
		return ErrorStream(nil, err)
	}
	if ch == nil {
		// Ranging over a nil channel blocks forever: a provider that
		// returns neither a channel nor an error has failed.
		return ErrorStream(nil, errors.New("core: the provider returned no stream and no error"))
	}
	s := NewEventStream(StreamOptions{})
	go func() {
		var msg *AssistantMessage
		var serr error
		for ev := range ch {
			if ev.Event != nil {
				s.Push(ev.Event)
				if end, ok := ev.Event.(MessageEndEvent); ok {
					m := end.Message
					msg = &m
				}
			}
			if ev.Err != nil {
				serr = ev.Err
			}
		}
		s.End(StreamResult{Message: msg, Err: serr})
	}()
	return s
}

// Model is a catalog row: what a request to the model may ask for and what
// it costs. Prices are USD per million tokens.
type Model struct {
	ID              string
	ContextWindow   int
	MaxOutputTokens int

	InputCostPerMillion      float64
	OutputCostPerMillion     float64
	CacheReadCostPerMillion  float64
	CacheWriteCostPerMillion float64

	// ThinkingKind is how the model takes extended thinking.
	ThinkingKind ThinkingKind
	// Efforts is the wire value of each effort the model takes: a token
	// budget on a budget model, an effort name on an adaptive one. With a map,
	// an effort absent from it (or nil) is not sent; with no map at all, the
	// effort is sent by its own name.
	Efforts map[Effort]*string
	// Compat is the catalog row's capability object, as JSON (for example
	// {"supports_sampling": false}).
	Compat json.RawMessage
}

// ThinkingKind is how a model takes extended thinking.
type ThinkingKind string

const (
	// ThinkingKindNone is a model without extended thinking.
	ThinkingKindNone ThinkingKind = "none"
	// ThinkingKindAdaptive is thinking {"type":"adaptive"} with an effort.
	ThinkingKindAdaptive ThinkingKind = "adaptive"
	// ThinkingKindBudget is thinking {"type":"enabled"} with budget_tokens.
	ThinkingKindBudget ThinkingKind = "budget"
)
