package core

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// everyEvent is one value of each event type, with payloads in every field
// a consumer reads.
func everyEvent(t *testing.T) []Event {
	call := toolUse(t, "c1", "edit", `{"path":"a.go"}`)
	msg := AssistantMessage{Content: Content{TextBlock{Text: "t"}, ThinkingBlock{Thinking: "th", Signature: "s"}, call}}
	res := ToolResultMessage{ToolUseID: "c1", Content: Content{TextBlock{Text: "r"}}}
	return []Event{
		AgentStartEvent{SessionID: "s", Provider: "p", API: APIFaux, Model: "m"},
		AgentDoneEvent{Result: RunResult{Messages: Messages{msg, res}, StopReason: RunStopEndTurn}},
		TurnStartEvent{TurnIndex: 1},
		TurnEndEvent{TurnIndex: 1, Message: msg, ToolResults: []ToolResultMessage{res}},
		MessageStartEvent{Message: msg}, MessageUpdateEvent{Message: msg}, MessageEndEvent{Message: msg},
		TextStartEvent{BlockIndex: 0}, TextDeltaEvent{Delta: "d"}, TextEndEvent{Text: "t"},
		ThinkingStartEvent{}, ThinkingDeltaEvent{Delta: "d"}, ThinkingEndEvent{Thinking: "t", Signature: "s"},
		ToolCallStartEvent{ToolUseID: "c1", Name: "edit"}, ToolInputDeltaEvent{ToolUseID: "c1", Delta: "{"},
		ToolCallEndEvent{Block: call},
		ToolExecutionStartEvent{ToolUseID: "c1", Name: "edit"},
		ToolExecutionUpdateEvent{ToolUseID: "c1", Name: "edit", Chunk: "c"},
		ToolExecutionEndEvent{ToolUseID: "c1", Name: "edit", Result: res.Content},
		ToolResultEvent{Message: res},
		ErrorEvent{Message: "boom", Err: errors.New("boom"), Terminal: true},
	}
}

func TestEventClassMarksDeltasIncrementalUpdatesSnapshotsAndTheRestAuthoritative(t *testing.T) {
	incremental := map[EventType]bool{EvTextDelta: true, EvThinkingDelta: true, EvToolInputDelta: true, EvToolExecutionUpdate: true}
	for _, e := range everyEvent(t) {
		want := ClassAuthoritative
		switch {
		case incremental[e.EventType()]:
			want = ClassIncremental
		case e.EventType() == EvMessageUpdate:
			want = ClassSnapshot
		}
		if e.Class() != want {
			t.Errorf("%T.Class() = %d, want %d", e, e.Class(), want)
		}
	}
}

func TestABoundedStreamDeliversEveryEventKindUnchanged(t *testing.T) {
	s := NewEventStream(StreamOptions{MaxPendingBytes: MinMaxPendingBytes})
	events := everyEvent(t)
	for _, e := range events {
		s.Push(e)
	}
	s.End(StreamResult{})
	var got []Event
	for e := range s.Events() {
		got = append(got, e)
	}
	if !reflect.DeepEqual(got, events) || s.Err() != nil {
		t.Fatalf("delivered %d of %d events unchanged (err %v)", len(got), len(events), s.Err())
	}
}

// Push deep-copies: a producer that keeps mutating its live message after
// pushing an event must not change what the consumer reads.
func TestPushedEventsDoNotAliasTheProducersMessage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(Content) Event
		read  func(Event) Content
	}{
		{"message update", func(c Content) Event { return MessageUpdateEvent{Message: AssistantMessage{Content: c}} },
			func(e Event) Content { return e.(MessageUpdateEvent).Message.Content }},
		{"message start", func(c Content) Event { return MessageStartEvent{Message: AssistantMessage{Content: c}} },
			func(e Event) Content { return e.(MessageStartEvent).Message.Content }},
		{"message end", func(c Content) Event { return MessageEndEvent{Message: AssistantMessage{Content: c}} },
			func(e Event) Content { return e.(MessageEndEvent).Message.Content }},
		{"turn end message", func(c Content) Event { return TurnEndEvent{Message: AssistantMessage{Content: c}} },
			func(e Event) Content { return e.(TurnEndEvent).Message.Content }},
		{"turn end tool result", func(c Content) Event { return TurnEndEvent{ToolResults: []ToolResultMessage{{Content: c}}} },
			func(e Event) Content { return e.(TurnEndEvent).ToolResults[0].Content }},
		{"agent done", func(c Content) Event {
			return AgentDoneEvent{Result: RunResult{Messages: Messages{UserMessage{Content: c}}}}
		},
			func(e Event) Content { return e.(AgentDoneEvent).Result.Messages[0].(UserMessage).Content }},
		{"tool call end", func(c Content) Event { return ToolCallEndEvent{Block: c[1].(ToolUseBlock)} },
			func(e Event) Content { return Content{TextBlock{Text: "partial"}, e.(ToolCallEndEvent).Block} }},
		{"tool execution end", func(c Content) Event { return ToolExecutionEndEvent{Result: c} },
			func(e Event) Content { return e.(ToolExecutionEndEvent).Result }},
		{"tool result", func(c Content) Event { return ToolResultEvent{Message: ToolResultMessage{Content: c}} },
			func(e Event) Content { return e.(ToolResultEvent).Message.Content }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := Content{TextBlock{Text: "partial"}, toolUse(t, "t1", "edit", `{"path":"a.go"}`)}
			want, _ := json.Marshal(live)
			s := NewEventStream(StreamOptions{})
			s.Push(tc.build(live))
			live[1].(ToolUseBlock).Input[2] = 'X'
			live[0] = TextBlock{Text: "partial and then some more"}
			e, ok := s.Next()
			if !ok {
				t.Fatal("no event")
			}
			if got, _ := json.Marshal(tc.read(e)); string(got) != string(want) {
				t.Fatalf("the buffered event saw a later mutation: %s, want %s", got, want)
			}
		})
	}
}
