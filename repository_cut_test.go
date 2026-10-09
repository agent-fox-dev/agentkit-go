package agentkit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/provider/faux"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-09-10: the Agent carries no session recorder, middleware chain or cache
// meter, has no Continue or CacheStats method, and the loop no longer asks
// compaction for a context estimate.
func TestAgentDecoupled_TS09_10(t *testing.T) {
	agentType := reflect.TypeOf(Agent{})
	for i := 0; i < agentType.NumField(); i++ {
		f := agentType.Field(i)
		if f.Name == "rec" || f.Name == "meter" {
			t.Errorf("Agent still has field %q (%s)", f.Name, f.Type)
		}
		for _, banned := range []string{"session.Recorder", "middleware.Chain", "middleware.CacheMeter"} {
			if strings.Contains(f.Type.String(), banned) {
				t.Errorf("Agent field %q has type %s", f.Name, f.Type)
			}
		}
	}
	ptr := reflect.TypeOf(&Agent{})
	for _, m := range []string{"Continue", "CacheStats", "Meter"} {
		if _, ok := ptr.MethodByName(m); ok {
			t.Errorf("Agent still has method %s", m)
		}
	}
	src, err := os.ReadFile("loop.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "compaction.EstimateContextTokens") {
		t.Error("loop.go still calls compaction.EstimateContextTokens")
	}
}

// TS-09-11: the registry holds exactly the providers registered into it, and
// the only first-party ones left are anthropic and faux.
func TestProviderDefaults_TS09_11(t *testing.T) {
	cfg := core.AgentConfig{}
	RegisterDefaults(&cfg, anthropic.Provider(anthropic.Options{}), faux.New().APIProvider())
	if _, ok := cfg.Providers.Get(anthropic.API); !ok {
		t.Error("anthropic is not registered")
	}
	if _, ok := cfg.Providers.Get(faux.API); !ok {
		t.Error("faux is not registered")
	}
	for _, api := range []core.API{"openai", "openai-completions", "openai-responses", "google", "google-generative-ai", "ollama"} {
		if _, ok := cfg.Providers.Get(api); ok {
			t.Errorf("legacy provider %q is registered", api)
		}
	}
	if len(DefaultProviders()) != 0 {
		t.Errorf("DefaultProviders registers %d providers, want none", len(DefaultProviders()))
	}
	src, err := os.ReadFile("providers.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai", "google", "ollama"} {
		if strings.Contains(strings.ToLower(string(src)), name) {
			t.Errorf("providers.go mentions %s", name)
		}
	}
}

// TS-09-12: nested calls through a wrapper and a top-level batch both run
// against the real read_file tool; nested execution events carry their
// parent, the batch yields one result per call, and the dispatch code holds
// no audit or tracer hooks.
func TestBatchAndNestedWithoutHooks_TS09_12(t *testing.T) {
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"test.txt", "a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(ws.Root, f), []byte("hello "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	var readFile core.Tool
	for _, tl := range all {
		if tl.Name == "read_file" {
			readFile = tl
		}
	}
	var nested []core.ToolResult
	parent := wrapperTool("parent", func(ctx context.Context) core.ToolResult {
		res, err := core.CallNested(ctx, core.ToolUseBlock{Name: "read_file", Input: json.RawMessage(`{"path":"test.txt"}`)})
		if err != nil {
			return core.ErrResult("nested_failed", err.Error())
		}
		nested = res
		return core.OKResult(nil)
	}, readFile)

	s := &scripted{turns: []core.AssistantMessage{
		assistantWithTools(core.StopReasonToolUse, toolUse(t, "call_parent_999", "parent", `{}`)),
		assistantWithTools(core.StopReasonToolUse,
			toolUse(t, "c1", "read_file", `{"path":"a.txt"}`),
			toolUse(t, "c2", "read_file", `{"path":"b.txt"}`)),
	}}
	a := newTestAgent(t, s, func(c *core.AgentConfig) {
		c.ToolPolicy.CustomTools = []core.Tool{parent, readFile}
	})
	st, err := a.Stream(context.Background(), "go")
	if err != nil {
		t.Fatal(err)
	}
	var foundStart bool
	batchResults := map[string]core.ToolResultMessage{}
	for e := range st.Events() {
		switch ev := e.(type) {
		case core.ToolExecutionStartEvent:
			if ev.Name == "read_file" && ev.ParentToolUseID != "" {
				if ev.ParentToolUseID != "call_parent_999" {
					t.Errorf("nested start parent = %q", ev.ParentToolUseID)
				}
				foundStart = true
			}
		case core.ToolResultEvent:
			if ev.Message.ToolName == "read_file" {
				batchResults[ev.Message.ToolUseID] = ev.Message
			}
		}
	}
	if _, err := st.RunResult(); err != nil {
		t.Fatal(err)
	}
	if !foundStart {
		t.Error("no nested ToolExecutionStartEvent carried the parent id")
	}
	if len(nested) != 1 || !nested[0].OK {
		t.Fatalf("nested read_file = %+v", nested)
	}
	if len(batchResults) != 2 {
		t.Fatalf("batch produced %d read_file results, want 2", len(batchResults))
	}
	for id, m := range batchResults {
		if m.IsError {
			t.Errorf("batch result %s is an error: %+v", id, m)
		}
	}

	for _, f := range []string{"batch.go", "nested.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, hook := range []string{"a.audit(", ".audit(", "core.AuditEvent", "Tracer", "StartSpan", "pluginVeto"} {
			if strings.Contains(string(src), hook) {
				t.Errorf("%s still contains %q", f, hook)
			}
		}
	}
}
