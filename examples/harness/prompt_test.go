package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/core"
)

const (
	envelope = `{"ok":true,"summary":"specr: rendered 01_x","result":{"spec_dir":".specs/01_x","title":"X",` +
		`"task":{"id":%d,"title":"T"},"markdown":"# spec"},"next":[]}`
	testCommandsJSON = `{"test_commands":{"linter":"gofmt -l .","all_tests":"go test ./..."}}`
)

func envelopeFor(task int) string { return fmt.Sprintf(envelope, task) }

// Each step is one Runner.Call, so the configured guard sees it.
func TestTheSpecificationStepsRunInTheSandboxThroughTheGuard(t *testing.T) {
	r, hk, seen := fakeSandbox(t, map[string]core.ToolResult{"specr": ran("ok", 0, envelopeFor(1))}, testCommandsJSON)
	ctx := context.Background()
	render, err := renderSpec(ctx, r, "01", 1, 10000)
	if err != nil || render.Result.SpecDir != ".specs/01_x" || render.Result.Task.ID != 1 {
		t.Fatalf("renderSpec = %+v, %v", render, err)
	}
	tc, err := readTestCommands(ctx, r, render.Result.SpecDir)
	if err != nil || tc != (testCommands{Linter: "gofmt -l .", AllTests: "go test ./..."}) {
		t.Fatalf("readTestCommands = %+v, %v", tc, err)
	}
	if checks := runChecks(ctx, r, tc); len(checks) != 2 || !checks[0].Green() || !checks[1].Green() {
		t.Errorf("checks = %v", checks)
	}
	want := []string{"execute specr 01 --task 1 --max-tokens 10000", "read_file", "execute gofmt -l .", "execute go test ./..."}
	if !slices.Equal(*seen, want) || hk.calls.Load() != 4 {
		t.Errorf("the guard saw %q and the hooks counted %d; want %q", *seen, hk.calls.Load(), want)
	}
}

func TestWithNoTaskSpecrRendersTheNextOneNotDone(t *testing.T) {
	whole := `{"ok":true,"result":{"spec_dir":".specs/01_x"},"next":[{"flags":["--task","2"],"why":"next"}]}`
	r, _, seen := fakeSandbox(t, map[string]core.ToolResult{"specr 01 --task 2": ran("ok", 0, envelopeFor(2)), "specr 01": ran("ok", 0, whole)}, "")
	if render, err := renderSpec(context.Background(), r, "01", 0, 0); err != nil || render.Result.Task.ID != 2 {
		t.Errorf("renderSpec = %+v, %v", render, err)
	}
	if want := []string{"execute specr 01", "execute specr 01 --task 2"}; !slices.Equal(*seen, want) {
		t.Errorf("ran %q, want %q", *seen, want)
	}
}

func TestSpecrFailuresAreErrorsThatSayWhy(t *testing.T) {
	for want, answer := range map[string]core.ToolResult{
		"every task of .specs/01_x is done":          ran("ok", 0, `{"ok":true,"result":{"spec_dir":".specs/01_x"},"next":[]}`),
		"printed no envelope":                        ran("ok", 0, "not json"),
		"specr 01: command_exit: ":                   {Error: "command_exit", Data: map[string]any{"output": "boom"}},
		"bad (parse/syntax): line 3":                 ran("exit", 1, `{"ok":false,"summary":"bad","error":{"stage":"parse","category":"syntax","message":"line 3"}}`),
		"specr 01: nothing to render, no error part": ran("exit", 2, `{"ok":false,"summary":"nothing to render, no error part"}`),
	} {
		r, _, _ := fakeSandbox(t, map[string]core.ToolResult{"specr": answer}, "")
		if _, err := renderSpec(context.Background(), r, "01", 0, 0); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("renderSpec = %v, want an error saying %q", err, want)
		}
	}
}

func TestTestCommandsMustBeReadableAndComplete(t *testing.T) {
	for want, r := range map[string]*agentkit.Runner{
		"tasks.json: invalid character":             first(fakeSandbox(t, nil, "not json")),
		"test_commands needs linter and all_tests":  first(fakeSandbox(t, nil, `{"test_commands":{"linter":"gofmt -l ."}}`)),
		"read .specs/01_x/tasks.json: unknown_tool": mustRunnerWithout(t),
	} {
		if _, err := readTestCommands(context.Background(), r, ".specs/01_x/"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("readTestCommands = %v, want %q", err, want)
		}
	}
}

func first[T, U, V any](v T, _ U, _ V) T { return v }

