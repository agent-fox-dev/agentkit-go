package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// project writes files under a fresh directory and returns it.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// do runs the program in process with the given standard input and arguments.
func do(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	err := run(context.Background(), strings.NewReader(stdin), &out, args)
	return out.String(), err
}

// builtins are the tools tools.For builds.
func builtins(t *testing.T) []core.Tool {
	t.Helper()
	ws, err := workspace.Open(t.TempDir(), workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func needGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH: find_references type-checks through it")
	}
}

// The tour runs as a program and in process, calls every built-in tool, shows
// each kind of failure as a result, leaves nothing behind, and ends with its
// completion line.
func TestTourCallsEveryBuiltInTool(t *testing.T) {
	needGo(t)
	leftovers := func() []string {
		m, _ := filepath.Glob(filepath.Join(os.TempDir(), "agentkit-toolbox-example-*"))
		return m
	}
	before := len(leftovers())

	progOut, err := exec.Command("go", "run", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, progOut)
	}
	out, err := do(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(progOut), "Toolbox example completed successfully") {
		t.Errorf("the program printed:\n%s", progOut)
	}

	for _, tl := range builtins(t) {
		if !strings.Contains(out, "\n$ "+tl.Name+" ") {
			t.Errorf("the tour never calls %s", tl.Name)
		}
	}
	for _, want := range []string{
		"main.go",                            // find_files, list_files
		"Showing lines 13-21 of 25",          // read_file's window and its continuation
		"func (g *Greeter) Greet() string",   // file_outline, find_symbol
		"(go/types, 2 references in 2 files", // find_references over the sample
		"resolved",                           // a reference the type checker confirmed
		"(go/types, 1 references in 1 files", // include_tests false drops one
		"Wrote 22 bytes to notes/hello.txt",  // write_file
		"Applied 1 edit to notes/todo.md",    // edit_file
		"error edit_not_unique",              // edit_file refuses an ambiguous text
		"hello from execute",                 // execute through guard.Allow("echo")
		"error blocked_by_policy",            // ... and refuses rm
		"\n  a;b\n",                          // run_command: no shell
		"error path_not_allowed",             // outside the workspace
		"error invalid_arguments",            // schema refusal
		"error unknown_tool",                 // no such tool
		"tools.Search: ",                     // the typed section
		"tools.References: 2 sites by the go/types backend",
		"Data[\"result\"] is a tools.ReferenceResult with 2 sites",
		"Toolbox example completed successfully",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the tour output lacks %q", want)
		}
	}
	if after := len(leftovers()); after != before {
		t.Errorf("%d sample projects before the tour, %d after: it must remove its own", before, after)
	}
}

// The tour writes files, so it takes no workspace and no arguments of its own.
func TestTourRefusesArgumentsAndTheFlagsOfOtherCommands(t *testing.T) {
	for _, args := range [][]string{
		{"tour", "extra"},
		{"--dir", ".", "tour"},
		{"--allow", "ls", "tour"},
	} {
		out, err := do(t, "", args...)
		if err == nil || !strings.Contains(err.Error(), "sample project of its own") {
			t.Errorf("%v: err = %v, want a refusal that names the sample project", args, err)
		}
		if out != "" {
			t.Errorf("%v: printed %q before refusing", args, out)
		}
	}
}

// list names every tool and every parameter of its input schema.
func TestListShowsEveryToolAndItsParameters(t *testing.T) {
	out, err := do(t, "", "--dir", t.TempDir(), "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range builtins(t) {
		if !strings.Contains("\n"+out, "\n"+tl.Name+"\n") {
			t.Errorf("list does not show %s", tl.Name)
		}
		for _, p := range propertyNames(tl.InputSchema) {
			if !strings.Contains(out, "    "+p) {
				t.Errorf("list does not show %s's parameter %s", tl.Name, p)
			}
		}
	}
	for _, want := range []string{"required", "optional", "integer", "boolean", "array"} {
		if !strings.Contains(out, want) {
			t.Errorf("list never says %q", want)
		}
	}
}

