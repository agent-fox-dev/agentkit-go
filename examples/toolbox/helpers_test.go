package main

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

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
