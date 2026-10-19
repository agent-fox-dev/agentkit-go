package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/mcpserve"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// defaults are the flags' defaults, as main sets them.
func defaults(t *testing.T) options {
	return options{Harness: "scripted", Network: "none", Allow: "go,gofmt,git,specr", MaxTurns: 8, SpecTokens: 10000, Out: filepath.Join(t.TempDir(), "out")}
}

// TS-23-60: the example runs offline — local, the sample project, the stand-in
// specr and the scripted harness over the MCP server — both as a program and in
// process, and the task lands: the test is written and red first, then
// green, the gate passes, and the exported workspace holds the work.
func TestExampleRunsOffline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	out, err := exec.Command("go", "run", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Harness example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}

	o := defaults(t)
	var account, log strings.Builder
	if err := run(context.Background(), &account, &log, o); err != nil {
		t.Fatalf("%v\n%s\n%s", err, account.String(), log.String())
	}
	for _, want := range []string{
		"on local (no isolation)", "filled the workspace from the sample project",
		"specr: rendered 01_greeting scoped to task 1 of 1",
		"baseline:\n  linter `gofmt -l .`: ok\n  all_tests `go test ./...`: ok",
		"MCP server at http://127.0.0.1:",
		"starting harness scripted on task 1 of .specs/01_greeting (Implement Greet)",
		"harness scripted exited 0 after 2 turns", "0 blocked",
		"after the task:\n  linter `gofmt -l .`: ok\n  all_tests `go test ./...`: ok\nverdict: pass",
		"Harness example completed successfully",
	} {
		if !strings.Contains(account.String(), want) {
			t.Errorf("the account lacks %q:\n%s", want, account.String())
		}
	}
	for _, want := range []string{
		"imported 12 tools over MCP: agentkit__code_mode, agentkit__edit_file, agentkit__execute",
		"run      code_mode script=", "    run      write_file", "    error    execute: command_exit: exit 1",
		"TS-01-1 before the implementation: outcome exit, exit 1", "TS-01-1 after: outcome ok, exit 0",
		"tasks.json: task 1 done",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
	for _, name := range []string{"greeter.go", "greeter_test.go", filepath.Join(".specs", "01_greeting", "tasks.json")} {
		b, err := os.ReadFile(filepath.Join(o.Out, name))
		if err != nil {
			t.Errorf("the exported %s: %v", name, err)
			continue
		}
		switch name {
		case "greeter.go":
			if !strings.Contains(string(b), `"Hello, " + name + "!"`) || strings.Contains(string(b), "not implemented") {
				t.Errorf("exported greeter.go is not the implementation:\n%s", b)
			}
		case "greeter_test.go":
			if !strings.Contains(string(b), "TestGreet_TS01_1") {
				t.Errorf("exported greeter_test.go lacks TS-01-1:\n%s", b)
			}
		default:
			if !strings.Contains(string(b), `"state": "done"`) {
				t.Errorf("exported tasks.json does not mark the task done:\n%s", b)
			}
		}
	}
}

