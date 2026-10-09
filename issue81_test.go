package agentkit

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/session"
)

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
