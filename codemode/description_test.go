package codemode_test

import (
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/codemode"
	"github.com/agentfox/agentkit-go/core"
)

// TS-08-7: an explicit description is used verbatim.
func TestDescriptionOverride_TS08_7(t *testing.T) {
	const desc = "Explicit override description"
	tl, info, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{Description: desc})
	if err != nil || tl.Description != desc || info.DescriptionBytes != len(desc) {
		t.Fatalf("description = %q (%+v), %v", tl.Description, info, err)
	}
}

// TS-08-8: the generated description states the runtime's rules and
// declares every bound tool.
func TestDescriptionGenerated_TS08_8(t *testing.T) {
	a, b := leaf("tool_alpha"), leaf("tool_beta")
	tl, info, err := codemode.New([]core.Tool{a, b}, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Starlark", "keyword arguments", "parallel(", "call(", "is_error", "ToolError",
		"no file system", "network", "not undone", "print(", "def main()", "result",
		"def tool_alpha(", "def tool_beta(", "the tool_alpha tool",
	} {
		if !strings.Contains(tl.Description, want) {
			t.Errorf("description lacks %q:\n%s", want, tl.Description)
		}
	}
	if info.DescriptionBytes != len(tl.Description) || info.DescriptionBytes == 0 {
		t.Fatalf("BuildInfo = %+v for %d bytes", info, len(tl.Description))
	}
}

// TS-08-9: a custom template replaces the instructions; the tool
// declarations still follow it.
func TestDescriptionCustomTemplate_TS08_9(t *testing.T) {
	tl, _, err := codemode.New([]core.Tool{leaf("tool_alpha")},
		codemode.Options{DescriptionTemplate: "Custom header: {{.ToolCount}} tools in {{.Name}}."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tl.Description, "Custom header: 1 tools in code_mode.") ||
		strings.Contains(tl.Description, "keyword arguments") || !strings.Contains(tl.Description, "def tool_alpha(") {
		t.Fatalf("description = %q", tl.Description)
	}
	if _, _, err := codemode.New([]core.Tool{leaf("a")}, codemode.Options{DescriptionTemplate: "{{.Nope"}); err == nil ||
		!strings.Contains(err.Error(), "codemode: description template") {
		t.Fatalf("bad template err = %v", err)
	}
}
