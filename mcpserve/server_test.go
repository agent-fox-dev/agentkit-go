package mcpserve

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/wire"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

const (
	rootPath = "github.com/agent-fox-dev/agentkit-go"
	sdkPath  = "github.com/modelcontextprotocol/go-sdk"
)

// connectPair joins an AgentKit MCP client to s over in-process pipes, as a
// stdio pair would be joined, and returns the client and the server's session.
// Both ends close with the test.
func connectPair(t *testing.T, s *Server) (*agentmcp.ServerConnection, *session) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	toClient, fromServer := io.Pipe()
	toServer, fromClient := io.Pipe()
	ss, err := s.open(ctx, agentmcp.NewPipeTransport(toServer, fromServer, wire.Defaults()))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	conn, err := agentmcp.Connect(ctx, agentmcp.ServerConfig{Name: "test"},
		agentmcp.NewPipeTransport(toClient, fromClient, wire.Defaults()), agentmcp.ConnectionOptions{})
	if err != nil {
		cancel()
		_ = ss.close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		_ = ss.close()
		cancel()
	})
	return conn, ss
}

// connect is connectPair for a test that does not need the session.
func connect(t *testing.T, s *Server) *agentmcp.ServerConnection {
	t.Helper()
	conn, _ := connectPair(t, s)
	return conn
}

// runnerOf builds a runner over ts with no guard; the tools must reach no shell.
func runnerOf(t *testing.T, ts ...core.Tool) *agentkit.Runner {
	t.Helper()
	r, err := agentkit.NewRunner(agentkit.Toolset{Tools: ts})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// textTool returns a tool that answers every call with its own name.
func textTool(name, desc string, in *schema.Schema) core.Tool {
	return core.Tool{Name: name, Description: desc, InputSchema: in,
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.OKResult(map[string]any{"name": name})
		}}
}

