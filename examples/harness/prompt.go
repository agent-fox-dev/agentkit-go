package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/prompt"
)

// The instructions the harness works under, and the spec rendering it works
// from. The method is agent-fox's af-impl skill, condensed to the part a
// single task needs: scope, test first, the spec's own checks as the gate,
// and a spec that is never edited. The host does what the skill's other steps
// do around the task — measures the baseline before and the gate after — so
// the model is asked only for the task.

// afImplRules is appended to the harness's system prompt. The tool
// guidelines of the served tools follow it (prompt.Build).
const afImplRules = `You are implementing one task of a version 2 specification package, by the af-impl method.

The repository is in a sandbox. Your only tools are the sandbox's: code_mode runs a Starlark script that calls the built-in tools as functions, and each built-in tool can also be called directly. Paths are relative to the workspace root. The shell (execute, run_command) runs only the programs the embedder allowed; a refused command comes back as blocked_by_policy, and retrying it differently does not help.

Rules:
1. Scope. The task is the unit of work: implement what it owns and nothing else. Other requirements and tasks are context by id only. No unrelated improvements.
2. Orient. Read the files the task touches and their tests before changing anything. Paths in the spec are what its author expected; confirm them against the code.
3. Test first. Write every test the task owns from its entry in the test specification (given, when, then, pseudocode), in the project's own framework and layout, with the test id in the function name or a comment. Run them and see them fail BEFORE any implementation file changes. If they cannot compile until the task adds a signature, add the minimal stub first, then see them fail on behaviour. An unwanted criterion's test asserts the outcome its contract names, not merely that an error occurred.
4. Implement the task's steps, in order, until the tests pass. Follow the conventions the files you edit already use.
5. Check, narrowly, then run the spec's test_commands (linter, then all_tests) as the gate. Compare with the baseline in the task prompt: a task lands only when nothing regressed.
6. Documentation. Update what the change makes wrong, from the code.
7. Never edit the spec package, except to set this task's "state" in tasks.json to "done" once the gate passed. A spec that is wrong gets an erratum under docs/errata/ and a declared deviation, not a quiet fix.
8. Do not commit, push or branch: the embedder exports the workspace and lands the work.

Never skip, delete or weaken a test to get green. Never claim a check you did not run; report what each command returned.

When you are done, report: the tests you wrote and how they failed first, the commands you ran and what they returned, the files you changed, and every deviation from the spec.`

// systemPrompt is the text appended to the harness's own system prompt: the
// rules, the allowed programs, and the served tools' own guidelines, which
// travel with the tools in AgentKit and here follow them to another driver.
func systemPrompt(served []core.Tool, allowed []string) string {
	rules := afImplRules + "\n\nThe shell may run: " + strings.Join(allowed, ", ") + "."
	return prompt.Build(rules, served)
}

// specrResult is the part of specr's envelope this program reads. The whole
// envelope is documented in the spec repository's docs/specr.md.
type specrResult struct {
	OK       bool   `json:"ok"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Summary  string `json:"summary"`
	Error    *struct {
		Stage    string `json:"stage"`
		Category string `json:"category"`
		Message  string `json:"message"`
	} `json:"error"`
	Result struct {
		SpecDir  string `json:"spec_dir"`
		SpecID   string `json:"spec_id"`
		SpecName string `json:"spec_name"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		Valid    bool   `json:"valid"`
		Scope    string `json:"scope"`
		Task     *struct {
			ID    int    `json:"id"`
			Title string `json:"title"`
			Kind  string `json:"kind"`
			State string `json:"state"`
		} `json:"task"`
		Tokens int `json:"tokens"`
		Tasks  []struct {
			ID    int    `json:"id"`
			Title string `json:"title"`
			State string `json:"state"`
		} `json:"tasks"`
		Markdown string `json:"markdown"`
	} `json:"result"`
	Next []struct {
		Flags []string `json:"flags"`
		Why   string   `json:"why"`
	} `json:"next"`
}

// renderSpec runs specr in the sandbox over the proxy execute tool, under
// the guard like every other call, and parses its envelope. task 0 renders
// the whole package and takes the task its next entry names: the first task
// not done. maxTokens bounds the render, because the shell tool keeps only
// the last 50 KB of a command's output and a cut envelope is no envelope.
func renderSpec(ctx context.Context, s *toolServer, spec string, task, maxTokens int) (specrResult, error) {
	run := func(task int) (specrResult, error) {
		cmd := "specr " + spec
		if task > 0 {
			cmd += " --task " + strconv.Itoa(task)
		}
		if maxTokens > 0 {
			cmd += " --max-tokens " + strconv.Itoa(maxTokens)
		}
		res := s.direct(ctx, "execute", json.RawMessage(fmt.Sprintf(`{"command":%q,"timeout_s":60}`, cmd)))
		var out specrResult
		// specr prints its envelope on every program-driven path, exit 1
		// and 2 included, so the output is read before the exit status.
		output, _ := res.Data["output"].(string)
		if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &out); err != nil {
			if !res.OK {
				return out, fmt.Errorf("%s: %s: %s", cmd, res.Error, res.Detail)
			}
			return out, fmt.Errorf("%s printed no envelope: %w\n%s", cmd, err, truncate(output, 2000))
		}
		if !out.OK {
			msg := out.Summary
			if out.Error != nil {
				msg = fmt.Sprintf("%s (%s/%s): %s", out.Summary, out.Error.Stage, out.Error.Category, out.Error.Message)
			}
			return out, fmt.Errorf("%s: %s", cmd, msg)
		}
		return out, nil
	}
	if task > 0 {
		return run(task)
	}
	whole, err := run(0)
	if err != nil {
		return whole, err
	}
	for _, n := range whole.Next {
		for i := 0; i+1 < len(n.Flags); i++ {
			if n.Flags[i] == "--task" {
				if t, err := strconv.Atoi(n.Flags[i+1]); err == nil {
					return run(t)
				}
			}
		}
	}
	return whole, fmt.Errorf("specr %s: every task of %s is done; nothing to implement", spec, whole.Result.SpecDir)
}

