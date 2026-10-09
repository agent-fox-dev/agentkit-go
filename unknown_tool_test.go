package agentkit

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// unknownToolResult runs one call to a tool that does not exist against an
// agent with the named tools registered, and returns the error text the model
// is given.
func unknownToolResult(t *testing.T, registered []string, called string) string {
	t.Helper()
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", called, `{}`)),
		{Content: core.Content{core.TextBlock{Text: "ok"}}, StopReason: core.StopReasonStop},
	}}
	a := newTestAgent(t, s, nil)
	for _, name := range registered {
		if err := a.RegisterTool(echoTool(name, nil)); err != nil {
			t.Fatal(err)
		}
	}
	res, err := a.Run(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range res.Messages {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.IsError {
			return tr.Content.Text()
		}
	}
	t.Fatal("no error result for the unknown tool")
	return ""
}

// The model that called a tool that is not there is told which ones are, so
// its next call can be right.
func TestUnknownToolListsTheAvailableTools(t *testing.T) {
	got := unknownToolResult(t, []string{"zeta", "alpha", "mid"}, "bash")
	// The text is JSON, so the quotes around the name are escaped in it.
	if !strings.Contains(got, "bash") || !strings.Contains(got, "is available in this run") {
		t.Errorf("the original message is gone: %q", got)
	}
	want := "available tools: alpha, mid, zeta"
	if !strings.Contains(got, want) {
		t.Errorf("result %q does not contain %q (sorted, so the text does not depend on registration order)", got, want)
	}
}

func TestUnknownToolListIsCapped(t *testing.T) {
	var names []string
	for i := 0; i < 60; i++ {
		names = append(names, fmt.Sprintf("tool_%02d", i))
	}
	got := unknownToolResult(t, names, "bash")
	if !strings.Contains(got, "tool_00") || !strings.Contains(got, "tool_49") {
		t.Errorf("the first 50 names should be listed: %q", got)
	}
	if strings.Contains(got, "tool_50") {
		t.Errorf("the 51st name is listed, so the list is not capped: %q", got)
	}
	if !strings.Contains(got, "and 10 more") {
		t.Errorf("the count of the rest is missing: %q", got)
	}
}

func TestUnknownToolWithNoToolsSaysSo(t *testing.T) {
	got := unknownToolResult(t, nil, "bash")
	if !strings.Contains(got, "no tools are available") {
		t.Errorf("result %q should say that no tools are available", got)
	}
}
