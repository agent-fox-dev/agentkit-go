package agentkit

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
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

// TS-09-11: the only first-party providers are anthropic, faux, (since spec 27)
// openrouter and (since spec 28) ollama; the first two are a core.ProviderClient
// built here. TS-28-45.
func TestProviderDefaults_TS09_11(t *testing.T) {
	m, _ := catalog.Lookup("claude-opus-5-5")
	var _ core.ProviderClient = anthropic.Provider(m, anthropic.Options{})
	var _ core.ProviderClient = faux.New()
	entries, err := os.ReadDir("provider")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if strings.Join(dirs, ",") != "anthropic,faux,ollama,openrouter" {
		t.Errorf("provider packages = %v, want [anthropic faux ollama openrouter]", dirs)
	}
	src, err := os.ReadFile("agent.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai", "google", "ollama"} {
		if strings.Contains(strings.ToLower(string(src)), name) {
			t.Errorf("agent.go mentions %s", name)
		}
	}
}

// TS-09-12: nested calls through a wrapper and a top-level batch both run
// against the real read_file tool; nested execution events carry their
// parent, the batch yields one result per call, and the dispatch code holds
// no audit or tracer hooks.
func TestBatchAndNestedWithoutHooks_TS09_12(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"test.txt", "a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(ws.Root(), f), []byte("hello "+f+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	all, err := tools.For(ws, tools.Options{})
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
	a := newTestAgent(t, s, func(c *Config) {
		c.Toolset.Tools = []core.Tool{parent, readFile}
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
	assertAbsent(t, "provider/openai", "provider/openairesponses", "provider/google",
		"plugins", "skills", "_skills/code-review", "cmd/validate-plugins", "session", "subagent",
		"difftest", "imagex", "compaction", "middleware", "stop")
	// provider/ollama left this list when spec 28 gave the repository a new
	// provider under that name; the
	// hand-written NDJSON one PRD 9 deleted is still gone.
	// provider/repair.go and provider/repair_test.go left this list when spec 27
	// gave package provider a new, shared tool-pairing repair under those names
	// (docs/adr/03-follow-the-vendor-sdks-and-wires-where-the-specs-disagree.md); the multi-wire repair they once
	// held is still gone.
	assertAbsent(t, "provider/credentials.go", "provider/credentials_test.go", "provider/salvage.go",
		"provider/salvage_test.go",
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

// TS-09-18: the wall-clock budget file is gone, and the concurrency test no
// longer compares elapsed durations against thresholds.
func TestNoWallClockThresholds_TS09_18(t *testing.T) {
	assertAbsent(t, "perf_budget_test.go")
	src, err := os.ReadFile(filepath.Join("tools", "concurrency_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, cmp := range []string{"writeElapsed >", "editElapsed >", "markElapsed >"} {
		if strings.Contains(string(src), cmp) {
			t.Errorf("tools/concurrency_test.go still compares %q against a threshold", cmp)
		}
	}
}

// TS-09-19: the Makefile names no deleted module and has no build-examples
// target.
func TestMakefileTargets_TS09_19(t *testing.T) {
	src, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"build-examples:", "difftest", "examples/codesearch"} {
		if strings.Contains(string(src), gone) {
			t.Errorf("Makefile still contains %q", gone)
		}
	}
}

// requiredModules26 is the root module's require set: the module paths of every
// require line, direct and indirect, without versions. It was spec 26's, which
// named no OpenRouter or Ollama SDK; spec 27 added the OpenRouter SDK and the
// ajson module it requires, and spec 28 added the Ollama module and the
// modules `go mod tidy` then requires for it (docs/DEPS.md).
var requiredModules26 = []string{
	"github.com/OpenRouterTeam/go-sdk",
	"github.com/spyzhov/ajson",
	"github.com/ollama/ollama",
	"github.com/google/uuid",
	"github.com/kr/pretty",
	"github.com/mailru/easyjson",
	"github.com/rogpeppe/go-internal",
	"github.com/wk8/go-ordered-map/v2",
	"gopkg.in/check.v1",
	"gopkg.in/yaml.v3",
	"github.com/anthropics/anthropic-sdk-go",
	"github.com/aws/aws-sdk-go-v2",
	"github.com/aws/aws-sdk-go-v2/config",
	"github.com/aws/aws-sdk-go-v2/credentials",
	"github.com/bmatcuk/doublestar/v4",
	"github.com/modelcontextprotocol/go-sdk",
	"github.com/pelletier/go-toml/v2",
	"github.com/tree-sitter-grammars/tree-sitter-kotlin",
	"github.com/tree-sitter-grammars/tree-sitter-lua",
	"github.com/tree-sitter/go-tree-sitter",
	"github.com/tree-sitter/tree-sitter-bash",
	"github.com/tree-sitter/tree-sitter-c",
	"github.com/tree-sitter/tree-sitter-c-sharp",
	"github.com/tree-sitter/tree-sitter-cpp",
	"github.com/tree-sitter/tree-sitter-java",
	"github.com/tree-sitter/tree-sitter-javascript",
	"github.com/tree-sitter/tree-sitter-php",
	"github.com/tree-sitter/tree-sitter-python",
	"github.com/tree-sitter/tree-sitter-ruby",
	"github.com/tree-sitter/tree-sitter-rust",
	"github.com/tree-sitter/tree-sitter-scala",
	"github.com/tree-sitter/tree-sitter-typescript",
	"go.starlark.net",
	"golang.org/x/oauth2",
	"cloud.google.com/go/auth",
	"cloud.google.com/go/auth/oauth2adapt",
	"cloud.google.com/go/compute/metadata",
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream",
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds",
	"github.com/aws/aws-sdk-go-v2/internal/configsources",
	"github.com/aws/aws-sdk-go-v2/internal/endpoints/v2",
	"github.com/aws/aws-sdk-go-v2/internal/ini",
	"github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding",
	"github.com/aws/aws-sdk-go-v2/service/internal/presigned-url",
	"github.com/aws/aws-sdk-go-v2/service/sso",
	"github.com/aws/aws-sdk-go-v2/service/ssooidc",
	"github.com/aws/aws-sdk-go-v2/service/sts",
	"github.com/aws/smithy-go",
	"github.com/bahlo/generic-list-go",
	"github.com/buger/jsonparser",
	"github.com/felixge/httpsnoop",
	"github.com/go-logr/logr",
	"github.com/go-logr/stdr",
	"github.com/golang/groupcache",
	"github.com/golang/protobuf",
	"github.com/google/jsonschema-go",
	"github.com/google/s2a-go",
	"github.com/googleapis/enterprise-certificate-proxy",
	"github.com/invopop/jsonschema",
	"github.com/mattn/go-pointer",
	"github.com/pb33f/ordered-map/v2",
	"github.com/segmentio/asm",
	"github.com/segmentio/encoding",
	"github.com/standard-webhooks/standard-webhooks/libraries",
	"github.com/tidwall/gjson",
	"github.com/tidwall/match",
	"github.com/tidwall/pretty",
	"github.com/tidwall/sjson",
	"github.com/yosida95/uritemplate/v3",
	"go.opencensus.io",
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc",
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp",
	"go.opentelemetry.io/otel",
	"go.opentelemetry.io/otel/metric",
	"go.opentelemetry.io/otel/trace",
	"go.yaml.in/yaml/v4",
	"golang.org/x/crypto",
	"golang.org/x/net",
	"golang.org/x/sync",
	"golang.org/x/sys",
	"golang.org/x/text",
	"golang.org/x/time",
	"google.golang.org/api",
	"google.golang.org/genproto/googleapis/rpc",
	"google.golang.org/grpc",
	"google.golang.org/protobuf",
}

// TS-26-35: the model ids of the new providers are written only in package
// catalog, the driver still resolves its model through the catalog (since spec
// 29 through Select in resolve.go, which reaches Lookup for an Anthropic id;
// agent.go calls neither), and go.mod requires no module that spec 26 did not
// find.
func TestNewProviderModelsStayInCatalog_TS26_35(t *testing.T) {
	ids := []string{"qwen/qwen3.8-flash", "qwen3.8:27b"}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "testdata" || path == "catalog") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if strings.Contains(string(b), id) {
				t.Errorf("%s names the model id %s; model ids live in the catalog", path, id)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	src, err := os.ReadFile("resolve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "catalog.Select(cfg.Provider, cfg.Model, cfg.Effort)") {
		t.Error("resolve.go no longer resolves the model with catalog.Select(cfg.Provider, cfg.Model, cfg.Effort)")
	}
	if agent, err := os.ReadFile("agent.go"); err != nil || strings.Contains(string(agent), "catalog.Lookup") {
		t.Errorf("agent.go calls catalog.Lookup (%v); the model is resolved in resolve.go", err)
	}

	_, requires, _ := modDirectives(t, "go.mod")
	var got []string
	for _, r := range requires {
		got = append(got, strings.Fields(r)[0])
	}
	slices.Sort(got)
	want := slices.Sorted(slices.Values(requiredModules26))
	if !slices.Equal(got, want) {
		for _, m := range got {
			if !slices.Contains(want, m) {
				t.Errorf("go.mod requires %s, which the pinned list does not name", m)
			}
		}
		for _, m := range want {
			if !slices.Contains(got, m) {
				t.Errorf("go.mod no longer requires %s", m)
			}
		}
	}
}
