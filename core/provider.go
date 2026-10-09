package core

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
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

// Model is REQ-PROV-10's descriptor. Provider is a VENDOR id used only for
// credential resolution and catalog lookup; API selects the implementation.
type Model struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	API           API                `json:"api"`
	Provider      string             `json:"provider"`
	BaseURL       string             `json:"base_url"`
	Headers       map[string]*string `json:"headers,omitzero"`
	Compat        json.RawMessage    `json:"compat,omitzero"`
	ContextWindow int                `json:"context_window"`
	MaxTokens     int                `json:"max_tokens"`
	Cost          Cost               `json:"cost"`
	Input         []string           `json:"input"` // modalities: "text","image"
	Reasoning     bool               `json:"reasoning"`
	// ThinkingLevelMap is the catalog row's wire value per level: a token
	// budget or an effort name. Present-null ("explicitly unsupported") and
	// absent both mean the level is not sent; the distinction is
	// catalog-authoring metadata for the REQ-CAT-06 diff.
	ThinkingLevelMap map[ThinkingLevel]*string `json:"thinking_level_map,omitzero"`
	Cloned           bool                      `json:"-"` // REQ-CAT-03
	ClonedFrom       string                    `json:"-"`
	// Thinking is how the model takes extended thinking, from its catalog row.
	Thinking ThinkingKind `json:"thinking,omitzero"`
}

// ThinkingMode is how m takes thinking: Thinking when the catalog set it,
// else what its level map implies.
func (m *Model) ThinkingMode() ThinkingKind {
	if m.Thinking != "" {
		return m.Thinking
	}
	return ThinkingKindOf(m.ThinkingLevelMap)
}

// ThinkingKindOf reads how a model takes thinking from its level map: token
// counts mean budget, effort names mean adaptive, and nothing above off means
// none.
func ThinkingKindOf(levels map[ThinkingLevel]*string) ThinkingKind {
	kind := ThinkingKindNone
	for lvl, wire := range levels {
		if wire == nil || lvl == ThinkingOff {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(*wire)); err == nil {
			return ThinkingKindBudget
		}
		kind = ThinkingKindAdaptive
	}
	return kind
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

func (m *Model) SupportsImages() bool {
	for _, in := range m.Input {
		if in == "image" {
			return true
		}
	}
	return false
}

type Cost struct {
	Input      float64    `json:"input"` // USD per 1M tokens
	Output     float64    `json:"output"`
	CacheRead  float64    `json:"cache_read"`
	CacheWrite float64    `json:"cache_write"`
	Tiers      []CostTier `json:"tiers,omitzero"` // REQ-PROV-05.4
}

type CostTier struct {
	Threshold  int     `json:"threshold"` // strictly exceeded
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"`
}
