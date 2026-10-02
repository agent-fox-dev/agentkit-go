package tools_test

import (
	"testing"

	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-01-43: CtagsRunner's return value is assignable to outline.Options.Runner.
func TestCtagsRunner_AssignableToRunner_TS_01_43(t *testing.T) {
	// This compiles only if the types match.
	_ = outline.Options{Runner: tools.CtagsRunner(nil)}
}
