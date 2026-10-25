package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestMarshalEventWritesTheTypeFirstAndExactlyTheVariantsFields(t *testing.T) {
	want := map[EventType][]string{
		EvAgentStart:          {"session_id", "provider", "api", "model"},
		EvAgentDone:           {"stop_reason", "last_reason", "turn_count", "error", "run_usage", "messages", "usage"},
		EvTurnStart:           {"turn_index"},
		EvTurnEnd:             {"turn_index", "message", "tool_results", "usage"},
		EvMessageStart:        {"message"},
		EvMessageUpdate:       {"message"},
		EvMessageEnd:          {"message"},
		EvTextStart:           {"block_index"},
		EvTextDelta:           {"block_index", "delta"},
		EvTextEnd:             {"block_index", "text"},
		EvThinkingStart:       {"block_index"},
		EvThinkingDelta:       {"block_index", "delta"},
		EvThinkingEnd:         {"block_index", "thinking", "signature", "redacted"},
		EvToolCallStart:       {"block_index", "tool_use_id", "name"},
		EvToolInputDelta:      {"block_index", "tool_use_id", "delta"},
		EvToolCallEnd:         {"block_index", "tool_use_id", "name", "input"},
		EvToolExecutionStart:  {"tool_use_id", "name"},
		EvToolExecutionUpdate: {"tool_use_id", "name", "chunk"},
		EvToolExecutionEnd:    {"tool_use_id", "name", "is_error", "elapsed_ms"},
		EvToolResult:          {"message"},
		EvError:               {"message", "terminal"},
	}
	for _, e := range everyEvent(t) {
		b, err := MarshalEvent(e, stubEncoder)
		if err != nil {
			t.Fatalf("%T: %v", e, err)
		}
		if !strings.HasPrefix(string(b), `{"type":"`+string(e.EventType())+`"`) {
			t.Errorf("%T: the type is not the first member: %s", e, b)
		}
		got := object(t, b)
		keys := want[e.EventType()]
		if len(got) != len(keys)+1 {
			t.Errorf("%T: %d members, want exactly %d: %s", e, len(got), len(keys)+1, b)
		}
		for _, k := range keys {
			if _, ok := got[k]; !ok {
				t.Errorf("%T: missing %q in %s", e, k, b)
			}
		}
	}
}

func TestMarshalEventCarriesThePayloadsAConsumerReads(t *testing.T) {
	u := Usage{InputTokens: 3, BilledModel: "m", Unpriced: true}
	u.SetCost(0.5)
	for _, tc := range []struct {
		e    Event
		want string
	}{
		{ToolCallEndEvent{Block: ToolUseBlock{ID: "1", Name: "n", Input: json.RawMessage(`{"z":1,"a":{"y":2,"b":3}}`)}},
			`{"type":"tool_call_end","block_index":0,"tool_use_id":"1","name":"n","input":{"z":1,"a":{"y":2,"b":3}}}`},
		{ToolCallEndEvent{Block: ToolUseBlock{ID: "1", Name: "n"}},
			`{"type":"tool_call_end","block_index":0,"tool_use_id":"1","name":"n","input":{}}`},
		{ToolExecutionStartEvent{ToolUseID: "c", Name: "n", ParentToolUseID: "p"},
			`{"type":"tool_execution_start","tool_use_id":"c","name":"n","parent_tool_use_id":"p"}`},
		{ToolExecutionUpdateEvent{ToolUseID: "c", Name: "n", Chunk: "x", ParentToolUseID: "p"},
			`{"type":"tool_execution_update","tool_use_id":"c","name":"n","chunk":"x","parent_tool_use_id":"p"}`},
		{ToolExecutionEndEvent{ToolUseID: "c", Name: "n", IsError: true, ElapsedMS: 7, ParentToolUseID: "p"},
			`{"type":"tool_execution_end","tool_use_id":"c","name":"n","is_error":true,"elapsed_ms":7,"parent_tool_use_id":"p"}`},
		{TurnEndEvent{TurnIndex: 2, Usage: u},
			`{"type":"turn_end","turn_index":2,"message":{"role":"assistant"},"tool_results":[],"usage":{"input_tokens":3,` +
				`"output_tokens":0,"reasoning_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"cache_write_1h_tokens":0,` +
				`"total_tokens":0,"cost_usd":0.5,"billed_model":"m","reported":true,"unpriced":true}}`},
		{AgentDoneEvent{Result: RunResult{Messages: Messages{UserMessage{}}, StopReason: RunStopError, LastReason: StopReasonError,
			TurnCount: 2, Error: errors.New("boom")}}, `"stop_reason":"error","last_reason":"error","turn_count":2,"error":"boom"`},
	} {
		b, err := MarshalEvent(tc.e, stubEncoder)
		if err != nil || !strings.Contains(string(b), tc.want) {
			t.Errorf("MarshalEvent(%T) = %s, %v; want it to contain %s", tc.e, b, err, tc.want)
		}
	}
	b, _ := MarshalEvent(AgentDoneEvent{Result: RunResult{Usage: Usage{Unpriced: true}}}, stubEncoder)
	if !strings.Contains(string(b), `"run_usage":{"input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"cache_read_tokens":0,`+
		`"cache_write_tokens":0,"cache_write_1h_tokens":0,"total_tokens":0,"cost_usd":0,"billed_model":"","reported":false,"unpriced":true}`) {
		t.Errorf("an unreported, unpriced run usage = %s", b)
	}
}

func TestMarshalEventFailsWithoutAnEncoderOrOnAnEncoderError(t *testing.T) {
	if _, err := MarshalEvent(TurnStartEvent{}, nil); err == nil {
		t.Fatal("a nil encoder must be an error")
	}
	if _, err := MarshalEvent(nil, stubEncoder); err == nil || !strings.Contains(err.Error(), "unknown event") {
		t.Fatalf("MarshalEvent(nil) = %v, want an unknown-event error", err)
	}
	boom := errors.New("boom")
	failing := func(m Message) (json.RawMessage, error) {
		if m.Role() == RoleToolResult || m.Role() == RoleAssistant {
			return nil, boom
		}
		return json.RawMessage(`{}`), nil
	}
	for _, e := range []Event{
		AgentDoneEvent{Result: RunResult{Messages: Messages{UserMessage{}, AssistantMessage{}}}},
		TurnEndEvent{}, MessageStartEvent{}, MessageUpdateEvent{}, MessageEndEvent{}, ToolResultEvent{},
	} {
		if _, err := MarshalEvent(e, failing); !errors.Is(err, boom) {
			t.Errorf("MarshalEvent(%T) = %v, want the encoder's error", e, err)
		}
	}
	onlyResults := func(m Message) (json.RawMessage, error) {
		if m.Role() == RoleToolResult {
			return nil, boom
		}
		return json.RawMessage(`{}`), nil
	}
	if _, err := MarshalEvent(TurnEndEvent{ToolResults: []ToolResultMessage{{}}}, onlyResults); !errors.Is(err, boom) {
		t.Errorf("a failing tool result encoding = %v, want the encoder's error", err)
	}
}