// KEY=VALUE pairs become the JSON the tool's schema asks for, so a number is a
// number and a string that looks like one is still a string.
func TestCallReadsKeyValuePairsByTheSchema(t *testing.T) {
	ws, err := workspace.Open(t.TempDir(), workspace.Options{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := newRunner(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		tool string
		args []string
		want string
	}{
		{"find_symbol", nil, `{}`},
		{"find_symbol", []string{`{"name":"x"}`}, `{"name":"x"}`},
		{"find_symbol", []string{"name=5"}, `{"name":"5"}`},
		{"find_symbol", []string{"name=Run", "exact=true", "max_results=5"}, `{"exact":true,"max_results":5,"name":"Run"}`},
		{"run_command", []string{`argv=["ls","-l"]`, "timeout_s=3"}, `{"argv":["ls","-l"],"timeout_s":3}`},
		{"search_files", []string{`pattern=a b`, "case_sensitive=false"}, `{"case_sensitive":false,"pattern":"a b"}`},
	} {
		got, err := inputFor(r, c.tool, c.args)
		if err != nil || string(got) != c.want {
			t.Errorf("%s %v: input %s, error %v, want %s", c.tool, c.args, got, err, c.want)
		}
	}
	for _, c := range []struct {
		tool string
		args []string
		want string
	}{
		{"find_symbol", []string{"bogus=1"}, `no parameter "bogus"; it takes name, kind, path, exact, max_results`},
		{"find_symbol", []string{"max_results=x"}, "want an integer"},
		{"find_symbol", []string{"exact=maybe"}, "want true or false"},
		{"run_command", []string{"argv=ls"}, "want JSON"},
		{"find_symbol", []string{"name"}, "neither a JSON object nor KEY=VALUE"},
		{"no_such_tool", []string{"a=b"}, `no tool "no_such_tool"`},
	} {
		if _, err := inputFor(r, c.tool, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: error %v, want one containing %q", c.tool, c.args, err, c.want)
		}
	}
}

// call runs one tool in --dir, prints what the model would read, and with
// --data the structured result too.
func TestCallRunsOneToolInTheWorkspace(t *testing.T) {
	dir := project(t, map[string]string{"notes.txt": "one\ntwo\nthree\n"})

	out, err := do(t, "", "--dir", dir, "call", "read_file", "path=notes.txt", "offset=2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `$ read_file {"offset":2,"path":"notes.txt"}`) || !strings.Contains(out, "\n  two\n  three\n") ||
		strings.Contains(out, "one") || strings.Contains(out, "data:") {
		t.Errorf("read_file from line 2 printed:\n%s", out)
	}

	out, err = do(t, "", "--dir", dir, "--data", "call", "read_file", `{"path":"notes.txt","limit":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "data: {") || !strings.Contains(out, `"content"`) {
		t.Errorf("--data printed no structured result:\n%s", out)
	}

	// A refused call is a result, and the program still succeeds.
	out, err = do(t, "", "--dir", dir, "call", "read_file", "path=../notes.txt")
	if err != nil || !strings.Contains(out, "error path_not_allowed") {
		t.Errorf("a path outside --dir: err %v, output:\n%s", err, out)
	}
}

// The shell tools are blocked until --allow names a program.
func TestShellToolsRunOnlyWhatIsAllowed(t *testing.T) {
	dir := t.TempDir()
	out, err := do(t, "", "--dir", dir, "call", "execute", "command=echo hi")
	if err != nil || !strings.Contains(out, "error blocked_by_policy") {
		t.Errorf("without --allow: err %v, output:\n%s", err, out)
	}
	out, err = do(t, "", "--dir", dir, "--allow", "echo,ls", "call", "execute", "command=echo hi")
	if err != nil || strings.Contains(out, "blocked_by_policy") || !strings.Contains(out, "\n  hi\n") {
		t.Errorf("with --allow echo: err %v, output:\n%s", err, out)
	}
	out, err = do(t, "", "--dir", dir, "--allow", "echo", "call", "execute", "command=ls")
	if err != nil || !strings.Contains(out, "error blocked_by_policy") {
		t.Errorf("ls with only echo allowed: err %v, output:\n%s", err, out)
	}
}

func TestCommandsThatAreWrong(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"call"}, "call needs a tool name"},
		{[]string{"nonsense"}, `unknown command "nonsense"`},
		{[]string{"call", "find_symbol", "bogus=1"}, `no parameter "bogus"`},
	} {
		if _, err := do(t, "", append([]string{"--dir", t.TempDir()}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %v, want one containing %q", c.args, err, c.want)
		}
	}
}

// The repl keeps one runner across lines: an error does not end it, quit does,
// and a file written on one line is found by the lookup on the next.
func TestReplKeepsOneRunnerAcrossLines(t *testing.T) {
	dir := project(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n"})
	input := strings.Join([]string{
		"find_symbol name=Widget",
		`write_file {"path":"widget.go","content":"package x\n\n// Widget is new.\ntype Widget struct{}\n"}`,
		"find_symbol name=Widget kind=type",
		"find_symbol bogus=1",
		"no_such_tool",
		"",
		"list_files",
		"quit",
		"list_files path=never-reached",
	}, "\n")
	out, err := do(t, input, "--dir", dir, "repl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"no symbols matched",
		"Wrote ",
		"widget.go:4  type  type Widget struct",
		`find_symbol has no parameter "bogus"`,
		"error unknown_tool",
		"\n  widget.go\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the repl output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "never-reached") {
		t.Errorf("the repl read a line after quit:\n%s", out)
	}
}

// The repl ends at the end of its input, as it does at quit.
func TestReplEndsAtTheEndOfInput(t *testing.T) {
	out, err := do(t, "list_files\n", "--dir", t.TempDir(), "repl")
	if err != nil || !strings.Contains(out, "$ list_files {}") {
		t.Errorf("err %v, output:\n%s", err, out)
	}
}