// The runner runs every call through the guard — direct calls and a
// script's nested calls alike — and answers an unknown tool, a refused
// command and a bad argument with a result, never a Go error or a panic. The
// call and blocked counts and the log come from the toolset's own hooks
// (TS-21-53, 21-REQ-9.5).
func TestServerGuardsDirectAndNestedCalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX commands")
	}
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	runner, hk, info, err := buildRunner(all, guard.Restricted(guard.Options{AllowedPrograms: []string{"cat"}}), &log)
	if err != nil {
		t.Fatal(err)
	}
	if served := runner.Tools(); info.BoundToolsCount != len(all) || served[0].Name != "code_mode" || len(served) != len(all)+1 {
		t.Fatalf("served %d tools (first %s), code_mode binds %d; want %d, code_mode, %d", len(served), served[0].Name, info.BoundToolsCount, len(all)+1, len(all))
	}
	ctx := context.Background()

	if res := runner.Call(ctx, "execute", json.RawMessage(`{"command":"cat a.txt"}`)); !res.OK || !strings.Contains(res.LLMText(), "hello") {
		t.Errorf("cat a.txt: %+v", res)
	}
	if res := runner.Call(ctx, "execute", json.RawMessage(`{"command":"cat a.txt | wc -l"}`)); res.OK || res.Error != core.BlockErrorCode {
		t.Errorf("a pipe was not blocked: %+v", res)
	}
	if res := runner.Call(ctx, "execute", json.RawMessage(`{"command":"rm a.txt"}`)); res.OK || res.Error != core.BlockErrorCode {
		t.Errorf("rm was not blocked: %+v", res)
	}
	if res := runner.Call(ctx, "no_such_tool", json.RawMessage(`{}`)); res.OK || res.Error != "unknown_tool" {
		t.Errorf("unknown tool: %+v", res)
	}
	if res := runner.Call(ctx, "read_file", json.RawMessage(`{}`)); res.OK || res.Error != "invalid_arguments" {
		t.Errorf("a missing argument: %+v", res)
	}
	if res := runner.Call(ctx, "read_file", json.RawMessage(`not json`)); res.OK || res.Error != "invalid_arguments" {
		t.Errorf("non-JSON input: %+v", res)
	}

	script, _ := json.Marshal(map[string]string{"script": `a = read_file(path="a.txt")
print("a:", a["content"].strip())
rm = execute(command="rm a.txt")
print("rm:", rm.error if is_error(rm) else "ran")
`})
	res := runner.Call(ctx, "code_mode", script)
	if !res.OK {
		t.Fatalf("code_mode: %+v", res)
	}
	for _, want := range []string{"a: hello", "rm: blocked_by_policy"} {
		if !strings.Contains(res.LLMText(), want) {
			t.Errorf("code_mode output lacks %q:\n%s", want, res.LLMText())
		}
	}
	// A script reaches the tools code_mode binds and no others: not code_mode.
	self, _ := json.Marshal(map[string]string{"script": `code_mode(script="print(1)")`})
	if res := runner.Call(ctx, "code_mode", self); res.OK || res.Error != "runtime_error" || !strings.Contains(res.Detail, "undefined: code_mode") {
		t.Errorf("code_mode calling itself: %+v", res)
	}
	if got := hk.blocked.Load(); got != 3 {
		t.Errorf("blocked %d calls, want 3", got)
	}
	if got := hk.calls.Load(); got < 9 {
		t.Errorf("counted %d calls, want every one of the nine direct calls and the script's nested ones", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Root(), "a.txt")); err != nil {
		t.Errorf("a.txt is gone: the guard did not hold: %v", err)
	}
	for _, want := range []string{"  blocked  execute: guard.Restricted", "    run      read_file path=a.txt", "    blocked  execute: guard.Restricted"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
}

// The harness command lines turn the harness's own tools off and leave it
// this server alone.
func TestHarnessArgs(t *testing.T) {
	o := options{MaxTurns: 7, Model: "m"}
	claude := claudeArgs(`{"mcpServers":{}}`, "sys.md", "do it", o)
	for _, want := range [][]string{{"-p", "do it"}, {"--tools", ""}, {"--strict-mcp-config"}, {"--mcp-config", `{"mcpServers":{}}`},
		{"--allowedTools", "mcp__agentkit"}, {"--permission-mode", "dontAsk"}, {"--append-system-prompt-file", "sys.md"},
		{"--max-turns", "7"}, {"--output-format", "json"}, {"--no-session-persistence"}, {"--model", "m"}} {
		if !hasSeq(claude, want) {
			t.Errorf("claude args lack %q: %q", want, claude)
		}
	}
	pi := piArgs("sys.md", "-starts with a dash", o)
	for _, want := range [][]string{{"-p"}, {"--approve"}, {"--tools", "mcp__agentkit__*"}, {"--no-skills"}, {"--append-system-prompt", "sys.md"}, {"--model", "m"}, {"--", "-starts with a dash"}} {
		if !hasSeq(pi, want) {
			t.Errorf("pi args lack %q: %q", want, pi)
		}
	}
	if pi[len(pi)-1] != "-starts with a dash" {
		t.Errorf("pi's prompt is not last: %q", pi)
	}
	if _, err := runHarness(context.Background(), io.Discard, "nope", &mcpserve.Endpoint{}, "", "", o); err == nil || !strings.Contains(err.Error(), "unknown harness") {
		t.Errorf("unknown harness: %v", err)
	}
	if _, _, err := runCommand(context.Background(), io.Discard, t.TempDir(), "agentkit-no-such-harness"); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("a missing command: %v", err)
	}
}

