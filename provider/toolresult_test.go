package provider_test

import (
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider"
)

func TestToolResultTextMarksAnErrorInTheText(t *testing.T) {
	text := func(s string, isErr bool) core.ToolResultMessage {
		return core.ToolResultMessage{Content: core.Content{core.TextBlock{Text: s}}, IsError: isErr}
	}
	for _, c := range []struct {
		in   core.ToolResultMessage
		want string
	}{
		{text("ok", false), "ok"},
		{text("no such file", true), "Error: no such file"},
		{text("", true), ""},
	} {
		if got := provider.ToolResultText(c.in); got != c.want {
			t.Errorf("ToolResultText(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}
