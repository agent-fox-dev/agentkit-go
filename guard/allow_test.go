package guard

import (
	"context"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

func execCall(cmd string) core.BeforeToolCallContext {
	return core.BeforeToolCallContext{ToolName: "execute", Arguments: map[string]any{"command": cmd}}
}

func argvCall(argv ...any) core.BeforeToolCallContext {
	return core.BeforeToolCallContext{ToolName: "run_command", Arguments: map[string]any{"argv": argv}}
}

// TS-22-7 (property): Allow decides exactly as Restricted with
// AllowedPrograms, for any programs and any context. 22-REQ-2.1.
func TestAllowDecidesAsRestricted_TS22_7(t *testing.T) {
	pool := []string{"ls", "cat", "go", "git", "/usr/bin/git", "./go", "", "  ls  ", "a/b"}
	commands := []string{"ls", "ls -l src", "cat f", "rm -rf src", "go test ./...", "ls | cat", "X=1 ls",
		"PATH=/x ls", "'ls' a", "./ls", "/usr/bin/git log", "", "echo $(ls)", "ls; rm x", "git log \"$HOME\"", "ls 'a"}
	argvs := [][]any{{"ls"}, {"rm", "x"}, {"/usr/bin/git", "log"}, {"./go"}, {}, {"ls", 1}, {1}}
	rng := rand.New(rand.NewPCG(22, 7))
	blocked := 0
	for range 500 {
		var programs []string
		for range rng.IntN(5) {
			programs = append(programs, pool[rng.IntN(len(pool))])
		}
		allow := Allow(programs...)
		restricted := Restricted(Options{AllowedPrograms: programs})
		calls := []core.BeforeToolCallContext{
			execCall(commands[rng.IntN(len(commands))]),
			argvCall(argvs[rng.IntN(len(argvs))]...),
			{ToolName: "read_file", Arguments: map[string]any{"path": "a"}},
			{ToolName: "no_such_tool"},
		}
		for _, in := range calls {
			got, want := allow(context.Background(), in), restricted(context.Background(), in)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("programs %q, %s %v: Allow decided %+v, Restricted %+v", programs, in.ToolName, in.Arguments, got, want)
			}
			if got.Block {
				blocked++
				if !strings.HasPrefix(got.Reason, "guard.Restricted: ") {
					t.Fatalf("reason %q does not begin %q", got.Reason, "guard.Restricted: ")
				}
			}
		}
	}
	if blocked == 0 {
		t.Fatal("no generated call was blocked: the property compared nothing")
	}
}

// TS-22-8: changing the caller's slice after Allow returns changes no
// decision. 22-REQ-2.2.
func TestAllowIgnoresLaterChangesToTheSlice_TS22_8(t *testing.T) {
	programs := make([]string, 2, 8)
	programs[0], programs[1] = "ls", "cat"
	g := Allow(programs...)

	programs[1] = "rm"
	programs = append(programs, "curl")
	programs[0], programs[2] = programs[2], programs[0]

	call := func(cmd string) core.BeforeToolCallDecision { return g(context.Background(), execCall(cmd)) }
	for _, cmd := range []string{"rm x", "curl u"} {
		if !call(cmd).Block {
			t.Errorf("%q is allowed after the caller changed its slice", cmd)
		}
	}
	for _, cmd := range []string{"ls", "cat f"} {
		if d := call(cmd); d.Block {
			t.Errorf("%q is blocked after the caller changed its slice: %s", cmd, d.Reason)
		}
	}
}

// TS-22-9: with no programs, every shell call is blocked and every other
// tool is allowed. 22-REQ-2.3.
func TestAllowWithNoPrograms_TS22_9(t *testing.T) {
	g := Allow()
	for _, in := range []core.BeforeToolCallContext{execCall("ls"), argvCall("ls")} {
		if !g(context.Background(), in).Block {
			t.Errorf("%s is allowed under an empty allowlist", in.ToolName)
		}
	}
	for _, name := range []string{"read_file", "no_such_tool"} {
		if d := g(context.Background(), core.BeforeToolCallContext{ToolName: name}); d.Block {
			t.Errorf("%s is blocked: %s", name, d.Reason)
		}
	}
}
