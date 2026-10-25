package codemode

import (
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// The generated description states the runtime's rules with the limits the
// tool runs under, and declares every bound tool.
func TestTheGeneratedDescriptionStatesTheRulesAndDeclaresTheTools(t *testing.T) {
	tl, _, err := New([]core.Tool{leaf("tool_alpha"), leaf("tool_beta")}, Options{MaxCalls: 7})
	must(t, err)
	for _, want := range []string{
		"Starlark", "keyword arguments", "parallel(", "call(", "is_error", "ToolError",
		"no file system", "network", "not undone", "print(", "def main()", "result",
		"through the 2 tools declared below", "at most 8 in flight",
		"Limits: 7 tool calls, 100000 execution steps, 30s wall time and 102400 bytes of output",
		"\n\nTools:\n\ndef tool_alpha(", "def tool_beta(", "the tool_alpha tool",
	} {
		if !strings.Contains(tl.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, tl.Description)
		}
	}
}

// An explicit description is used verbatim; a custom template replaces the
// instructions, and the declarations still follow it.
func TestADescriptionCanBeGivenOrTemplated(t *testing.T) {
	tl, _, err := New([]core.Tool{leaf("a")}, Options{Description: "Explicit"})
	if err != nil || tl.Description != "Explicit" {
		t.Fatalf("description = %q, %v", tl.Description, err)
	}
	tl, _, err = New([]core.Tool{leaf("tool_alpha")}, Options{Name: "scripts",
		DescriptionTemplate: "Custom: {{.ToolCount}} tools in {{.Name}}: {{.ToolNames}}."})
	must(t, err)
	if !strings.HasPrefix(tl.Description, "Custom: 1 tools in scripts: [tool_alpha].") ||
		strings.Contains(tl.Description, "keyword arguments") || !strings.Contains(tl.Description, "def tool_alpha(") {
		t.Fatalf("description = %q", tl.Description)
	}
	for _, bad := range []string{"{{.Nope", "{{.Nope}}"} {
		if _, _, err := New(nil, Options{DescriptionTemplate: bad}); err == nil ||
			!strings.Contains(err.Error(), "codemode: description template") {
			t.Fatalf("template %q: err = %v", bad, err)
		}
	}
}