// verdict is af-impl's table.
func TestVerdict(t *testing.T) {
	green := []check{{Name: "linter", Outcome: "ok"}, {Name: "all_tests", Outcome: "ok"}}
	red := []check{{Name: "linter", Outcome: "ok"}, {Name: "all_tests", Outcome: "exit", ExitCode: 1}}
	unrun := []check{{Name: "linter", Outcome: "ok"}, {Name: "all_tests", Err: errShellBlocked}}
	for _, c := range []struct {
		before, after []check
		want          string
		landable      bool
	}{
		{green, green, "pass", true},
		{red, green, "pass, baseline was failing", true},
		{green, red, "regressed", false},
		{red, red, "still failing", false},
		{green, unrun, "gate failed: a check could not run, so the work was never measured", false},
		{unrun, green, "gate failed: a check could not run, so the work was never measured", false},
	} {
		got, landable := verdict(c.before, c.after)
		if got != c.want || landable != c.landable {
			t.Errorf("verdict(%v, %v) = %q, %v; want %q, %v", c.before, c.after, got, landable, c.want, c.landable)
		}
	}
	if s := unrun[1].String(); !strings.Contains(s, "could not run") {
		t.Errorf("an unrun check reads %q", s)
	}
	if s := red[1].String(); !strings.Contains(s, "exit, exit 1") {
		t.Errorf("a failed check reads %q", s)
	}
}

// The sample spec package is the shape the format's schemas require, and
// the stand-in specr's envelope names it.
func TestSampleShape(t *testing.T) {
	dir := t.TempDir()
	if err := writeSample(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"requirements.json", "test_spec.json", "tasks.json"} {
		b, err := os.ReadFile(filepath.Join(dir, ".specs", "01_greeting", f))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if v["spec_id"] != "01" || v["spec_name"] != "greeting" || v["schema_version"] != float64(2) {
			t.Errorf("%s header: %v %v %v", f, v["spec_id"], v["spec_name"], v["schema_version"])
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	bin := filepath.Join(dir, "bin")
	if err := writeStandInSpecr(bin); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(bin, "specr"), "01", "--task", "1").Output()
	if err != nil {
		t.Fatal(err)
	}
	var env specrResult
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("the stand-in printed no envelope: %v\n%s", err, out)
	}
	if !env.OK || env.Result.SpecDir != ".specs/01_greeting" || env.Result.Task == nil || env.Result.Task.ID != 1 ||
		!strings.Contains(env.Result.Markdown, "TS-01-1") || !slices.Equal(env.Next[0].Flags, []string{"--task", "1"}) {
		t.Errorf("envelope: %+v", env)
	}
}

func hasSeq(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// TS-21-51, retargeted by spec 23: the example holds no tool-call pipeline. The
// runner owns preparation, the guard, the handler and its recovery, and the
// nested caller; mcpserve serves the tools, and the example's own files call
// Runner.Call and nothing like it. Before spec 23 this parsed server.go, which
// held the MCP server; its remaining files are the ones that could regrow a
// pipeline (docs/errata/23_mcp_serve.md).
func TestExampleHoldsNoToolCallPipeline_TS21_51(t *testing.T) {
	banned := map[string]bool{"core.NewToolUse": true, "core.PrepareArguments": true, "core.WithNestedCaller": true}
	for name, f := range parseNonTest(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				switch fun := v.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := fun.X.(*ast.Ident); ok && banned[x.Name+"."+fun.Sel.Name] {
						t.Errorf("%s calls %s.%s", name, x.Name, fun.Sel.Name)
					}
				case *ast.Ident:
					if fun.Name == "recover" {
						t.Errorf("%s calls recover()", name)
					}
				}
			case *ast.FuncDecl:
				if v.Recv == nil {
					break
				}
				if v.Name.Name == "call" {
					t.Errorf("%s declares a method named call", name)
				}
				// A core.NestedCaller: Call(ctx, ...core.ToolUseBlock).
				if v.Name.Name == "Call" {
					for _, p := range v.Type.Params.List {
						if _, variadic := p.Type.(*ast.Ellipsis); variadic {
							t.Errorf("%s declares a Call method taking variadic tool-use blocks: a core.NestedCaller", name)
						}
					}
				}
			case *ast.TypeSpec:
				if v.Name.Name == "nested" {
					t.Errorf("%s declares the type nested", name)
				}
			}
			return true
		})
	}
}