// testCommands is tasks.json's test_commands: the spec's own checks.
type testCommands struct {
	Linter   string `json:"linter"`
	AllTests string `json:"all_tests"`
}

// readTestCommands reads them from the package's tasks.json through the
// proxy read_file tool.
func readTestCommands(ctx context.Context, s *toolServer, specDir string) (testCommands, error) {
	path := strings.TrimSuffix(specDir, "/") + "/tasks.json"
	res := s.direct(ctx, "read_file", json.RawMessage(fmt.Sprintf(`{"path":%q}`, path)))
	if !res.OK {
		return testCommands{}, fmt.Errorf("read %s: %s: %s", path, res.Error, res.Detail)
	}
	content, _ := res.Data["content"].(string)
	var tasks struct {
		TestCommands testCommands `json:"test_commands"`
	}
	if err := json.Unmarshal([]byte(content), &tasks); err != nil {
		return testCommands{}, fmt.Errorf("%s: %w", path, err)
	}
	if tasks.TestCommands.Linter == "" || tasks.TestCommands.AllTests == "" {
		return testCommands{}, fmt.Errorf("%s: test_commands needs linter and all_tests", path)
	}
	return tasks.TestCommands, nil
}

// check is one run of a spec check.
type check struct {
	Name, Command string
	Outcome       string // the shell tool's outcome: ok, exit, signal, timeout, abort
	ExitCode      int
	Output        string
	Err           error // the check could not run: the guard refused it, or the call failed
}

// Green is a check that ran and passed.
func (c check) Green() bool { return c.Err == nil && c.Outcome == "ok" }

// String is the one-line account of the check.
func (c check) String() string {
	switch {
	case c.Err != nil:
		return fmt.Sprintf("%s `%s`: could not run: %v", c.Name, c.Command, c.Err)
	case c.Green():
		return fmt.Sprintf("%s `%s`: ok", c.Name, c.Command)
	}
	return fmt.Sprintf("%s `%s`: %s, exit %d", c.Name, c.Command, c.Outcome, c.ExitCode)
}

// runChecks runs the linter, then all_tests, in the sandbox through the
// guarded execute tool. A command the guard refuses (a shell operator, a
// program not allowed) is a check that could not run, which the verdict
// treats as a failed gate, never as a pass.
func runChecks(ctx context.Context, s *toolServer, tc testCommands) []check {
	out := []check{{Name: "linter", Command: tc.Linter}, {Name: "all_tests", Command: tc.AllTests}}
	for i := range out {
		res := s.direct(ctx, "execute", json.RawMessage(fmt.Sprintf(`{"command":%q,"timeout_s":900}`, out[i].Command)))
		// A command that ran and failed is an error result (command_exit,
		// command_timeout) whose Data still carries the outcome, the exit
		// code and the output. A result with no outcome never ran.
		out[i].Outcome, _ = res.Data["outcome"].(string)
		if out[i].Outcome == "" {
			if res.Error == core.BlockErrorCode {
				out[i].Err = fmt.Errorf("%w: %s", errShellBlocked, res.Detail)
			} else {
				out[i].Err = errors.New(res.Error + ": " + res.Detail)
			}
			continue
		}
		out[i].ExitCode = num(res.Data["exit_code"])
		out[i].Output, _ = res.Data["output"].(string)
	}
	return out
}

// verdict is af-impl's gate table over the baseline and the run after the
// task. Only the first two rows land.
func verdict(before, after []check) (text string, landable bool) {
	green := func(cs []check) (ran, ok bool) {
		ran, ok = true, true
		for _, c := range cs {
			if c.Err != nil {
				ran = false
			}
			if !c.Green() {
				ok = false
			}
		}
		return ran, ok
	}
	bRan, bGreen := green(before)
	aRan, aGreen := green(after)
	switch {
	case !bRan || !aRan:
		return "gate failed: a check could not run, so the work was never measured", false
	case bGreen && aGreen:
		return "pass", true
	case !bGreen && aGreen:
		return "pass, baseline was failing", true
	case bGreen && !aGreen:
		return "regressed", false
	}
	return "still failing", false
}

// taskPrompt is the harness's prompt: what to implement, the baseline the
// host measured, and specr's rendering of the package scoped to the task.
func taskPrompt(r specrResult, baseline []check) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Implement task %d of the specification package %s (%s): %s.\n\n",
		r.Result.Task.ID, r.Result.SpecDir, r.Result.Title, r.Result.Task.Title)
	fmt.Fprintf(&b, "The package is at %s in the workspace; read its artefacts there when the rendering below is not enough.\n\n", r.Result.SpecDir)
	b.WriteString("Baseline, measured on the unchanged tree before you started (the gate compares against it):\n")
	for _, c := range baseline {
		fmt.Fprintf(&b, "- %s\n", c)
		if !c.Green() && c.Err == nil && strings.TrimSpace(c.Output) != "" {
			fmt.Fprintf(&b, "  output (tail):\n%s\n", indent(tail(c.Output, 1500)))
		}
	}
	fmt.Fprintf(&b, "\nThe rendering below is `specr %s --task %d`: the requirements that own the task's criteria, its tests and the paths they verify in full, everything else by id.\n\n---\n\n%s",
		r.Result.SpecDir, r.Result.Task.ID, r.Result.Markdown)
	return b.String()
}

// num reads a JSON number that arrived as float64 over the wire, or an int
// from an in-process tool.
func num(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
