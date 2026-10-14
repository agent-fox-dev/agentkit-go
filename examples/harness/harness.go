package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/mcp"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
)

// The harnesses. Each is started with the task prompt, the appended system
// prompt, and the MCP server as its only tool source; each returns what the
// model said at the end. The sandbox, the guard and the credential never
// meet: the harness holds the model credential and talks to the server on
// loopback, the server holds the sandbox, and the sandbox holds neither.

// harnessResult is what a harness run came back with.
type harnessResult struct {
	Harness  string
	ExitCode int
	Final    string  // the model's final text
	CostUSD  float64 // when the harness reports it
	Turns    int     // when the harness reports it
}

// runHarness dispatches on name: claude, pi or scripted.
func runHarness(ctx context.Context, log io.Writer, name string, m *servedMCP, system, task string, o options) (harnessResult, error) {
	switch name {
	case "claude":
		return runClaude(ctx, log, m, system, task, o)
	case "pi":
		return runPi(ctx, log, m, system, task, o)
	case "scripted":
		return runScripted(ctx, log, m, system, task, o)
	}
	return harnessResult{}, fmt.Errorf("unknown harness %q: claude, pi or scripted", name)
}

// runClaude runs Claude Code headless. --tools "" turns its own tools off
// (Read, Edit, Bash, …), --strict-mcp-config with --mcp-config makes this
// server its only MCP server, --allowedTools mcp__agentkit pre-approves the
// server's tools and --permission-mode dontAsk denies anything that would
// still prompt, so an unattended run never blocks on a question. The output
// is one JSON object: the final text, the cost and the turn count. It runs
// in an empty directory, so nothing of this program's own tree is a
// workspace for it; with its tools off, it could not reach one anyway.
func runClaude(ctx context.Context, log io.Writer, m *servedMCP, system, task string, o options) (harnessResult, error) {
	dir, systemFile, err := harnessDir(system)
	if err != nil {
		return harnessResult{}, err
	}
	defer os.RemoveAll(dir)
	stdout, code, err := runCommand(ctx, log, dir, "claude", claudeArgs(m.mcpConfig(false), systemFile, task, o)...)
	if err != nil {
		return harnessResult{}, err
	}
	res := harnessResult{Harness: "claude", ExitCode: code, Final: strings.TrimSpace(string(stdout))}
	var out struct {
		Result    string  `json:"result"`
		CostUSD   float64 `json:"total_cost_usd"`
		Turns     int     `json:"num_turns"`
		IsError   bool    `json:"is_error"`
		Subtype   string  `json:"subtype"`
		SessionID string  `json:"session_id"`
	}
	if json.Unmarshal(stdout, &out) == nil && (out.Result != "" || out.Subtype != "") {
		res.Final, res.CostUSD, res.Turns = out.Result, out.CostUSD, out.Turns
		if out.IsError && res.Final == "" {
			res.Final = "claude: " + out.Subtype
		}
	}
	return res, nil
}

