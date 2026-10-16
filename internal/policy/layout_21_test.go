package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TS-21-55: the exported-API golden records Toolset with its five fields,
// NewRunner, Runner with Call, Batch, Tools and Reachable, Agent.Runner and
// the twelve-field Config. 21-REQ-10.1. (TS-13-67 compares the current API to
// the golden.)
func TestAPIGoldenRecordsTheRunner_TS21_55(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "policy", "testdata", "exported-api.golden"))
	if err != nil {
		t.Fatal(err)
	}
	root := ""
	for _, section := range strings.Split(string(raw), "== ") {
		if name, body, ok := strings.Cut(section, "\n"); ok && name == "." {
			root = body
		}
	}
	if root == "" {
		t.Fatal("the golden has no section for the root package")
	}
	for _, c := range []struct {
		line string
		want bool
	}{
		{"type Toolset struct { Tools []core.Tool Policy core.ToolPolicy Guard core.BeforeToolCall After core.AfterToolCall Events func(core.Event) }", true},
		{"func NewRunner(ts Toolset) (*Runner, error)", true},
		{"type Runner struct { }", true},
		{"func (r *Runner) Call(ctx context.Context, name string, input json.RawMessage) core.ToolResult", true},
		{"func (r *Runner) Batch(ctx context.Context, calls ...core.ToolUseBlock) []core.ToolResult", true},
		{"func (r *Runner) Tools() []core.Tool", true},
		{"func (r *Runner) Reachable() []core.Tool", true},
		{"func (a *Agent) Runner() *Runner", true},
		{"Prefix []core.Message Toolset Toolset MaxTurns int MaxCostUSD float64 Timeout time.Duration Prune PruneOptions MaxTokens int }", true},
		{"Tools []core.Tool Policy core.ToolPolicy Guard core.BeforeToolCall After core.AfterToolCall MaxTurns", false},
	} {
		if got := strings.Contains(root, c.line); got != c.want {
			t.Errorf("the golden's root section contains %q = %v, want %v", c.line, got, c.want)
		}
	}
}