var callIDRE = regexp.MustCompile(`^call_[0-9a-f]{16}$`)

// TS-21-52: the program builds code_mode over the proxies and a runner over
// code_mode and the proxies under a guard that wraps the configured one; the
// spec steps run through Runner.Call, so the configured guard sees them; and
// the system prompt is built from Runner.Tools().
func TestHarnessStepsRunThroughTheRunner_TS21_52(t *testing.T) {
	const envelope = `{"ok":true,"status":"ok","exit_code":0,"summary":"specr: rendered 01_x","result":{"spec_dir":".specs/01_x",` +
		`"task":{"id":1,"title":"T","state":"pending"},"markdown":"# spec"},"next":[]}`
	const tasksJSON = `{"test_commands":{"linter":"gofmt -l .","all_tests":"go test ./..."}}`
	execute := core.Tool{Name: "execute", InputSchema: schema.Object(schema.Prop("command", schema.String()), schema.Opt("timeout_s", schema.Int())),
		PromptGuidelines: []string{"Run one program per call."},
		Execute: func(_ context.Context, in json.RawMessage) core.ToolResult {
			var a struct{ Command string }
			_ = json.Unmarshal(in, &a)
			out := ""
			switch {
			case strings.HasPrefix(a.Command, "specr"):
				out = envelope
			case strings.HasPrefix(a.Command, "git status"):
				out = " M greeter.go\n"
			}
			return core.OKResult(map[string]any{"output": out, "outcome": "ok", "exit_code": 0})
		}}
	readFile := core.Tool{Name: "read_file", InputSchema: schema.Object(schema.Prop("path", schema.String())),
		Execute: func(context.Context, json.RawMessage) core.ToolResult {
			return core.OKResult(map[string]any{"content": tasksJSON})
		}}

	var seen []string
	configured := func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		seen = append(seen, in.ToolName+" "+in.ToolUseID)
		return core.BeforeToolCallDecision{}
	}
	runner, hk, _, err := buildRunner([]core.Tool{execute, readFile}, configured, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range runner.Tools() {
		names = append(names, tl.Name)
	}
	if !slices.Equal(names, []string{"code_mode", "execute", "read_file"}) {
		t.Fatalf("the runner serves %v, want code_mode and the proxies", names)
	}

	ctx := context.Background()
	render, err := renderSpec(ctx, runner, "01", 1, 10000)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := readTestCommands(ctx, runner, render.Result.SpecDir)
	if err != nil {
		t.Fatal(err)
	}
	checks := runChecks(ctx, runner, tc)
	if len(checks) != 2 || !checks[0].Green() || !checks[1].Green() {
		t.Fatalf("checks = %v", checks)
	}
	if status := gitStatus(ctx, runner); status != "M greeter.go" {
		t.Fatalf("gitStatus = %q, want what git printed, trimmed", status)
	}
	// 1 specr, 1 read_file, 2 checks and the final git status: each reached the
	// configured guard, as a Runner.Call, which gives a call its id.
	if len(seen) != 5 {
		t.Fatalf("the configured guard saw %d calls (%v), want 5", len(seen), seen)
	}
	for _, s := range seen {
		if id := strings.Fields(s)[1]; !callIDRE.MatchString(id) {
			t.Errorf("the guard saw %q: not a Runner.Call id", s)
		}
	}
	if got := hk.calls.Load(); got != 5 {
		t.Errorf("the hooks counted %d calls, want 5", got)
	}
	system := systemPrompt(runner.Tools(), []string{"go", "git"})
	if !strings.Contains(system, "Run one program per call.") || !strings.Contains(system, "The shell may run: go, git.") {
		t.Errorf("the system prompt lacks the served tools' guidelines or the allowed programs:\n%s", system)
	}
}

