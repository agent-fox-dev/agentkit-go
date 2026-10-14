package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// defaults are the flags' defaults, as main sets them.
func defaults(t *testing.T) options {
	return options{Harness: "scripted", Network: "none", Allow: "go,gofmt,git,specr", MaxTurns: 8, SpecTokens: 10000, Out: filepath.Join(t.TempDir(), "out")}
}

// The example runs offline — local, the sample project, the stand-in specr
// and the scripted harness over the MCP server — both as a program and in
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
		"on local (no isolation)", "synced the sample project: 7 files",
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

// The server runs every call through the guard — direct calls and a
// script's nested calls alike — and answers an unknown tool, a refused
// command and a bad argument with a result, never a Go error or a panic.
func TestServerGuardsDirectAndNestedCalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs POSIX commands")
	}
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws.Root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	srv, info, err := newToolServer(all, guard.Restricted(guard.Options{AllowedPrograms: []string{"cat"}}), &log)
	if err != nil {
		t.Fatal(err)
	}
	if info.BoundToolsCount != len(all) || srv.served()[0].Name != "code_mode" || len(srv.served()) != len(all)+1 {
		t.Fatalf("served %d tools (first %s), code_mode binds %d; want %d, code_mode, %d", len(srv.served()), srv.served()[0].Name, info.BoundToolsCount, len(all)+1, len(all))
	}
	ctx := context.Background()

	if res := srv.direct(ctx, "execute", json.RawMessage(`{"command":"cat a.txt"}`)); !res.OK || !strings.Contains(res.LLMText(), "hello") {
		t.Errorf("cat a.txt: %+v", res)
	}
	if res := srv.direct(ctx, "execute", json.RawMessage(`{"command":"cat a.txt | wc -l"}`)); res.OK || res.Error != core.BlockErrorCode {
		t.Errorf("a pipe was not blocked: %+v", res)
	}
	if res := srv.direct(ctx, "execute", json.RawMessage(`{"command":"rm a.txt"}`)); res.OK || res.Error != core.BlockErrorCode {
		t.Errorf("rm was not blocked: %+v", res)
	}
	if res := srv.direct(ctx, "no_such_tool", json.RawMessage(`{}`)); res.OK || res.Error != "unknown_tool" {
		t.Errorf("unknown tool: %+v", res)
	}
	if res := srv.direct(ctx, "read_file", json.RawMessage(`{}`)); res.OK || res.Error != "invalid_arguments" {
		t.Errorf("a missing argument: %+v", res)
	}
	if res := srv.direct(ctx, "read_file", json.RawMessage(`not json`)); res.OK || res.Error != "invalid_arguments" {
		t.Errorf("non-JSON input: %+v", res)
	}

	script, _ := json.Marshal(map[string]string{"script": `a = read_file(path="a.txt")
print("a:", a["content"].strip())
rm = execute(command="rm a.txt")
print("rm:", rm.error if is_error(rm) else "ran")
`})
	res := srv.direct(ctx, "code_mode", script)
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
	if res := srv.direct(ctx, "code_mode", self); res.OK || res.Error != "runtime_error" || !strings.Contains(res.Detail, "undefined: code_mode") {
		t.Errorf("code_mode calling itself: %+v", res)
	}
	if got := srv.blocked.Load(); got != 3 {
		t.Errorf("blocked %d calls, want 3", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, "a.txt")); err != nil {
		t.Errorf("a.txt is gone: the guard did not hold: %v", err)
	}
	for _, want := range []string{"  blocked  execute: guard.Restricted", "    run      read_file path=a.txt", "    blocked  execute: guard.Restricted"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log.String())
		}
	}
}

// The MCP server answers over HTTP only with the token, and serves the tool
// definitions AgentKit's driver would send.
func TestServeRequiresTheToken(t *testing.T) {
	ws, err := tools.NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	srv, _, err := newToolServer(all, guard.AllowAll, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m, err := srv.serve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer m.stop()
	if !strings.HasPrefix(m.URL, "http://127.0.0.1:") || !strings.HasSuffix(m.URL, "/mcp") || len(m.Token) != 48 {
		t.Fatalf("served at %q with token %q", m.URL, m.Token)
	}
	post := func(auth string) int {
		body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
		req, err := http.NewRequest(http.MethodPost, m.URL, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if code := post(""); code != http.StatusUnauthorized {
		t.Errorf("without the token: HTTP %d, want 401", code)
	}
	if code := post("Bearer not-" + m.Token); code != http.StatusUnauthorized {
		t.Errorf("with a wrong token: HTTP %d, want 401", code)
	}
	if code := post("Bearer " + m.Token); code != http.StatusOK {
		t.Errorf("with the token: HTTP %d, want 200", code)
	}
	var cfg struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(m.mcpConfig(false)), &cfg); err != nil {
		t.Fatal(err)
	}
	s := cfg.Servers["agentkit"]
	if s["type"] != "http" || s["url"] != m.URL || s["headers"].(map[string]any)["Authorization"] != "Bearer "+m.Token || s["exposure"] != nil {
		t.Errorf("mcp config: %v", s)
	}
	if err := json.Unmarshal([]byte(m.mcpConfig(true)), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Servers["agentkit"]["exposure"] != "direct" {
		t.Errorf("pi config lacks exposure direct: %v", cfg.Servers["agentkit"])
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
	for _, want := range [][]string{{"-p"}, {"--tools", "mcp__agentkit__*"}, {"--no-skills"}, {"--append-system-prompt", "sys.md"}, {"--model", "m"}, {"--", "-starts with a dash"}} {
		if !hasSeq(pi, want) {
			t.Errorf("pi args lack %q: %q", want, pi)
		}
	}
	if pi[len(pi)-1] != "-starts with a dash" {
		t.Errorf("pi's prompt is not last: %q", pi)
	}
	if _, err := runHarness(context.Background(), io.Discard, "nope", &servedMCP{}, "", "", o); err == nil || !strings.Contains(err.Error(), "unknown harness") {
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
