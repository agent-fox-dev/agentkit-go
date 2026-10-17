package mcpserve

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codemode"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	agentmcp "github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-23-61 (smoke, 23-PATH-1): an embedder serves code_mode over loopback, a
// pool client runs a script whose nested read_file call also passes the runner's
// guard, and Close refuses further connections. A real workspace, tools.For,
// code_mode, a Runner with a recording guard, a real Server, the SDK's
// Streamable HTTP handler, a real TCP listener and AgentKit's mcp.Pool: nothing
// is replaced.
func TestSmokeLoopbackCodeMode_TS23_61(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	const content = "a line the script reads through the runner"
	if err := os.WriteFile(filepath.Join(ws.Root(), "notes.txt"), []byte(content+"\n"), 0o644); err != nil {
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
	var mu sync.Mutex
	var seen []string
	runner, err := agentkit.NewRunner(agentkit.Toolset{Tools: append([]core.Tool{cm}, all...),
		Guard: func(ctx context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
			mu.Lock()
			seen = append(seen, in.ToolName)
			mu.Unlock()
			return guard.AllowAll(ctx, in)
		}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(runner, Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ep, err := srv.ServeLoopback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if !urlRE.MatchString(ep.URL) || !tokenRE.MatchString(ep.Token) {
		t.Fatalf("endpoint %q with token %q", ep.URL, ep.Token)
	}

	pool := agentmcp.NewPool(agentmcp.ConnectionOptions{})
	defer pool.Close()
	cfg := agentmcp.ServerConfig{Name: "agentkit", URL: ep.URL, Timeout: 10 * time.Second,
		Headers: map[string]string{"Authorization": "Bearer " + ep.Token}}
	if _, err := pool.Connect(ctx, cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	imported, err := pool.Tools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(runner.Tools()); len(imported) != want {
		t.Errorf("the pool imported %d tools, the runner serves %d", len(imported), want)
	}
	var code core.Tool
	for _, tl := range imported {
		if tl.Name == "agentkit__code_mode" {
			code = tl
		}
	}
	if code.Execute == nil {
		t.Fatalf("code_mode is not imported: %v", toolNames(imported))
	}
	script, _ := json.Marshal(map[string]string{"script": "n = read_file(path=\"notes.txt\")\nprint(n[\"content\"].strip())\n"})
	res := code.Execute(ctx, script)
	if !res.OK || !strings.Contains(res.LLMText(), content) {
		t.Errorf("code_mode = %+v, want OK and the file's content", res)
	}
	mu.Lock()
	if !slices.Contains(seen, "code_mode") || !slices.Contains(seen, "read_file") {
		t.Errorf("the guard saw %v, want both the code_mode call and the nested read_file call", seen)
	}
	mu.Unlock()

	if err := ep.Close(); err != nil {
		t.Errorf("Close returned %v", err)
	}
	if !refused(ep) {
		t.Error("a TCP connect to the port succeeds after Close")
	}
}

// TS-23-62 (smoke, 23-PATH-2): a harness spawns the program as a child process
// and uses its tools over stdio until EOF. The child is this test binary
// re-executed, serving a runner over the built-in tools and code_mode through
// ServeStdio; the client is mcp.Pool with ServerConfig.Command, over real
// pipes. When the pool closes the connection the child's stdin closes, every
// reply owed has been written, and the child exits 0.
func TestSmokeStdioChildProcess_TS23_62(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the child is this test binary re-executed with a marker variable")
	}
	dir, exitFile := t.TempDir(), filepath.Join(t.TempDir(), "exit")
	const content = "read by the child"
	if err := os.WriteFile(filepath.Join(dir, "child.txt"), []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := agentmcp.NewPool(agentmcp.ConnectionOptions{Warnf: func(format string, args ...any) { t.Logf("child: "+format, args...) }})
	ctx := context.Background()
	cfg := agentmcp.ServerConfig{Name: "child", Command: os.Args[0], Env: map[string]string{
		childMarker: "1", childExitFile: exitFile, childWorkspace: dir, "PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}}
	if _, err := pool.Connect(ctx, cfg, nil, nil); err != nil {
		t.Fatal(err)
	}
	imported, err := pool.Tools(ctx, nil)
	if err != nil || len(imported) < 2 {
		t.Fatalf("Tools = %v, %v", toolNames(imported), err)
	}
	byName := map[string]core.Tool{}
	for _, tl := range imported {
		byName[tl.Name] = tl
	}
	read := byName["child__read_file"].Execute(ctx, json.RawMessage(`{"path":"child.txt"}`))
	if !read.OK || !strings.Contains(read.LLMText(), content) {
		t.Errorf("read_file through the child = %+v, want the file's content", read)
	}
	script, _ := json.Marshal(map[string]string{"script": "print(read_file(path=\"child.txt\")[\"content\"].strip())\n"})
	if res := byName["child__code_mode"].Execute(ctx, script); !res.OK || !strings.Contains(res.LLMText(), content) {
		t.Errorf("code_mode through the child = %+v, want the file's content", res)
	}
	_ = pool.Close()
	var outcome []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if outcome, err = os.ReadFile(exitFile); err == nil {
			break
		}
	}
	if string(outcome) != "exit 0" {
		t.Errorf("the child ended %q, want %q", outcome, "exit 0")
	}
}

// TS-23-63 (smoke, 23-PATH-3): an unauthenticated request is refused with 401
// and a Warn record that carries the peer and no header, and a call to execute
// with a program the guard does not allow comes back as an error result that
// names blocked_by_policy and the guard's reason, not as a protocol error, with
// one Info "tool call" record for it with ok=false. A real Runner and guard,
// the real execute tool, mcp.Pool, the SDK's handler and a real listener.
func TestSmokeRefusalsAndBlockedCalls_TS23_63(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agentkit.NewRunner(agentkit.Toolset{Tools: all, Guard: guard.Allow("ls")})
	if err != nil {
		t.Fatal(err)
	}
	logs := newCapture()
	srv, err := New(runner, Options{Logger: slog.New(logs)})
	if err != nil {
		t.Fatal(err)
	}
	ep := serveLoop(t, srv)

	r := send(t, http.MethodPost, ep.URL, "", strings.NewReader(initFrame))
	if r.status != http.StatusUnauthorized || r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("an unauthenticated initialize: %d %q", r.status, r.header.Get("WWW-Authenticate"))
	}

	imported, err := dial(t, ep).Tools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var execute core.Tool
	for _, tl := range imported {
		if tl.Name == "agentkit__execute" {
			execute = tl
		}
	}
	if execute.Execute == nil {
		t.Fatalf("execute is not imported: %v", toolNames(imported))
	}
	res := execute.Execute(context.Background(), json.RawMessage(`{"command":"rm -rf nothing"}`))
	if res.OK || res.Error == "mcp_call_failed" {
		t.Fatalf("the blocked call = %+v, want an error result from the tool, not a failed call", res)
	}
	// The text is the runner's JSON envelope, so the reason's quotes are escaped.
	if text := res.Detail; !strings.Contains(text, "blocked_by_policy") || !strings.Contains(text, `guard.Restricted: program \"rm\" is not on the allowlist`) {
		t.Errorf("the result text is %q, want blocked_by_policy and the guard's reason", text)
	}

	var warns, infos []slog.Record
	for _, rec := range logs.records() {
		switch {
		case rec.Level == slog.LevelWarn:
			warns = append(warns, rec)
		case rec.Message == "tool call":
			infos = append(infos, rec)
		}
	}
	if len(warns) != 1 || !strings.HasPrefix(attrsOf(warns[0])["peer"].String(), "127.0.0.1:") {
		t.Errorf("Warn records %v, want one carrying the peer", warns)
	}
	if text := logs.text(); strings.Contains(text, ep.Token) || strings.Contains(text, "Bearer") {
		t.Errorf("the log carries the token or the header:\n%s", text)
	}
	if len(infos) != 1 || attrsOf(infos[0])["ok"].Bool() || attrsOf(infos[0])["error"].String() != "blocked_by_policy" {
		t.Errorf("tool call records %v, want one with ok=false and error=blocked_by_policy", infos)
	}
}
