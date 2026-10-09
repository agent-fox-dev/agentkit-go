package guard

import (
	"context"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// TestRestrictedPolicy pins the reference interceptor's decisions.
func TestRestrictedPolicy(t *testing.T) {
	p := Restricted(Options{AllowedPrograms: []string{"go", "/usr/bin/git"}})
	call := func(tool string, args map[string]any) core.BeforeToolCallDecision {
		return p(context.Background(), core.BeforeToolCallContext{ToolName: tool, Arguments: args})
	}
	cases := []struct {
		tool  string
		args  map[string]any
		block bool
		why   string
	}{
		{"execute", map[string]any{"command": "go test ./..."}, false, "allowed program"},
		{"execute", map[string]any{"command": "GOFLAGS=-mod=mod go build"}, true, "env prefix refused by default"},
		{"execute", map[string]any{"command": "PATH=/tmp/x go test"}, true, "PATH prefix chooses the binary"},
		{"execute", map[string]any{"command": "./go test"}, true, "relative path named after an allowed program"},
		{"execute", map[string]any{"command": "/tmp/go test"}, true, "absolute path named after an allowed program"},
		{"execute", map[string]any{"command": "'./go' test"}, true, "quoted relative path"},
		{"execute", map[string]any{"command": "/usr/bin/git log"}, false, "the exact allowlisted path"},
		{"execute", map[string]any{"command": "/opt/bin/git log"}, true, "another path with an allowlisted basename"},
		{"run_command", map[string]any{"argv": []any{"./go", "test"}}, true, "argv relative path named after an allowed program"},
		{"run_command", map[string]any{"argv": []any{"/usr/bin/git", "log"}}, false, "argv exact allowlisted path"},
		{"execute", map[string]any{"command": "go vet 'a;b'"}, false, "operator inside single quotes"},
		{"execute", map[string]any{"command": "git log"}, true, "a bare name is not admitted by a listed path"},
		{"execute", map[string]any{"command": "go test | tee out"}, true, "pipe"},
		{"execute", map[string]any{"command": "go test; rm -rf /"}, true, "list operator"},
		{"execute", map[string]any{"command": "echo $(whoami)"}, true, "command substitution"},
		{"execute", map[string]any{"command": "git log \"$HOME\""}, true, "expansion inside double quotes"},
		{"execute", map[string]any{"command": "rm -rf /"}, true, "program not allowed"},
		{"execute", map[string]any{"command": ""}, true, "empty"},
		{"run_command", map[string]any{"argv": []any{"go", "vet", "a;b"}}, false, "argv is not re-parsed"},
		{"run_command", map[string]any{"argv": []any{"curl", "x"}}, true, "argv program not allowed"},
		{"read_file", map[string]any{"path": "x"}, false, "non-shell tools pass"},
	}
	for _, c := range cases {
		if got := call(c.tool, c.args).Block; got != c.block {
			t.Errorf("%s %v: block=%v, want %v (%s)", c.tool, c.args, got, c.block, c.why)
		}
	}
	if !call("execute", map[string]any{"command": "go test | tee"}).Block {
		t.Fatal("pipe")
	}
	loose := Restricted(Options{AllowedPrograms: []string{"go"}, AllowShellOperators: true})
	if loose(context.Background(), core.BeforeToolCallContext{ToolName: "execute",
		Arguments: map[string]any{"command": "go test | tee"}}).Block {
		t.Fatal("AllowShellOperators must permit the pipe")
	}
	// AllowEnvPrefixes admits an assignment prefix, but never one that changes
	// which binary runs or what is loaded into it.
	env := Restricted(Options{AllowedPrograms: []string{"go", "ls"}, AllowEnvPrefixes: true})
	for cmd, want := range map[string]bool{
		"GOFLAGS=-mod=mod go build":         false,
		"CGO_ENABLED=0 GOOS=linux go test":  false,
		"PATH=/anything ls":                 true,
		"LD_PRELOAD=/x.so ls":               true,
		"LD_LIBRARY_PATH=/x ls":             true,
		"DYLD_INSERT_LIBRARIES=/x.dylib ls": true,
		"FOO=1 BASH_ENV=/x go test":         true,
		"GIT_EXEC_PATH=/x go test":          true,
		"NODE_OPTIONS=--require=/x go test": true,
		"PYTHONPATH=/x go test":             true,
		"PERL5OPT=-Mx go test":              true,
		"x-y=1 ls":                          true, // not an assignment: bash runs `x-y=1`
		"./x=1 ls":                          true,
		// A quoted value is one word: the program is the word after it.
		`X='a ls' rm -rf /`: true,
		`X="a ls" rm -rf /`: true,
		"X='a\tls' rm":      true,
		`X="a \" ls" rm`:    true,
		`X='a ls' ls -l`:    false,
		`X="a b" go test`:   false,
		`'X=1' ls`:          true, // quoted, so not an assignment: bash runs `X=1`
		`X='a ls`:           true, // unterminated quote: nothing runs
		`"ls" -l`:           false,
		`l\s -l`:            false, // bash runs ls
		`l"s"x`:             true,  // bash runs lsx
		`"./go" test`:       true,
		// Injection variables beyond Perl, Python, Node and Ruby.
		"JAVA_TOOL_OPTIONS=-javaagent:/x.jar go test": true,
		"_JAVA_OPTIONS=-javaagent:/x.jar go test":     true,
		"JDK_JAVA_OPTIONS=-javaagent:/x.jar go test":  true,
		"CLASSPATH=/x go test":                        true,
		"DOTNET_STARTUP_HOOKS=/tmp/evil.dll go test":  true,
		"LUA_INIT=@/x.lua go test":                    true,
		"LUA_INIT_5_4=@/x.lua go test":                true,
		"LUA_PATH=/x/?.lua go test":                   true,
		"LUA_CPATH=/x/?.so go test":                   true,
		"GCONV_PATH=/tmp/gconv go test":               true,
		"PHPRC=/x go test":                            true,
		"PHP_INI_SCAN_DIR=/x go test":                 true,
		"TCLLIBPATH=/x go test":                       true,
		"ZDOTDIR=/x go test":                          true,
		"HOME=/x go test":                             true,
	} {
		if got := env(context.Background(), core.BeforeToolCallContext{ToolName: "execute",
			Arguments: map[string]any{"command": cmd}}).Block; got != want {
			t.Errorf("AllowEnvPrefixes %q: block=%v, want %v", cmd, got, want)
		}
	}
	term := Restricted(Options{TerminateOnBlock: true})
	if d := term(context.Background(), core.BeforeToolCallContext{ToolName: "execute",
		Arguments: map[string]any{"command": "ls"}}); !d.Block || !d.Terminate {
		t.Fatal("TerminateOnBlock must cast the REQ-TOOL-13.2 vote")
	}
}

// TS-12-29: Decision carries Block, Reason and Terminate, and the shell tools
// are execute and run_command.
func TestDecisionAndShellToolNames_TS12_29(t *testing.T) {
	d := Decision{Block: true, Reason: "test", Terminate: true}
	if !d.Block || d.Reason != "test" || !d.Terminate {
		t.Fatalf("Decision = %+v", d)
	}
	if strings.Join(ShellToolNames, ",") != "execute,run_command" {
		t.Fatalf("ShellToolNames = %v, want [execute run_command]", ShellToolNames)
	}
}

// TS-12-30: an empty argv is blocked.
func TestCheckBlocksEmptyArgv_TS12_30(t *testing.T) {
	if d := Check([]string{}, Options{TerminateOnBlock: true}); d != (Decision{Block: true, Reason: "empty argv", Terminate: true}) {
		t.Fatalf("Check(empty) = %+v", d)
	}
}

// TS-12-31: a path is allowed only as that exact, cleaned path.
func TestCheckMatchesPathsExactly_TS12_31(t *testing.T) {
	o := Options{AllowedPrograms: []string{"/usr/bin/git"}, TerminateOnBlock: true}
	want := Decision{Block: true, Reason: `program "/usr/bin/curl" is not on the allowlist`, Terminate: true}
	if d := Check([]string{"/usr/bin/curl", "url"}, o); d != want {
		t.Fatalf("Check(/usr/bin/curl) = %+v, want %+v", d, want)
	}
	if d := Check([]string{"/usr/bin/../bin/git"}, o); d.Block {
		t.Fatalf("a path that cleans to an allowed one was blocked: %+v", d)
	}
	if d := Check([]string{"./git"}, o); !d.Block {
		t.Fatal("./git is the workspace's git, not the allowed path")
	}
}

// TS-12-32: a bare name not on the allowlist is blocked.
func TestCheckBlocksUnlistedNames_TS12_32(t *testing.T) {
	o := Options{AllowedPrograms: []string{"git", "go"}}
	want := Decision{Block: true, Reason: `program "rm" is not on the allowlist`}
	if d := Check([]string{"rm", "-rf", "dir"}, o); d != want {
		t.Fatalf("Check(rm) = %+v, want %+v", d, want)
	}
}

// TS-12-33: an allowed bare name or exact path runs.
func TestCheckAllowsListedPrograms_TS12_33(t *testing.T) {
	o := Options{AllowedPrograms: []string{"git", "/usr/local/bin/deploy"}}
	if d := Check([]string{"git", "status"}, o); d.Block {
		t.Fatalf("git was blocked: %+v", d)
	}
	if d := Check([]string{"/usr/local/bin/deploy"}, o); d.Block {
		t.Fatalf("the listed path was blocked: %+v", d)
	}
}

// TS-12-34: Restricted refuses blocked tools, shell operators and
// environment prefixes before asking Check about the program.
func TestRestrictedChecksBeforeCheck_TS12_34(t *testing.T) {
	hook := Restricted(Options{BlockedTools: []string{"danger_tool"}, AllowedPrograms: []string{"git"}})
	ctx := context.Background()
	cases := []struct {
		name  string
		in    core.BeforeToolCallContext
		block bool
		why   string
	}{
		{"blocked tool", core.BeforeToolCallContext{ToolName: "danger_tool"}, true, `tool "danger_tool" is not permitted`},
		{"pipe", core.BeforeToolCallContext{ToolName: "execute", Arguments: map[string]any{"command": "git status | cat"}}, true, "shell operator"},
		{"env prefix", core.BeforeToolCallContext{ToolName: "execute", Arguments: map[string]any{"command": "PATH=/tmp git status"}}, true, "environment assignment"},
		{"unlisted", core.BeforeToolCallContext{ToolName: "execute", Arguments: map[string]any{"command": "rm -rf ."}}, true, `guard.Restricted: program "rm" is not on the allowlist`},
		{"allowed", core.BeforeToolCallContext{ToolName: "execute", Arguments: map[string]any{"command": "git commit"}}, false, ""},
	}
	for _, c := range cases {
		d := hook(ctx, c.in)
		if d.Block != c.block || !strings.Contains(d.Reason, c.why) {
			t.Errorf("%s: %+v, want block=%v mentioning %q", c.name, d, c.block, c.why)
		}
	}
}