// TS-21-54, rewritten by spec 23 on mcpserve: an MCP client holding the token
// calls a served tool and gets one text content holding the runner's LLMText,
// with IsError the negation of OK. The client is AgentKit's own, so the example
// imports no MCP SDK package, even in its tests.
func TestMCPHandlerAnswersThroughRunnerCall_TS21_54(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root(), "a.txt"), []byte("hello mcp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	runner, _, _, err := buildRunner(all, guard.AllowAll, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserve.New(runner, mcpserve.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ep, err := srv.ServeLoopback(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()

	pool := mcp.NewPool(mcp.ConnectionOptions{})
	defer pool.Close()
	conn, err := pool.Connect(ctx, mcp.ServerConfig{Name: "test", URL: ep.URL, Headers: map[string]string{"Authorization": "Bearer " + ep.Token}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name  string
		input string
	}{
		{"read_file", `{"path":"a.txt"}`},
		{"read_file", `{}`},
	} {
		want := runner.Call(ctx, c.name, json.RawMessage(c.input))
		got, err := conn.CallRaw(ctx, c.name, json.RawMessage(c.input))
		if err != nil {
			t.Fatalf("%s %s: a protocol error: %v", c.name, c.input, err)
		}
		text, ok := got.Content[0].(*mcp.TextContent)
		if len(got.Content) != 1 || !ok {
			t.Fatalf("%s %s: content = %#v, want one text content", c.name, c.input, got.Content)
		}
		if text.Text != want.LLMText() || got.IsError != !want.OK {
			t.Errorf("%s %s: MCP says %q (error %v), the runner says %q (ok %v)", c.name, c.input, text.Text, got.IsError, want.LLMText(), want.OK)
		}
	}
}

// TS-21-60 (smoke, 21-PATH-4): the harness example runs offline through a
// runner and prints its success line. The program is built and run, so the
// runner, code mode, the MCP server and the scripted provider are the real
// ones: the scripted harness's tool calls arrive over MCP and are answered by
// Runner.Call, and the counts the program prints come from the toolset's hooks.
func TestSmokeHarnessRunsOfflineThroughARunner_TS21_60(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	cmd := exec.Command("go", "run", ".")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go run: %v\n%s\n%s", err, out, stderr.String())
	}
	if !strings.Contains(string(out), "Harness example completed successfully") {
		t.Fatalf("output:\n%s", out)
	}
	m := regexp.MustCompile(`; (\d+) tool calls, (\d+) blocked`).FindStringSubmatch(string(out))
	if m == nil || m[1] == "0" || m[2] != "0" {
		t.Errorf("the printed counts are %v; want tool calls from the hooks and none blocked:\n%s", m, out)
	}
	for _, want := range []string{"run      code_mode script=", "    run      write_file"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the log lacks %q: the MCP-served call and its nested calls ran through the runner's hooks:\n%s", want, stderr.String())
		}
	}
}

// parseNonTest parses the example's non-test Go files, by name.
func parseNonTest(t *testing.T) map[string]*ast.File {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[n] = f
	}
	return out
}

// selector names x.sel, or "" when e is not a selector on an identifier.
func selector(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		if id, ok := sel.X.(*ast.Ident); ok {
			return id.Name + "." + sel.Sel.Name
		}
	}
	return ""
}

// declared is every top-level name a file declares, and "hooks.<method>" for a
// method on hooks.
func declared(f *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				out[d.Name.Name] = true
				continue
			}
			if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := star.X.(*ast.Ident); ok {
					out[id.Name+"."+d.Name.Name] = true
				}
			}
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					out[sp.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						out[n.Name] = true
					}
				}
			}
		}
	}
	return out
}

