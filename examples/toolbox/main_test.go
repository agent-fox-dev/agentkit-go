package main

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

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
		want string // the input, or the error it fails with
	}{
		{"find_symbol", nil, `{}`},
		{"find_symbol", []string{`{"name":"x"}`}, `{"name":"x"}`},
		{"find_symbol", []string{"name=5"}, `{"name":"5"}`},
		{"find_symbol", []string{"name=Run", "exact=true", "max_results=5"}, `{"exact":true,"max_results":5,"name":"Run"}`},
		{"run_command", []string{`argv=["ls","-l"]`, "timeout_s=3"}, `{"argv":["ls","-l"],"timeout_s":3}`},
		{"search_files", []string{`pattern=a b`, "case_sensitive=false"}, `{"case_sensitive":false,"pattern":"a b"}`},
		{"find_symbol", []string{"bogus=1"}, `no parameter "bogus"; it takes name, kind, path, exact, max_results`},
		{"find_symbol", []string{"max_results=x"}, "want an integer"},
		{"find_symbol", []string{"exact=maybe"}, "want true or false"},
		{"run_command", []string{"argv=ls"}, "want JSON"},
		{"find_symbol", []string{"name"}, "neither a JSON object nor KEY=VALUE"},
		{"no_such_tool", []string{"a=b"}, `no tool "no_such_tool"`},
	} {
		got, err := inputFor(r, c.tool, c.args)
		if err != nil && !strings.Contains(err.Error(), c.want) || err == nil && string(got) != c.want {
			t.Errorf("%s %v: input %s, error %v, want %s", c.tool, c.args, got, err, c.want)
		}
	}
	if raw, err := encodeValue("number", "2.5"); err != nil || string(raw) != "2.5" {
		t.Errorf("a number: %s, %v", raw, err)
	}
	if _, err := encodeValue("number", "x"); err == nil {
		t.Error("a number that is not one was accepted")
	}
}

// call runs one tool in --dir and prints what the model would read; --data
// adds the structured result, and a refused call is a result, not a failure.
func TestCallRunsOneToolInTheWorkspace(t *testing.T) {
	dir := testutil.Tree(t, map[string]string{"notes.txt": "one\ntwo\nthree\n"})
	for _, c := range []struct {
		args       []string
		want, none []string
	}{
		{[]string{"call", "read_file", "path=notes.txt", "offset=2"}, []string{`$ read_file {"offset":2,"path":"notes.txt"}`, "\n  two\n  three\n"}, []string{"one", "data:"}},
		{[]string{"--data", "call", "read_file", `{"path":"notes.txt","limit":1}`}, []string{"data: {", `"content"`}, nil},
		{[]string{"call", "read_file", "path=../notes.txt"}, []string{"error path_not_allowed"}, nil},
	} {
		out, err := do(t, "", append([]string{"--dir", dir}, c.args...)...)
		for _, want := range c.want {
			if err != nil || !strings.Contains(out, want) {
				t.Errorf("%v: %v, output lacks %q:\n%s", c.args, err, want, out)
			}
		}
		for _, none := range c.none {
			if strings.Contains(out, none) {
				t.Errorf("%v: output has %q:\n%s", c.args, none, out)
			}
		}
	}
}

// The shell tools are blocked until --allow names a program.
func TestTheShellToolsRunOnlyWhatIsAllowed(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		allow, command, want string
	}{
		{"", "echo hi", "error blocked_by_policy"},
		{"echo,ls", "echo hi", "\n  hi\n"},
		{"echo", "ls", "error blocked_by_policy"},
	} {
		if out, err := do(t, "", "--dir", dir, "--allow", c.allow, "call", "execute", "command="+c.command); err != nil || !strings.Contains(out, c.want) {
			t.Errorf("--allow %q, %s: %v, output lacks %q:\n%s", c.allow, c.command, err, c.want, out)
		}
	}
}

func TestAWrongCommandIsAnErrorAndHelpPrintsTheUsage(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"call"}, "call needs a tool name"},
		{[]string{"nonsense"}, `unknown command "nonsense"`},
		{[]string{"call", "find_symbol", "bogus=1"}, `no parameter "bogus"`},
		{[]string{"--bogus"}, "flag provided but not defined"},
	} {
		if _, err := do(t, "", append([]string{"--dir", t.TempDir()}, c.args...)...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: error %v, want one containing %q", c.args, err, c.want)
		}
	}
	if out, err := do(t, "", "--help"); !errors.Is(err, flag.ErrHelp) || !strings.HasPrefix(out, "usage: toolbox") {
		t.Errorf("--help: %v\n%s", err, out)
	}
}

// The repl keeps one runner across lines: an error does not end it, quit does,
// and a file written on one line is found by the lookup on the next.
func TestTheReplKeepsOneRunnerAcrossLines(t *testing.T) {
	dir := testutil.Tree(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n"})
	input := strings.Join([]string{
		"find_symbol name=Widget",
		`write_file {"path":"widget.go","content":"package x\n\n// Widget is new.\ntype Widget struct{}\n"}`,
		"find_symbol name=Widget kind=type",
		"find_symbol bogus=1",
		"no_such_tool",
		"",
		"list",
		"list_files",
		"quit",
		"list_files path=never-reached",
	}, "\n")
	out, err := do(t, input, "--dir", dir, "repl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no symbols matched", "Wrote ", "widget.go:4  type  type Widget struct",
		`find_symbol has no parameter "bogus"`, "error unknown_tool", "\nrun_command\n", "\n  widget.go\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("the repl output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "never-reached") {
		t.Errorf("the repl read a line after quit:\n%s", out)
	}
}

func TestTheReplEndsAtTheEndOfItsInput(t *testing.T) {
	if out, err := do(t, "list_files\n", "--dir", t.TempDir(), "repl"); err != nil || !strings.Contains(out, "$ list_files {}") {
		t.Errorf("err %v, output:\n%s", err, out)
	}
}
