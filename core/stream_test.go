package core

import (
	"strings"
	"testing"
)

func TestEventStreamPushNeverBlocksAndResultIsFedByEnd(t *testing.T) {
	s := NewEventStream(StreamOptions{})
	for range 100_000 {
		s.Push(TextDeltaEvent{Delta: "tok"}) // returns with no consumer at all
	}
	s.End(StreamResult{Message: &AssistantMessage{Content: Content{TextBlock{Text: "final"}}}})
	if got := s.Result(); got == nil || got.Content.Text() != "final" {
		t.Fatalf("Result() = %+v, want the message End recorded, read without consuming an event", got)
	}
	if s.Err() != nil {
		t.Fatalf("Err() = %v", s.Err())
	}
}

func TestEventStreamDrainsQueuedEventsBeforeReportingDone(t *testing.T) {
	s := NewEventStream(StreamOptions{})
	s.Push(TextDeltaEvent{Delta: "a"})
	s.Push(TextDeltaEvent{Delta: "b"})
	s.End(StreamResult{})
	s.Push(TextDeltaEvent{Delta: "after End"})
	var got []string
	for e := range s.Events() {
		got = append(got, e.(TextDeltaEvent).Delta)
	}
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("drained %v, want [a b] and nothing pushed after End", got)
	}
}

func TestEventStreamNextWakesOnPushAndEnd(t *testing.T) {
	s := NewEventStream(StreamOptions{})
	got := make(chan Event)
	go func() {
		for {
			e, ok := s.Next()
			if !ok {
				close(got)
				return
			}
			got <- e
		}
	}()
	s.Push(TurnStartEvent{TurnIndex: 7})
	if e := <-got; e.(TurnStartEvent).TurnIndex != 7 {
		t.Fatalf("Next() = %+v", e)
	}
	s.End(StreamResult{})
	if _, open := <-got; open {
		t.Fatal("Next did not report done after End")
	}
}

func TestEventStreamEventsMayBeAbandonedEarly(t *testing.T) {
	s := NewEventStream(StreamOptions{})
	for i := range 3 {
		s.Push(TurnStartEvent{TurnIndex: i})
	}
	for range s.Events() {
		break
	}
	if e, ok := s.Next(); !ok || e.(TurnStartEvent).TurnIndex != 1 {
		t.Fatalf("Next() after breaking out = %+v, %v; want the second event", e, ok)
	}
}

func TestEventStreamOverrunDropsTheConsumerNotTheRun(t *testing.T) {
	s := NewEventStream(StreamOptions{MaxPendingBytes: 1}) // raised to the floor
	s.Push(TextDeltaEvent{Delta: strings.Repeat("x", 1000)})
	if e, ok := s.Next(); !ok || len(e.(TextDeltaEvent).Delta) != 1000 {
		t.Fatal("an event under the floor must be delivered: a limit of 1 is raised to MinMaxPendingBytes")
	}
	s.Push(TextDeltaEvent{Delta: string(make([]byte, MinMaxPendingBytes+1))})
	s.Push(TextDeltaEvent{Delta: "after the overrun"})
	s.End(StreamResult{Message: &AssistantMessage{Content: Content{TextBlock{Text: "complete"}}}})
	if _, ok := s.Next(); ok {
		t.Fatal("the overrun consumer was not dropped")
	}
	if s.Err() != ErrStreamOverrun || s.Result().Content.Text() != "complete" {
		t.Fatalf("Err() = %v, Result() = %+v; want an overrun and the run's result", s.Err(), s.Result())
	}
}

func TestEventStreamEndIsIdempotentAndTheFirstWins(t *testing.T) {
	s := NewEventStream(StreamOptions{})
	s.End(StreamResult{Message: &AssistantMessage{ResponseID: "first"}})
	s.End(StreamResult{Message: &AssistantMessage{ResponseID: "second"}, Err: ErrAborted})
	if s.Result().ResponseID != "first" || s.Err() != nil {
		t.Fatal("the first End must win")
	}
}

func TestEventStreamRunResultIsTheAgentLevelResult(t *testing.T) {
	s := NewEventStream(StreamOptions{})
	s.End(StreamResult{Result: &RunResult{TurnCount: 3}, Err: ErrMaxTurns})
	if r, err := s.RunResult(); r.TurnCount != 3 || err != ErrMaxTurns {
		t.Fatalf("RunResult() = %+v, %v", r, err)
	}
	p := ErrorStream(nil, ErrBusy)
	if r, err := p.RunResult(); r.TurnCount != 0 || err != ErrBusy {
		t.Fatalf("RunResult() of a provider-level stream = %+v, %v; want zero and its error", r, err)
	}
}

func TestErrorStreamIsAlreadyEndedWithTheOriginalError(t *testing.T) {
	s := ErrorStream(nil, ErrToolRejected)
	ev, ok := s.Next()
	if e, isErr := ev.(ErrorEvent); !ok || !isErr || !e.Terminal || e.Err != ErrToolRejected || e.Message != ErrToolRejected.Error() {
		t.Fatalf("first event = %+v, want one terminal ErrorEvent", ev)
	}
	if _, ok := s.Next(); ok {
		t.Fatal("the stream must already be ended")
	}
	if s.Err() != ErrToolRejected {
		t.Fatalf("Err() = %v, want the original sentinel unwrapped", s.Err())
	}
	if m := s.Result(); m.StopReason != StopReasonError || m.ErrorMessage != ErrToolRejected.Error() {
		t.Fatalf("Result() = %+v", m)
	}
	partial := &AssistantMessage{Content: Content{TextBlock{Text: "partial"}}}
	if got := ErrorStream(partial, ErrAborted).Result(); got != partial {
		t.Fatalf("Result() = %+v, want the message given", got)
	}
}
