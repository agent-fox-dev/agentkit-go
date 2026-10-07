package agentkit

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/schema"
	"github.com/agentfox/agentkit-go/stop"
)

// TS-04-53: NewAgentFromSession restores tool-result metadata into the resumed
// agent's history.
func TestResumeRestoresToolResultMetadata_TS04_53(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")

	two := 2
	wantMD := &core.ToolMetadata{ExitCode: &two, Outcome: "exit", TotalBytes: 5}

	// ---- Process 1: run an agent with a probe tool that returns metadata.
	store1, _ := openTestSession(t, path)
	probeTool := core.Tool{
		Name: "probe", Description: "probe", InputSchema: schema.Object(),
		Execute: func(ctx context.Context, in json.RawMessage) core.ToolResult {
			return core.ToolResult{
				OK:       false,
				Error:    "command_exit",
				Text:     "boom\n[exit 2]",
				Metadata: wantMD.Clone(),
			}
		},
	}

	s1 := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "c1", "probe", `{}`)),
		{Content: core.Content{core.TextBlock{Text: "done"}}, StopReason: core.StopReasonStop},
	}}
	a1 := newTestAgent(t, s1, func(c *core.AgentConfig) {
		c.SessionStore = store1
	})
	if err := a1.RegisterTool(probeTool); err != nil {
		t.Fatal(err)
	}
	if _, err := a1.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	// Verify metadata is in history before close.
	var beforeMD *core.ToolMetadata
	for _, m := range a1.History().Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			beforeMD = tr.Metadata
		}
	}
	if beforeMD == nil {
		t.Fatal("before close: tool result has nil Metadata")
	}
	if beforeMD.ExitCode == nil || *beforeMD.ExitCode != 2 {
		t.Fatalf("before close: ExitCode = %v, want 2", beforeMD.ExitCode)
	}

	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// ---- Process 2: reopen and resume.
	store2, resume := openTestSession(t, path)
	defer store2.Close()

	s2 := &scripted{turns: []core.AssistantMessage{
		{Content: core.Content{core.TextBlock{Text: "resumed"}}, StopReason: core.StopReasonStop},
	}}
	cfg := core.AgentConfig{
		Model:        testModel(),
		StopPolicy:   stop.AfterTurns(5),
		Providers:    core.ProviderRegistry{testAPI: s2.provider()},
		SessionStore: store2,
	}
	a2, err := NewAgentFromSession(cfg, resume,
		func(provider string, api core.API, modelID string) (*core.Model, error) {
			return testModel(), nil
		})
	if err != nil {
		t.Fatalf("NewAgentFromSession: %v", err)
	}

	// Check the resumed agent's history.
	var afterMD *core.ToolMetadata
	for _, m := range a2.History().Messages() {
		if tr, ok := m.(core.ToolResultMessage); ok && tr.ToolUseID == "c1" {
			afterMD = tr.Metadata
		}
	}
	if afterMD == nil {
		t.Fatal("after resume: tool result has nil Metadata")
	}
	if afterMD.ExitCode == nil || *afterMD.ExitCode != 2 {
		t.Fatalf("after resume: ExitCode = %v, want 2", afterMD.ExitCode)
	}
	if afterMD.Outcome != "exit" {
		t.Fatalf("after resume: Outcome = %q, want %q", afterMD.Outcome, "exit")
	}
	if afterMD.TotalBytes != 5 {
		t.Fatalf("after resume: TotalBytes = %d, want 5", afterMD.TotalBytes)
	}
}
