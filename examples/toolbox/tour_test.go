package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// The tour calls every built-in tool on its own sample project, shows each
// kind of failure as a result, removes the project, and ends with its
// completion line.
func TestTheTourCallsEveryBuiltInToolAndRemovesItsProject(t *testing.T) {
	needGo(t)
	leftovers := func() int {
		m, _ := filepath.Glob(filepath.Join(os.TempDir(), "agentkit-toolbox-example-*"))
		return len(m)
	}
	before := leftovers()
	out, err := do(t, "")
	if err != nil {
		t.Fatal(err)
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
	if after := leftovers(); after != before {
		t.Errorf("%d sample projects before the tour, %d after", before, after)
	}
}

func TestTheProgramRunsTheTourByDefault(t *testing.T) {
	needGo(t)
	if res := testutil.GoRun(t, "", ".", testutil.Environ()); res.Exit != 0 || !strings.Contains(res.Stdout, "Toolbox example completed successfully") {
		t.Fatalf("go run exited %d:\n%s\n%s", res.Exit, res.Stdout, res.Stderr)
	}
}

// The tour writes files, so it takes no workspace and no arguments of its own.
func TestTheTourRefusesArgumentsAndTheFlagsOfOtherCommands(t *testing.T) {
	for _, args := range [][]string{{"tour", "extra"}, {"--dir", ".", "tour"}, {"--allow", "ls", "tour"}} {
		if out, err := do(t, "", args...); err == nil || !strings.Contains(err.Error(), "sample project of its own") || out != "" {
			t.Errorf("%v: printed %q and returned %v, want a refusal that names the sample project", args, out, err)
		}
	}
}