// claudeArgs is Claude Code's command line: the prompt, no built-in tools,
// this MCP server alone, its tools pre-approved, no prompts, the rules
// appended, bounded turns, JSON out and no session left behind.
func claudeArgs(mcpConfig, systemFile, task string, o options) []string {
	args := []string{"-p", task,
		"--tools", "",
		"--strict-mcp-config", "--mcp-config", mcpConfig,
		"--allowedTools", "mcp__agentkit",
		"--permission-mode", "dontAsk",
		"--append-system-prompt-file", systemFile,
		"--max-turns", strconv.Itoa(o.MaxTurns),
		"--output-format", "json",
		"--no-session-persistence",
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	return args
}

// piArgs is Pi's command line: print mode, this server's tools only, no
// skills, the rules appended, then the prompt after `--` so a prompt that
// starts with a dash is still a prompt.
func piArgs(systemFile, task string, o options) []string {
	args := []string{"-p",
		"--tools", "mcp__agentkit__*",
		"--no-skills",
		"--append-system-prompt", systemFile,
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	return append(args, "--", task)
}

// runPi runs Pi in print mode. Pi takes MCP servers from a config file, not
// a flag: the project file .pi/mcp.json of the directory it runs in, read
// once the project is trusted. --tools 'mcp__agentkit__*' keeps only this
// server's tools, which drops Pi's own (read, bash, edit, write, …); the
// server's exposure is set to direct so the tools are declared to the model
// rather than reached only through Pi's own code mode. Verified against
// Pi's documentation, not run here.
func runPi(ctx context.Context, log io.Writer, m *servedMCP, system, task string, o options) (harnessResult, error) {
	dir, systemFile, err := harnessDir(system)
	if err != nil {
		return harnessResult{}, err
	}
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Join(dir, ".pi"), 0o700); err != nil {
		return harnessResult{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, ".pi", "mcp.json"), []byte(m.mcpConfig(true)), 0o600); err != nil {
		return harnessResult{}, err
	}
	stdout, code, err := runCommand(ctx, log, dir, "pi", piArgs(systemFile, task, o)...)
	if err != nil {
		return harnessResult{}, err
	}
	return harnessResult{Harness: "pi", ExitCode: code, Final: strings.TrimSpace(string(stdout))}, nil
}

// runScripted is the stand-in for a harness, and the proof that the server
// serves any MCP client: AgentKit's own driver, with the server's tools
// imported through AgentKit's MCP client exactly as another embedder's
// would be, and provider/faux replaying one code_mode call that implements
// the sample task the af-impl way — the test first and red, the
// implementation, the test green, the linter, the task's state. No model
// credential, no network beyond loopback. The driver needs no Guard: the
// imported tools are agentkit__execute and friends, and the guard is the
// server's.
func runScripted(ctx context.Context, log io.Writer, m *servedMCP, system, task string, o options) (harnessResult, error) {
	pool := mcp.NewPool(mcp.ConnectionOptions{Warnf: func(format string, args ...any) { fmt.Fprintf(log, "  mcp: "+format+"\n", args...) }})
	defer pool.Close()
	cfg := mcp.ServerConfig{Name: "agentkit", URL: m.URL, Headers: map[string]string{"Authorization": "Bearer " + m.Token}}
	if _, err := pool.Connect(ctx, cfg, nil, nil); err != nil {
		return harnessResult{}, fmt.Errorf("connect to the MCP server: %w", err)
	}
	imported, err := pool.Tools(ctx, nil)
	if err != nil {
		return harnessResult{}, err
	}
	var names []string
	for _, t := range imported {
		names = append(names, t.Name)
	}
	fmt.Fprintf(log, "  scripted harness imported %d tools over MCP: %s\n", len(imported), strings.Join(names, ", "))

	script, _ := json.Marshal(map[string]string{"script": scriptedTask})
	agent, err := agentkit.New(agentkit.Config{
		Provider: faux.New(
			faux.Turn{Blocks: []core.ContentBlock{faux.FauxToolCall("call_1", "agentkit__code_mode", string(script))}, StopReason: core.StopReasonToolUse},
			faux.Turn{Blocks: []core.ContentBlock{faux.FauxText(
				"Task 1 is done. TS-01-1 was written first and failed (go test exit 1); Greet now returns the name's greeting, " +
					"or \"Hello, stranger!\" for an empty name; go test ./... passes and gofmt -l . prints nothing; " +
					"tasks.json marks task 1 done. No deviations.")}, StopReason: core.StopReasonStop},
		),
		Model:    faux.Model().ID,
		System:   system,
		Tools:    imported,
		MaxTurns: o.MaxTurns,
	})
	if err != nil {
		return harnessResult{}, err
	}
	run, err := agent.Run(ctx, task)
	if err != nil {
		return harnessResult{}, err
	}
	for _, msg := range run.Messages {
		if tr, ok := msg.(core.ToolResultMessage); ok {
			fmt.Fprintf(log, "\n  %s result:\n%s\n\n", tr.ToolName, indent(strings.TrimSpace(tr.Content.Text())))
		}
	}
	return harnessResult{Harness: "scripted", Final: run.FinalText(), Turns: run.TurnCount}, nil
}

// scriptedTask is the scripted model's one code_mode call: the sample task,
// the af-impl way, over the proxy tools inside the sandbox.
const scriptedTask = `test = '''package greeter

import "testing"

// TS-01-1: Greet names the caller, and a stranger when there is no name.
func TestGreet_TS01_1(t *testing.T) {
	for name, want := range map[string]string{"Ada": "Hello, Ada!", "": "Hello, stranger!"} {
		if got := Greet(name); got != want {
			t.Errorf("Greet(%q) = %q, want %q", name, got, want)
		}
	}
}
'''
write_file(path="greeter_test.go", content=test)

def status(r):
    # A command that exits non-zero is a tool_error whose data still holds
    # the outcome, the exit code and the output.
    d = r.data if is_error(r) else r
    return d["outcome"], d["exit_code"]

red = execute(command="go test ./...", timeout_s=600)
print("TS-01-1 before the implementation: outcome %s, exit %d" % status(red))

edit_file(path="greeter.go", edits=[{
    "old_string": 'panic("not implemented")',
    "new_string": 'if name == "" {\n\t\tname = "stranger"\n\t}\n\treturn "Hello, " + name + "!"',
}])
green = execute(command="go test ./...", timeout_s=600)
print("TS-01-1 after: outcome %s, exit %d" % status(green))
lint = execute(command="gofmt -l .")
print("gofmt -l . printed:", repr(lint["output"].strip()))

if status(green)[0] == "ok" and lint["output"].strip() == "":
    edit_file(path=".specs/01_greeting/tasks.json", edits=[{"old_string": '"state": "pending"', "new_string": '"state": "done"'}])
    print("tasks.json: task 1 done")

def main():
    return {"red_exit": status(red)[1], "green_exit": status(green)[1]}
`

// harnessDir makes the empty directory a harness runs in, with the appended
// system prompt in a file beside nothing else.
func harnessDir(system string) (dir, systemFile string, err error) {
	dir, err = os.MkdirTemp("", "agentkit-harness-")
	if err != nil {
		return "", "", err
	}
	systemFile = filepath.Join(dir, "system-prompt.md")
	if err := os.WriteFile(systemFile, []byte(system), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return dir, systemFile, nil
}

// runCommand runs a harness command in dir with this process's environment
// — that is where its model credential comes from — relaying its stderr to
// log and returning its stdout and exit status. A missing command is an
// error that says so; a non-zero exit is a status, because the gate decides
// what the run was worth.
func runCommand(ctx context.Context, log io.Writer, dir, name string, args ...string) ([]byte, int, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, 0, fmt.Errorf("the %s command is not on PATH; install it, or run with --harness scripted", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = log
	fmt.Fprintf(log, "  starting %s with %d arguments in %s\n", name, len(args), dir)
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), 0, nil
	case errors.As(err, &exit):
		return stdout.Bytes(), exit.ExitCode(), nil
	}
	return stdout.Bytes(), -1, fmt.Errorf("%s: %w", name, err)
}
