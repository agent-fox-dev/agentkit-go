package agentkit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/session"
)

// Issue #81 §1: every tool call is audited — including one an interceptor
// blocked, one naming an unknown tool, and one cut off by max_tokens — with
// IsError and the reason code. A denied call is the entry a security reviewer
// most needs.
func TestEveryToolCallIsAuditedWithItsReason(t *testing.T) {
	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "echo", `{"v":"x"}`), toolUse(t, "c2", "nope", `{}`)),
		assistantWithTools(core.StopReasonLength, toolUse(t, "c3", "echo", `{"v":"cut"}`)),
	}}
	got := map[string]core.AuditEvent{}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{echoTool("echo", nil)}
		c.BeforeToolCall = func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
			return core.BeforeToolCallDecision{Block: true, Reason: "denied by policy"}
		}
		c.Hooks.OnAudit = func(e core.AuditEvent) {
			if e.Kind == core.AuditToolCall {
				got[e.ToolUseID] = e
			}
		}
	})
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"c1": core.BlockErrorCode, "c2": "unknown_tool", "c3": "max_tokens"}
	for id, code := range want {
		e, ok := got[id]
		if !ok {
			t.Errorf("call %s has no audit event", id)
			continue
		}
		if !e.IsError || e.ErrorCode != code {
			t.Errorf("call %s audited IsError=%v ErrorCode=%q, want true, %q", id, e.IsError, e.ErrorCode, code)
		}
		if e.ArgumentsHash == "" {
			t.Errorf("call %s audited with no arguments hash", id)
		}
	}
}

// Issue #81 §2: with no resolver, a config whose provider or API differs from
// the log's is refused, not just one whose model id differs.
func TestResumeWithoutAResolverComparesTheWholeTriple(t *testing.T) {
	r := &session.Resume{Provider: "anthropic", API: "anthropic-messages", ModelID: "test-model",
		History: core.NewConversationHistory()}
	cfg := core.AgentConfig{Model: testModel()} // provider "test", api testAPI, id "test-model"
	if _, err := NewAgentFromSession(cfg, r, nil); err == nil {
		t.Fatal("a config on another provider and api was accepted for the log's model")
	}
	r.Provider, r.API = "test", testAPI
	if _, err := NewAgentFromSession(cfg, r, nil); err != nil {
		t.Fatalf("the matching triple was refused: %v", err)
	}
}

// Issue #81 §3: a Resume folded from a branch the store's head no longer
// points at is refused — the model would be sent one conversation and the log
// would record another. session.FoldLeaf forks and folds in one step.
func TestAResumeFromAStaleBranchIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, _, err := session.OpenOrCreate(path, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := session.NewRecorder(store, core.NewConversationHistory(), nil)
	first, _ := rec.RecordMessage(core.UserMessage{Content: core.Content{core.TextBlock{Text: "one"}}})
	for _, txt := range []string{"two", "three", "four"} {
		_, _ = rec.RecordMessage(core.UserMessage{Content: core.Content{core.TextBlock{Text: txt}}})
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, r, err := session.OpenOrCreate(path, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ForkFrom(first); err != nil {
		t.Fatal(err)
	}
	cfg := core.AgentConfig{Model: testModel(), SessionStore: store}
	if _, err := NewAgentFromSession(cfg, r, nil); err == nil || !strings.Contains(err.Error(), "FoldLeaf") {
		t.Fatalf("err = %v; a Resume whose leaf is not the store's head must be refused, naming FoldLeaf", err)
	}

	r2, err := session.FoldLeaf(store, first)
	if err != nil {
		t.Fatal(err)
	}
	if r2.History.Len() != 1 || r2.LeafID != first || store.Head() != first {
		t.Fatalf("FoldLeaf: history %d, leaf %q, head %q; want 1, %q, %q", r2.History.Len(), r2.LeafID, store.Head(), first, first)
	}
	if _, err := NewAgentFromSession(cfg, r2, nil); err != nil {
		t.Fatalf("the folded leaf was refused: %v", err)
	}
}
