package main

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
)

// The offline run: local, the sample project, the stand-in specr and the
// scripted harness over the MCP server. The test is written and red first,
// then green, the gate passes, the workspace is exported, and the endpoint and
// the sandbox are closed when run returns.
func TestTheOfflineRunLandsTheTaskAndCleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	o := defaults(t)
	var account, log strings.Builder
	if err := run(context.Background(), &account, &log, o); err != nil {
		t.Fatalf("%v\n%s\n%s", err, account.String(), log.String())
	}
	for _, want := range []string{
		"on local (no isolation)", "filled the workspace from the sample project",
		"specr: rendered 01_greeting scoped to task 1 of 1",
		"baseline:\n  linter `gofmt -l .`: ok\n  all_tests `go test ./...`: ok",
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
	for name, want := range map[string]string{
		"greeter.go":                    `"Hello, " + name + "!"`,
		"greeter_test.go":               "func TestGreet_TS01_1",
		".specs/01_greeting/tasks.json": `"state": "done"`,
	} {
		if b, err := os.ReadFile(filepath.Join(o.Out, name)); err != nil || !strings.Contains(string(b), want) {
			t.Errorf("the exported %s lacks %q: %v\n%s", name, want, err, b)
		}
	}
	m := regexp.MustCompile(`MCP server at http://(127\.0\.0\.1:[0-9]+)/mcp`).FindStringSubmatch(account.String())
	if m == nil {
		t.Fatal("the account names no endpoint")
	}
	if c, err := net.DialTimeout("tcp", m[1], time.Second); err == nil {
		_ = c.Close()
		t.Errorf("%s still accepts connections after the run", m[1])
	}
	if ents, err := os.ReadDir(filepath.Join(tmp, "agentkit-sandbox-local")); len(ents) > 0 || (err != nil && !errors.Is(err, os.ErrNotExist)) {
		t.Errorf("%d sandbox directories remain after the run (%v)", len(ents), err)
	}
}

// The program, built and run, prints its account on stdout and every tool
// call on stderr, with the counts the toolset's hooks kept.
func TestTheProgramRunsOfflineWithItsDefaults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in specr is a shell script")
	}
	res := testutil.GoRun(t, "", ".", testutil.Environ("ANTHROPIC_API_KEY"))
	if res.Exit != 0 || !strings.Contains(res.Stdout, "Harness example completed successfully") {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
	if m := regexp.MustCompile(`; (\d+) tool calls, (\d+) blocked`).FindStringSubmatch(res.Stdout); m == nil || m[1] == "0" || m[2] != "0" {
		t.Errorf("the printed counts are %v; want calls and none blocked:\n%s", m, res.Stdout)
	}
	if !strings.Contains(res.Stderr, "run      code_mode script=") || !strings.Contains(res.Stderr, "    run      write_file") {
		t.Errorf("stderr lacks the served call and its nested calls:\n%s", res.Stderr)
	}
}

func TestRunRefusesARepositoryOnLocal(t *testing.T) {
	o := defaults(t)
	o.Repo = "https://example.invalid/r.git"
	if err := run(context.Background(), &strings.Builder{}, &strings.Builder{}, o); err == nil || !strings.Contains(err.Error(), "--repo needs --podman") {
		t.Errorf("run = %v", err)
	}
}

func TestBuildRunnerServesCodeModeBesideTheProxiesAndRequiresAGuard(t *testing.T) {
	if _, _, _, err := buildRunner(nil, nil, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "a guard is required") {
		t.Errorf("no guard: %v", err)
	}
	r, _, _ := fakeSandbox(t, nil, "")
	var names []string
	for _, tl := range r.Tools() {
		names = append(names, tl.Name)
	}
	if strings.Join(names, " ") != "code_mode execute read_file" {
		t.Errorf("the runner serves %v, want code_mode and the proxies", names)
	}
}

func TestGitStatusIsWhatGitPrintedOrNothing(t *testing.T) {
	for want, answer := range map[string]core.ToolResult{
		"M greeter.go": ran("ok", 0, " M greeter.go\n"),
		"":             ran("exit", 128, "fatal: not a git repository"),
	} {
		r, _, _ := fakeSandbox(t, map[string]core.ToolResult{"git status": answer}, "")
		if got := gitStatus(context.Background(), r); got != want {
			t.Errorf("gitStatus = %q, want %q", got, want)
		}
	}
}

func TestIsolationNamesEachLevel(t *testing.T) {
	for i, want := range map[sandbox.Isolation]string{sandbox.IsolationNone: "no isolation", sandbox.IsolationConfinement: "confinement",
		sandbox.IsolationContainer: "container", sandbox.IsolationMicroVM: "micro-VM"} {
		if got := isolation(i); got != want {
			t.Errorf("isolation(%v) = %q, want %q", i, got, want)
		}
	}
}