// TS-23-54: the example has no server.go, imports no MCP SDK package or net and
// calls no listen API, and calls.go holds what server.go held that is not the
// server, with the per-call log and counts. 23-REQ-10.1.
func TestExampleHoldsNoServer_TS23_54(t *testing.T) {
	if _, err := os.Stat("server.go"); err == nil {
		t.Error("server.go exists")
	}
	files := parseNonTest(t)
	for name, f := range files {
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, "github.com/modelcontextprotocol/go-sdk") || p == "net" {
				t.Errorf("%s imports %s", name, p)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "Listen") {
				t.Errorf("%s refers to %s, a listen API", name, sel.Sel.Name)
			}
			return true
		})
	}
	calls, ok := files["calls.go"]
	if !ok {
		t.Fatal("calls.go does not exist")
	}
	have := declared(calls)
	for _, want := range []string{"summarize", "firstLine", "truncate", "errShellBlocked", "hooks", "indentFor",
		"hooks.printf", "hooks.guard", "hooks.after", "hooks.event"} {
		if !have[want] {
			t.Errorf("calls.go does not declare %s", want)
		}
	}
}

// TS-23-55: main.go serves the runner with mcpserve.New(runner,
// mcpserve.Options{Instructions: ...}) and ServeLoopback, defers ep.Close() and
// prints the endpoint's URL. 23-REQ-10.2.
func TestMainServesThroughMcpserve_TS23_55(t *testing.T) {
	var newCall, optionsWithInstructions, serve, deferredClose, printsURL bool
	ast.Inspect(parseNonTest(t)["main.go"], func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if selector(v.Fun) == "mcpserve.New" {
				newCall = true
				for _, a := range v.Args {
					if lit, ok := a.(*ast.CompositeLit); ok && selector(lit.Type) == "mcpserve.Options" {
						for _, e := range lit.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok && kv.Key.(*ast.Ident).Name == "Instructions" {
								optionsWithInstructions = true
							}
						}
					}
				}
			}
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ServeLoopback" {
				serve = true
			}
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fprintf" {
				for _, a := range v.Args {
					if lit, ok := a.(*ast.BasicLit); ok && strings.Contains(lit.Value, "MCP server at") {
						printsURL = true
					}
				}
			}
		case *ast.DeferStmt:
			if sel, ok := v.Call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" && selector(sel) == "ep.Close" {
				deferredClose = true
			}
		}
		return true
	})
	for what, ok := range map[string]bool{"calls mcpserve.New": newCall, "passes Options{Instructions: ...}": optionsWithInstructions,
		"calls ServeLoopback": serve, "defers ep.Close()": deferredClose, `prints "MCP server at"`: printsURL} {
		if !ok {
			t.Errorf("main.go does not do this: %s", what)
		}
	}
}

// TS-23-56: the harness runners take *mcpserve.Endpoint; claude is given
// ClaudeConfig("agentkit") after --mcp-config and pi finds PiConfig("agentkit")
// in .pi/mcp.json. A claude and a pi that only record what they were given
// stand in for the real ones; the pi reads .pi/mcp.json only when it is run
// with --approve, as the real one does in print mode, which cannot ask whether
// to trust the project and skips an untrusted project's MCP servers, leaving
// the model no tools. 23-REQ-10.3.
func TestRunnersTakeTheEndpoint_TS23_56(t *testing.T) {
	files := parseNonTest(t)
	for _, fn := range []string{"runHarness", "runClaude", "runPi", "runScripted"} {
		var found bool
		for _, d := range files["harness.go"].Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == fn {
				found = true
				var takes bool
				for _, p := range fd.Type.Params.List {
					if star, ok := p.Type.(*ast.StarExpr); ok && selector(star.X) == "mcpserve.Endpoint" {
						takes = true
					}
				}
				if !takes {
					t.Errorf("%s does not take a *mcpserve.Endpoint", fn)
				}
			}
		}
		if !found {
			t.Errorf("harness.go does not declare %s", fn)
		}
	}
	ep := &mcpserve.Endpoint{URL: "http://127.0.0.1:9/mcp", Token: strings.Repeat("a", 48)}
	args := claudeArgs(ep.ClaudeConfig("agentkit"), "sys.md", "do it", options{MaxTurns: 3})
	if i := slices.Index(args, "--mcp-config"); i < 0 || args[i+1] != ep.ClaudeConfig("agentkit") {
		t.Errorf("claude args carry %q after --mcp-config, want %q", args, ep.ClaudeConfig("agentkit"))
	}

	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	bin, out := t.TempDir(), t.TempDir()
	scripts := map[string]string{
		"claude": "#!/bin/sh\nprev=\nfor a in \"$@\"; do\n  if [ \"$prev\" = --mcp-config ]; then printf '%s' \"$a\" > \"$HARNESS_TEST_OUT/claude-config\"; fi\n  prev=$a\ndone\nprintf '{\"result\":\"done\",\"total_cost_usd\":0,\"num_turns\":1}'\n",
		"pi":     "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = --approve ]; then cp .pi/mcp.json \"$HARNESS_TEST_OUT/pi-config\"; fi\ndone\necho done\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HARNESS_TEST_OUT", out)
	o := options{MaxTurns: 3}
	if hr, err := runHarness(context.Background(), io.Discard, "claude", ep, "system", "do it", o); err != nil || hr.Final != "done" {
		t.Fatalf("claude: %+v, %v", hr, err)
	}
	if _, err := runHarness(context.Background(), io.Discard, "pi", ep, "system", "do it", o); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{"claude-config": ep.ClaudeConfig("agentkit"), "pi-config": ep.PiConfig("agentkit")} {
		got, err := os.ReadFile(filepath.Join(out, file))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v; want %q", file, got, err, want)
		}
	}
}

