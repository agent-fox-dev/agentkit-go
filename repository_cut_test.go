package agentkit

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/tools"
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

// assertAbsent fails for every path that exists, relative to the repository
// root (the root package's own directory).
func assertAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s still exists", p)
		} else if !os.IsNotExist(err) {
			t.Errorf("stat %s: %v", p, err)
		}
	}
}

// TS-09-1: the provider backends, the multi-wire support files and the
// auxiliary packages are gone.
func TestDeletedPackagesAbsent_TS09_1(t *testing.T) {
	assertAbsent(t, "provider/openai", "provider/openairesponses", "provider/google", "provider/ollama",
		"plugins", "skills", "_skills/code-review", "cmd/validate-plugins", "session", "subagent",
		"difftest", "imagex", "compaction", "middleware", "stop")
	assertAbsent(t, "provider/credentials.go", "provider/credentials_test.go", "provider/salvage.go",
		"provider/salvage_test.go", "provider/repair.go", "provider/repair_test.go",
		"provider/asymmetry_test.go", "provider/conformance_test.go", "provider/fuzz_test.go")
}

// TS-09-2: the two kept providers and the five kept examples are still
// there; every other example is gone.
func TestRetainedProvidersAndExamples_TS09_2(t *testing.T) {
	for _, p := range []string{"provider/anthropic/anthropic.go", "provider/faux/faux.go",
		"examples/agentdemo", "examples/codingagent", "examples/customtools", "examples/codemode", "examples/mcp"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	assertAbsent(t, "examples/branching", "examples/chat", "examples/cleaner", "examples/codesearch",
		"examples/compaction", "examples/deferred", "examples/delegation", "examples/flatline",
		"examples/images", "examples/interactive", "examples/mcpserver", "examples/middleware",
		"examples/observability", "examples/plugins", "examples/session", "examples/skills",
		"examples/streaming", "examples/testing", "examples/tools", "examples/triage")
}

// TS-09-3: the deleted tool, MCP server and root agent files are gone.
func TestObsoleteRootAndToolFilesAbsent_TS09_3(t *testing.T) {
	assertAbsent(t, "tools/fetch.go", "tools/ssrf.go", "tools/ssrf_test.go", "tools/powershell.go",
		"mcp/server.go", "mcp/serve.go", "mcp/server_test.go", "wire/bind.go",
		"images.go", "images_test.go", "skillsconfig.go", "skillsconfig_test.go", "audit.go",
		"audit_test.go", "deferred.go", "deferred_test.go", "resume.go", "resume_test.go",
		"resume_metadata_test.go")
}

// TS-09-4: the deleted core declarations and the root tests of deleted
// subsystems are gone.
func TestDeletedCoreAndSubsystemTestsAbsent_TS09_4(t *testing.T) {
	assertAbsent(t, "core/audit.go", "core/audit_test.go", "core/trace.go", "core/plugin.go",
		"plugin_wiring_test.go", "middleware_wiring_test.go", "events_test.go", "compaction_usage_test.go")
}

// modDirectives reads a go.mod and returns its module path, its require
// lines as "path version", and its replace lines as "old => new". It handles
// both the single-line and the block forms, which is all a go.mod uses.
func modDirectives(t *testing.T, path string) (module string, requires, replaces []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	block := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case line == ")":
			block = ""
		case strings.HasSuffix(line, "("):
			block = strings.TrimSpace(strings.TrimSuffix(line, "("))
		case block == "require":
			requires = append(requires, strings.Join(strings.Fields(line), " "))
		case block == "replace":
			replaces = append(replaces, strings.Join(strings.Fields(line), " "))
		case strings.HasPrefix(line, "module "):
			module = strings.TrimSpace(strings.TrimPrefix(line, "module "))
		case strings.HasPrefix(line, "require "):
			requires = append(requires, strings.Join(strings.Fields(strings.TrimPrefix(line, "require ")), " "))
		case strings.HasPrefix(line, "replace "):
			replaces = append(replaces, strings.Join(strings.Fields(strings.TrimPrefix(line, "replace ")), " "))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return module, requires, replaces
}

// TS-09-13: the root and codesearch go.mod files declare the canonical
// module paths, and codesearch requires and replaces the root by it.
func TestCanonicalModulePaths_TS09_13(t *testing.T) {
	const root = "github.com/agent-fox-dev/agentkit-go"
	if m, _, _ := modDirectives(t, "go.mod"); m != root {
		t.Errorf("go.mod module = %q, want %q", m, root)
	}
	m, reqs, reps := modDirectives(t, filepath.Join("codesearch", "go.mod"))
	if m != root+"/codesearch" {
		t.Errorf("codesearch/go.mod module = %q, want %q", m, root+"/codesearch")
	}
	var foundReq, foundRep bool
	for _, r := range reqs {
		foundReq = foundReq || r == root+" v0.0.0"
	}
	for _, r := range reps {
		foundRep = foundRep || r == root+" => .."
	}
	if !foundReq {
		t.Errorf("codesearch/go.mod does not require %s v0.0.0: %v", root, reqs)
	}
	if !foundRep {
		t.Errorf("codesearch/go.mod does not replace %s => ..: %v", root, reps)
	}
}

// TS-09-14 (property): no Go file anywhere in the repository imports the
// legacy module path.
func TestNoLegacyImportPaths_TS09_14(t *testing.T) {
	legacy := `"github.com/` + `agentfox/agentkit-go` // split so this file does not match itself
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), legacy) {
			t.Errorf("%s still references the legacy module path", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