// A check the guard refused, or whose call failed, could not run: the
// verdict treats that as a failed gate, never a pass.
func TestAChecksOutcomeIsWhatTheShellReportedOrWhyItCouldNotRun(t *testing.T) {
	r, _, _ := fakeSandbox(t, map[string]core.ToolResult{"go test": ran("exit", 1, "FAIL")}, "")
	got := runChecks(context.Background(), r, testCommands{Linter: "gofmt -l . | wc", AllTests: "go test ./..."})
	if !errors.Is(got[0].Err, errShellBlocked) || got[1].Outcome != "exit" || got[1].ExitCode != 1 || got[1].Output != "FAIL" {
		t.Fatalf("checks = %+v", got)
	}
	failed := runChecks(context.Background(), mustRunnerWithout(t), testCommands{Linter: "x", AllTests: "y"})
	for _, c := range append(got, failed...) {
		if c.Green() {
			t.Errorf("%v is green", c)
		}
	}
	for c, want := range map[check]string{
		got[0]: "linter `gofmt -l . | wc`: could not run: the guard refused the command: ",
		got[1]: "all_tests `go test ./...`: exit, exit 1",
		{Name: "linter", Command: "x", Outcome: "ok"}: "linter `x`: ok",
	} {
		if s := c.String(); !strings.HasPrefix(s, want) {
			t.Errorf("String = %q, want %q", s, want)
		}
	}
	if failed[0].Err == nil || errors.Is(failed[0].Err, errShellBlocked) {
		t.Errorf("a failed call reads %v, want an error that is not a block", failed[0].Err)
	}
}

// mustRunnerWithout is a runner with no execute tool, so every check's call fails.
func mustRunnerWithout(t *testing.T) *agentkit.Runner {
	t.Helper()
	r, _, _, err := buildRunner(nil, func(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{}
	}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTheVerdictIsTheGateTable(t *testing.T) {
	green := []check{{Name: "linter", Outcome: "ok"}, {Name: "all_tests", Outcome: "ok"}}
	red := []check{{Name: "linter", Outcome: "ok"}, {Name: "all_tests", Outcome: "exit", ExitCode: 1}}
	unrun := []check{{Name: "linter", Outcome: "ok"}, {Name: "all_tests", Err: errShellBlocked}}
	const never = "gate failed: a check could not run, so the work was never measured"
	for _, c := range []struct {
		before, after []check
		want          string
		landable      bool
	}{
		{green, green, "pass", true},
		{red, green, "pass, baseline was failing", true},
		{green, red, "regressed", false},
		{red, red, "still failing", false},
		{green, unrun, never, false},
		{unrun, green, never, false},
	} {
		if got, landable := verdict(c.before, c.after); got != c.want || landable != c.landable {
			t.Errorf("verdict(%v, %v) = %q, %v; want %q, %v", c.before, c.after, got, landable, c.want, c.landable)
		}
	}
}

func TestTheTaskPromptCarriesTheTaskTheBaselineAndTheRendering(t *testing.T) {
	var r specrResult
	if err := json.Unmarshal([]byte(envelopeFor(1)), &r); err != nil {
		t.Fatal(err)
	}
	baseline := []check{{Name: "linter", Command: "gofmt -l .", Outcome: "ok"},
		{Name: "all_tests", Command: "go test ./...", Outcome: "exit", ExitCode: 1, Output: strings.Repeat("x", 2000) + "the end\n"}}
	p := taskPrompt(r, baseline)
	for _, want := range []string{"Implement task 1 of the specification package .specs/01_x (X): T.",
		"- linter `gofmt -l .`: ok\n", "- all_tests `go test ./...`: exit, exit 1\n  output (tail):\n    …", "the end\n",
		"`specr .specs/01_x --task 1`", "---\n\n# spec"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Count(p, "x") > 1600 {
		t.Error("the prompt carries more than the tail of a failing check's output")
	}
}

func TestTheSystemPromptIsTheRulesTheAllowedProgramsAndTheToolsGuidelines(t *testing.T) {
	r, _, _ := fakeSandbox(t, nil, "")
	s := systemPrompt(r.Tools(), []string{"go", "git"})
	for _, want := range []string{"by the af-impl method", "The shell may run: go, git.", "Run one program per call."} {
		if !strings.Contains(s, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, s)
		}
	}
}

func TestNumReadsAJSONNumberInEveryFormItArrivesIn(t *testing.T) {
	for _, v := range []any{float64(3), 3, int64(3), json.Number("3")} {
		if num(v) != 3 {
			t.Errorf("num(%#v) = %d", v, num(v))
		}
	}
	if num("3") != 0 {
		t.Error("a string is not a number")
	}
}