// TS-23-57: the example chooses its source first and fills the sandbox with
// exactly one sandbox.Open: no Create, Sync or sandbox Tools call. 23-REQ-10.4.
func TestMainFillsWithOneOpen_TS23_57(t *testing.T) {
	opens := 0
	var gitDepth1, excludeGit, sample bool
	for name, f := range parseNonTest(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				if selector(v.Fun) == "sandbox.Open" {
					opens++
				}
				if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "Create", "Sync":
						t.Errorf("%s calls %s", name, sel.Sel.Name)
					case "Tools":
						for _, a := range v.Args {
							if lit, ok := a.(*ast.CompositeLit); ok && selector(lit.Type) == "sandbox.ToolOptions" {
								t.Errorf("%s calls Sandbox.Tools", name)
							}
						}
					}
				}
			case *ast.CompositeLit:
				kv := map[string]ast.Expr{}
				for _, e := range v.Elts {
					if p, ok := e.(*ast.KeyValueExpr); ok {
						if id, ok := p.Key.(*ast.Ident); ok {
							kv[id.Name] = p.Value
						}
					}
				}
				switch selector(v.Type) {
				case "sandbox.GitRepo":
					if lit, ok := kv["Depth"].(*ast.BasicLit); ok && lit.Value == "1" {
						gitDepth1 = true
					}
				case "sandbox.LocalDir":
					if id, ok := kv["ExcludeGit"].(*ast.Ident); ok && id.Name == "true" {
						excludeGit = true
					}
					if _, ok := kv["Path"]; ok && kv["ExcludeGit"] == nil {
						sample = true
					}
				}
			}
			return true
		})
	}
	if opens != 1 {
		t.Errorf("the example calls sandbox.Open %d times, want 1", opens)
	}
	if !gitDepth1 || !excludeGit || !sample {
		t.Errorf("sources: GitRepo with Depth 1 %v, LocalDir with ExcludeGit %v, the sample's LocalDir %v; want all three", gitDepth1, excludeGit, sample)
	}
}

// TS-23-58: the example uses guard.Allow(allowed...) and the faux helpers, and
// has no guard.Restricted call or faux.Turn literal. 23-REQ-10.5.
func TestExampleUsesTheHelpers_TS23_58(t *testing.T) {
	var allow, toolCall, text bool
	for name, f := range parseNonTest(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				switch selector(v.Fun) {
				case "guard.Allow":
					allow = allow || v.Ellipsis.IsValid()
				case "faux.ToolCall":
					toolCall = true
				case "faux.Text":
					text = true
				case "guard.Restricted":
					t.Errorf("%s calls guard.Restricted", name)
				}
			case *ast.CompositeLit:
				if selector(v.Type) == "faux.Turn" {
					t.Errorf("%s has a faux.Turn literal", name)
				}
			}
			return true
		})
	}
	if !allow || !toolCall || !text {
		t.Errorf("guard.Allow(allowed...) %v, faux.ToolCall %v, faux.Text %v; want all three", allow, toolCall, text)
	}
}