// realRunner is a runner over the built-in tools of a workspace and a
// code_mode tool that binds them, under a guard that allows everything: the
// toolset the spec's integration tests serve.
func realRunner(t *testing.T) *agentkit.Runner {
	t.Helper()
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cm, _, err := codemode.New(all, codemode.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := agentkit.NewRunner(agentkit.Toolset{Tools: append([]core.Tool{cm}, all...), Guard: guard.AllowAll})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// jsonOf decodes b, or the JSON v marshals to, into a value comparable with
// reflect.DeepEqual.
func jsonOf(t *testing.T, v any) any {
	t.Helper()
	b, ok := v.([]byte)
	if !ok {
		var err error
		if b, err = json.Marshal(v); err != nil {
			t.Fatal(err)
		}
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return out
}

// TS-23-3: the module holds one package, mcpserve, with no main package and no
// cmd directory, and its non-test files import only the standard library, the
// root packages agentkit, core, mcp and wire, and the SDK's mcp and jsonrpc.
// 23-REQ-1.3.
func TestModuleLayout_TS23_3(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{.ImportPath}} {{.Name}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if got, want := strings.TrimSpace(string(out)), selfPath+" mcpserve"; got != want {
		t.Errorf("go list ./... = %q, want exactly %q", got, want)
	}
	if _, err := os.Stat("cmd"); err == nil {
		t.Error("mcpserve/cmd exists: the module has no command")
	}
	allowed := []string{rootPath, rootPath + "/core", rootPath + "/mcp", rootPath + "/wire",
		sdkPath + "/mcp", sdkPath + "/jsonrpc"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Name.Name == "main" {
			t.Errorf("%s is in package main", f)
		}
		for _, imp := range parsed.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			stdlib := !strings.Contains(strings.SplitN(p, "/", 2)[0], ".")
			if !stdlib && !slices.Contains(allowed, p) {
				t.Errorf("%s imports %s, which is neither the standard library nor on the allowlist", f, p)
			}
		}
	}
}

// TS-23-5: New with a nil runner returns nil and the exact refusal.
// 23-REQ-2.1.
func TestNewRefusesANilRunner_TS23_5(t *testing.T) {
	s, err := New(nil, Options{})
	if s != nil {
		t.Errorf("New(nil) = %v, want nil", s)
	}
	if err == nil || err.Error() != "mcpserve: Runner is nil" {
		t.Errorf("error = %v, want %q", err, "mcpserve: Runner is nil")
	}
}

// TS-23-6 (integration): tools/list returns exactly the runner's tools, with
// verbatim names and descriptions and JSON-equal input schemas, and declares no
// output schema, annotations or _meta. 23-REQ-2.2.
func TestListsExactlyTheRunnersTools_TS23_6(t *testing.T) {
	r := realRunner(t)
	s, err := New(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := connect(t, s).ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := r.Tools()
	if len(listed) != len(want) || len(want) < 2 {
		t.Fatalf("listed %d tools, the runner has %d", len(listed), len(want))
	}
	byName := map[string]int{}
	for i, l := range listed {
		byName[l.Name] = i
	}
	for _, tl := range want {
		i, ok := byName[tl.Name]
		if !ok {
			t.Errorf("%s is not listed", tl.Name)
			continue
		}
		l := listed[i]
		if l.Description != tl.Description {
			t.Errorf("%s: description %q, want %q", tl.Name, l.Description, tl.Description)
		}
		if !reflect.DeepEqual(jsonOf(t, l.InputSchema), jsonOf(t, tl.InputSchema)) {
			t.Errorf("%s: inputSchema %v, want %v", tl.Name, jsonOf(t, l.InputSchema), jsonOf(t, tl.InputSchema))
		}
		if l.OutputSchema != nil || l.Annotations != nil || l.Meta != nil {
			t.Errorf("%s declares an output schema, annotations or _meta: %+v", tl.Name, l)
		}
	}
}

// TS-23-7 (integration): a tool with a nil InputSchema is listed with
// {"type":"object"}. 23-REQ-2.3.
func TestNilSchemaIsServedAsAnObject_TS23_7(t *testing.T) {
	s, err := New(runnerOf(t, textTool("bare", "no schema", nil)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := connect(t, s).ListTools(context.Background())
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListTools = %v, %v", listed, err)
	}
	if got, want := jsonOf(t, listed[0].InputSchema), jsonOf(t, []byte(`{"type":"object"}`)); !reflect.DeepEqual(got, want) {
		t.Errorf("inputSchema = %v, want %v", got, want)
	}
}

// TS-23-8: New refuses a tool whose schema cannot be served, naming the tool,
// and does not panic. 23-REQ-2.4.
func TestNewRefusesAnUnservableSchema_TS23_8(t *testing.T) {
	for name, in := range map[string]*schema.Schema{
		"does not marshal": {Type: schema.TypeObject, Extra: schema.Extra{{Key: "x", Value: json.RawMessage("{")}}},
		"has no type":      {},
		"is an array":      schema.Array(schema.String()),
		"is a string":      schema.String(),
		"is nullable":      {Type: schema.TypeObject, Nullable: true},
	} {
		s, err := New(runnerOf(t, textTool("bad", "x", in)), Options{})
		if s != nil {
			t.Errorf("a schema that %s: New returned a server", name)
		}
		if err == nil || !strings.HasPrefix(err.Error(), `mcpserve: tool "bad": input schema`) {
			t.Errorf("a schema that %s: error = %v, want one starting %q", name, err, `mcpserve: tool "bad": input schema`)
		}
	}
}

// snapshotRunner is a toolRunner whose tool slice a test can change and whose
// Tools calls it counts.
type snapshotRunner struct {
	tools []core.Tool
	calls int
}

func (r *snapshotRunner) Tools() []core.Tool { r.calls++; return r.tools }
func (r *snapshotRunner) Call(context.Context, string, json.RawMessage) core.ToolResult {
	return core.OKResult(nil)
}

// TS-23-9: the tool set is a snapshot taken once at New. 23-REQ-2.5.
func TestToolSetIsASnapshot_TS23_9(t *testing.T) {
	r := &snapshotRunner{tools: []core.Tool{
		textTool("one", "first", schema.Object()), textTool("two", "second", schema.Object())}}
	s, err := newServer(r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	conn := connect(t, s)
	r.tools[0].Description = "changed"
	r.tools = r.tools[:0]
	listed, err := conn.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, l := range listed {
		got = append(got, l.Name+": "+l.Description)
	}
	slices.Sort(got)
	if want := []string{"one: first", "two: second"}; !slices.Equal(got, want) {
		t.Errorf("listed %v, want the original %v", got, want)
	}
	if r.calls != 1 {
		t.Errorf("Tools was called %d times, want 1", r.calls)
	}
}

// TS-23-10 (integration): initialize reports the configured or the default
// server info, and the instructions. 23-REQ-2.6.
func TestInitializeReportsServerInfo_TS23_10(t *testing.T) {
	info := func(o Options) (name, version, instructions string) {
		s, err := New(runnerOf(t), o)
		if err != nil {
			t.Fatal(err)
		}
		sess, err := connect(t, s).Session(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		res := sess.InitializeResult()
		return res.ServerInfo.Name, res.ServerInfo.Version, res.Instructions
	}
	if n, v, i := info(Options{Name: "n", Version: "v", Instructions: "i"}); n != "n" || v != "v" || i != "i" {
		t.Errorf("configured: %q %q %q, want n v i", n, v, i)
	}
	n, v, i := info(Options{})
	if n != "agentkit" || i != "" {
		t.Errorf("default: name %q, instructions %q, want agentkit and none", n, i)
	}
	want := "(devel)"
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Path == selfPath && bi.Main.Version != "" {
		want = bi.Main.Version
	}
	if v != want {
		t.Errorf("default version %q, want %q", v, want)
	}
}

// TS-23-10, the algorithm: the version is the main module's when the binary is
// built from mcpserve, otherwise the dependency's (its replacement's, when
// replaced), and (devel) when there is none.
func TestVersionFromBuildInfo_TS23_10(t *testing.T) {
	dep := func(v string, repl *debug.Module) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "example.test/app"},
			Deps: []*debug.Module{{Path: "example.test/other", Version: "v9"}, {Path: selfPath, Version: v, Replace: repl}}}
	}
	for name, c := range map[string]struct {
		bi   *debug.BuildInfo
		want string
	}{
		"main module":     {&debug.BuildInfo{Main: debug.Module{Path: selfPath, Version: "v1.2.3"}}, "v1.2.3"},
		"dependency":      {dep("v0.4.0", nil), "v0.4.0"},
		"replaced":        {dep("v0.0.0", &debug.Module{Path: "../mcpserve", Version: "v0.5.0"}), "v0.5.0"},
		"replaced, local": {dep("v0.0.0", &debug.Module{Path: "../mcpserve"}), "(devel)"},
		"absent":          {&debug.BuildInfo{Main: debug.Module{Path: "example.test/app"}}, "(devel)"},
		"main, devel":     {&debug.BuildInfo{Main: debug.Module{Path: selfPath}}, "(devel)"},
		"no build info":   {nil, "(devel)"},
	} {
		if got := versionFrom(c.bi); got != c.want {
			t.Errorf("%s: versionFrom = %q, want %q", name, got, c.want)
		}
	}
}

// TS-23-11: a runner with no tools yields a working server with an empty tool
// list. 23-REQ-2.7.
func TestNoToolsServesAnEmptyList_TS23_11(t *testing.T) {
	s, err := New(runnerOf(t), Options{})
	if s == nil || err != nil {
		t.Fatalf("New = %v, %v", s, err)
	}
	listed, err := connect(t, s).ListTools(context.Background())
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("listed %d tools, want none", len(listed))
	}
}
