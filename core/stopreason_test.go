package core

import "testing"

func TestShouldIterateReadsToolCallsNotTheStopReason(t *testing.T) {
	call := toolUse(t, "toolu_1", "search", `{"z":1,"a":2}`)
	for _, c := range []struct {
		name    string
		reason  StopReason
		content Content
		iterate bool
		short   bool
	}{
		{"stop with tool calls", StopReasonStop, Content{call}, true, false},
		{"tool_use with no calls", StopReasonToolUse, Content{TextBlock{Text: "hi"}}, false, false},
		{"stop_sequence with calls", StopReasonStopSequence, Content{call}, true, false},
		{"an unrecognized reason with calls", StopReason("weird"), Content{call}, true, false},
		{"max_tokens with calls", StopReasonLength, Content{TextBlock{}, call, call}, true, false},
		{"refusal without calls", StopReasonRefusal, nil, false, false},
		{"error short-circuits", StopReasonError, Content{call}, true, true},
		{"aborted short-circuits", StopReasonAborted, Content{call}, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := &AssistantMessage{StopReason: c.reason, Content: c.content}
			if got := ShouldIterate(m); got != c.iterate {
				t.Fatalf("ShouldIterate = %v, want %v", got, c.iterate)
			}
			if got := c.reason.ShortCircuits(); got != c.short {
				t.Fatalf("ShortCircuits = %v, want %v", got, c.short)
			}
		})
	}
	if got := ExtractToolUse(&AssistantMessage{Content: Content{call, TextBlock{}, call}}); len(got) != 2 {
		t.Fatalf("ExtractToolUse found %d calls, want 2", len(got))
	}
	if ExtractToolUse(nil) != nil || ShouldIterate(nil) {
		t.Fatal("a nil message has no tool calls")
	}
}

func TestToolChoiceIsSetOnlyWhenChosen(t *testing.T) {
	if ToolChoiceUnset.IsSet() || !ToolChoiceAuto.IsSet() || !ToolChoiceNone.IsSet() {
		t.Fatal("only the zero ToolChoice is unset")
	}
}