// TS-23-59: go.mod requires mcpserve with a replace to ../../mcpserve, and the
// MCP SDK only as an indirect requirement. 23-REQ-10.6.
func TestGoModUsesMcpserve_TS23_59(t *testing.T) {
	out, err := exec.Command("go", "mod", "edit", "-json").Output()
	if err != nil {
		t.Fatalf("go mod edit -json: %v", err)
	}
	var m struct {
		Require []struct {
			Path, Version string
			Indirect      bool
		}
		Replace []struct{ Old, New struct{ Path string } }
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	const self, sdk = "github.com/agent-fox-dev/agentkit-go/mcpserve", "github.com/modelcontextprotocol/go-sdk"
	var sawSelf, sawSDK bool
	for _, r := range m.Require {
		switch r.Path {
		case self:
			sawSelf = true
			if r.Version != "v0.0.0" || r.Indirect {
				t.Errorf("mcpserve is required at %s (indirect %v), want v0.0.0 directly", r.Version, r.Indirect)
			}
		case sdk:
			sawSDK = true
			if !r.Indirect {
				t.Error("the MCP SDK is still a direct requirement")
			}
		}
	}
	if !sawSelf || !sawSDK {
		t.Errorf("requirements: mcpserve %v, the SDK %v; want both", sawSelf, sawSDK)
	}
	var replaced bool
	for _, r := range m.Replace {
		replaced = replaced || (r.Old.Path == self && r.New.Path == "../../mcpserve")
	}
	if !replaced {
		t.Errorf("no replace of mcpserve to ../../mcpserve in %+v", m.Replace)
	}
}

// TS-23-64 (smoke, 23-PATH-4): the harness runs offline end to end through
// sandbox.Open, the Runner and mcpserve, and when it returns the endpoint and
// the sandbox are closed. The sandbox is local's real one, the runner and
// mcpserve are the real ones, the scripted harness reaches them through
// mcp.Pool and its faux.ToolCall and faux.Text turns, and code_mode runs over
// the sandbox's proxies. 23-PATH-4, 23-REQ-10.7.
func TestSmokeHarnessThroughOpenRunnerAndMcpserve_TS23_64(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	var account, log strings.Builder
	if err := run(context.Background(), &account, &log, defaults(t)); err != nil {
		t.Fatalf("%v\n%s\n%s", err, account.String(), log.String())
	}
	for _, want := range []string{"filled the workspace from the sample project", "MCP server at http://127.0.0.1:",
		"verdict: pass", "Harness example completed successfully"} {
		if !strings.Contains(account.String(), want) {
			t.Errorf("the account lacks %q:\n%s", want, account.String())
		}
	}
	if want := "imported 12 tools over MCP: agentkit__code_mode, agentkit__edit_file, agentkit__execute"; !strings.Contains(log.String(), want) {
		t.Errorf("the log lacks %q:\n%s", want, log.String())
	}
	if !strings.Contains(log.String(), "    run      write_file") {
		t.Errorf("the scripted code_mode turn's nested calls did not run through the runner:\n%s", log.String())
	}

	m := regexp.MustCompile(`MCP server at http://(127\.0\.0\.1:[0-9]+)/mcp`).FindStringSubmatch(account.String())
	if m == nil {
		t.Fatal("the account names no endpoint")
	}
	if c, err := net.DialTimeout("tcp", m[1], time.Second); err == nil {
		_ = c.Close()
		t.Errorf("%s still accepts connections after the run: the endpoint was not closed", m[1])
	}
	root := filepath.Join(tmp, "agentkit-sandbox-local")
	ents, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(ents) > 0 {
		t.Errorf("%d sandbox directories remain under %s: the sandbox was not closed", len(ents), root)
	}
}
