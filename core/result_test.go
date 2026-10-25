package core

import "testing"

func TestRunResultFinalTextIsTheLastAssistantMessagesText(t *testing.T) {
	r := RunResult{Messages: Messages{
		UserMessage{Content: Content{TextBlock{Text: "question"}}},
		AssistantMessage{Content: Content{TextBlock{Text: "first"}}},
		AssistantMessage{Content: Content{TextBlock{Text: "fin"}, TextBlock{Text: "al"}}},
		ToolResultMessage{Content: Content{TextBlock{Text: "result"}}},
	}}
	if got := r.FinalText(); got != "final" {
		t.Fatalf("FinalText() = %q, want final", got)
	}
	if got := (RunResult{Messages: Messages{UserMessage{}}}).FinalText(); got != "" {
		t.Fatalf("FinalText() without an assistant message = %q", got)
	}
}
