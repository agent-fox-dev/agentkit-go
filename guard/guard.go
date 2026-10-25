// Package guard is the `execute` boundary in non-interactive deployments.
//
// AgentKit authorizes tool calls with a per-call interceptor rather than a
// command allowlist, on the grounds that a static allowlist is both trivially
// escaped and too narrow to run a build. That reasoning assumes an embedder
// that can answer a permission question. A daemon triaging issues overnight
// cannot, and "allow" by default is strictly worse than the allowlist it
// replaced. So:
//
//   - agentkit.New refuses LOUDLY an agent whose tool set reaches a shell
//     tool with no Guard configured (core.ErrUnguardedExecute), and
//   - Check is the pure decision — may this argv run? — and Restricted ships
//     as an importable, REPLACEABLE interceptor over it, kept out of the
//     SDK's enforcement path so it can be swapped rather than only narrowed.
//
// Restricted is a floor for unattended safety, not a sandbox: an allowlist
// wide enough to run a build is escapable through the allowed programs' own
// configuration and subprocess surfaces. It remains that floor whatever runs
// the tools.
//
// A sandbox is the boundary the guard never claimed to be. AllowAll is a
// reasonable choice only when every shell tool the agent can reach is a
// proxy from a sandbox whose provider reports sandbox.IsolationContainer or
// sandbox.IsolationMicroVM (sandbox/podman), under sandbox.NetworkNone (or
// with egress the embedder accepts), with Spec.Resources set, and with the
// files exported from it treated as untrusted. It is never reasonable with
// the local provider (sandbox/local), which isolates nothing, or with any
// local shell tool in the set. agentkit.New still requires a Guard either
// way.
package guard

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// ShellToolNames are the tools the guard treats as a shell. A caller-supplied
// tool of the same name counts: the name is what the model calls, and a
// custom `execute` is no less a shell for being custom.
var ShellToolNames = []string{"execute", "run_command"}

// AllowAll is the explicit opt-out from the guard: an interceptor
// that never blocks. Passing it is an affirmative act — the embedder has
// said, in code, that this agent runs an unrestricted shell.
//
// It is reasonable only when every shell tool the agent can reach is a proxy
// from a sandbox whose provider reports IsolationContainer or
// IsolationMicroVM, under NetworkNone (or egress the embedder accepts), with
// Resources set and exported files treated as untrusted; never with the
// local provider or with a local shell tool in the set.
func AllowAll(context.Context, core.BeforeToolCallContext) core.BeforeToolCallDecision {
	return core.BeforeToolCallDecision{}
}

// IsShellTool reports whether name is one of ShellToolNames.
func IsShellTool(name string) bool {
	for _, n := range ShellToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// Decision is Check's verdict on one command.
type Decision struct {
	Block     bool
	Reason    string
	Terminate bool
}

// Check decides whether the command argv may run under o: whether argv[0]
// is on o.AllowedPrograms. It is a pure function — no environment, no
// filesystem, no PATH lookup — so a host can ask it about a command before
// running it as well as through Restricted.
//
// A program with a path separator matches only a listed path, compared
// after cleaning: `./go` names a file in the workspace, not the go on PATH.
// A bare name matches a listed bare name only.
// A block carries o.TerminateOnBlock.
func Check(argv []string, o Options) Decision {
	if len(argv) == 0 {
		return Decision{Block: true, Reason: "empty argv", Terminate: o.TerminateOnBlock}
	}
	if !newAllowlist(o.AllowedPrograms).permits(argv[0]) {
		return Decision{Block: true, Reason: fmt.Sprintf("program %q is not on the allowlist", argv[0]),
			Terminate: o.TerminateOnBlock}
	}
	return Decision{}
}

// Options configures Restricted.
type Options struct {
	// AllowedPrograms are the programs that may run: argv[0], or the first
	// word after any assignments of each command an `execute` line runs (see
	// Commands). A bare name
	// ("go") admits only that bare name, resolved through PATH. A path
	// ("/usr/bin/git") admits that exact path only — not a bare git found
	// through PATH, which may be another binary. A
	// program word containing a path separator is refused unless that path
	// itself is listed: `./go` names a file in the workspace, not the go on
	// PATH. Empty means every shell call is blocked, which is the safe
	// default for a policy whose only purpose is to say no.
	AllowedPrograms []string
	// AllowShellOperators permits pipes, `;`, `&&`, `||`, redirection,
	// subshells, command substitution and variable expansion in `execute`
	// commands. Off by default. The grammar this filter understands is POSIX
	// sh / bash quoting — single quotes literal, double quotes expanding —
	// and it is declared here because a filter must say which grammar it
	// filters.
	//
	// When on, every command the line runs is judged, not only the first:
	// each one Commands finds, the ones inside substitutions and unquoted
	// heredoc bodies included. A line Commands cannot read with certainty
	// (a *SyntaxError) is refused.
	AllowShellOperators bool
	// AllowEnvPrefixes permits `NAME=value` assignments in `execute` commands:
	// in front of a program (`GOFLAGS=-mod=mod go build`) or, under
	// AllowShellOperators, on their own (`d=$(go list .)`), which changes the
	// environment of the commands after it when NAME is already exported. Off
	// by default: an assignment configures the programs it reaches, and an
	// allowlist of names says nothing about how those programs are
	// configured. Even when on, a name that changes which binary runs or
	// what is loaded into it — PATH, LD_*, DYLD_*, BASH_ENV, interpreter
	// injection variables, see envDenied — is refused.
	AllowEnvPrefixes bool
	// BlockedTools are refused by name, shell or not.
	BlockedTools []string
	// TerminateOnBlock sets Terminate beside every Block, so that a refusal
	// ends the run instead of looping the model into retrying.
	TerminateOnBlock bool
}

// Restricted is the reference interceptor for headless embedders: an
// allowlist of programs plus shell-operator rejection.
//
// It is a FLOOR, not a sandbox. The argument against a command allowlist
// still holds — an allowlist wide enough to run a build is escapable through
// the allowed programs' own configuration and subprocess surfaces — and this
// policy does nothing about that. What it does is make the unattended default
// "no" instead of "yes", which is the difference the guard exists to close.
// Embedders with real context should replace it, not extend it.
//
// What it does check, it checks against what will actually run: the program
// of every command on the line (see Commands) is matched as spelled, not by
// basename, and an assignment that would change which binary a listed name
// resolves to, or load code into it, is refused.
func Restricted(o Options) core.BeforeToolCall {
	blocked := make(map[string]bool, len(o.BlockedTools))
	for _, t := range o.BlockedTools {
		blocked[t] = true
	}
	block := func(reason string) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{Block: true, Terminate: o.TerminateOnBlock, Reason: reason}
	}
	// check asks Check about the program and adapts its verdict.
	check := func(argv []string) core.BeforeToolCallDecision {
		d := Check(argv, o)
		if !d.Block {
			return core.BeforeToolCallDecision{}
		}
		return core.BeforeToolCallDecision{Block: true, Terminate: d.Terminate, Reason: "guard.Restricted: " + d.Reason}
	}

	return func(_ context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		if blocked[in.ToolName] {
			return block(fmt.Sprintf("guard.Restricted: tool %q is not permitted", in.ToolName))
		}
		switch in.ToolName {
		case "execute":
			cmd, _ := in.Arguments["command"].(string)
			if !o.AllowShellOperators {
				if op, found := firstShellOperator(cmd); found {
					return block(fmt.Sprintf("guard.Restricted: shell operator %q is not permitted "+
						"(grammar: POSIX sh); use run_command with an argument list, or one plain command", op))
				}
			}
			cmds, err := Commands(cmd)
			if err != nil {
				return block(fmt.Sprintf("guard.Restricted: the command cannot be read with certainty, so "+
					"nothing in it was run: %v", err))
			}
			ran := false
			for _, c := range cmds {
				if len(c.Assign) > 0 && !o.AllowEnvPrefixes {
					where := "in front of the program"
					if len(c.Argv) == 0 {
						where = "on its own"
					}
					return block(fmt.Sprintf("guard.Restricted: environment assignment %s= %s is not permitted; "+
						"run the program without it", c.Assign[0], where))
				}
				for _, n := range c.Assign {
					if envDenied(n) {
						return block(fmt.Sprintf("guard.Restricted: environment assignment %s= is not permitted: "+
							"it changes which program runs or what is loaded into it", n))
					}
				}
				if len(c.Argv) == 0 {
					continue // assignments or redirections alone run no program
				}
				ran = true
				if d := check(c.Argv); d.Block {
					return d
				}
			}
			if !ran {
				return block("guard.Restricted: the command runs no program")
			}
			return core.BeforeToolCallDecision{}
		case "run_command":
			raw, _ := in.Arguments["argv"].([]any)
			argv := make([]string, len(raw))
			for i, a := range raw {
				s, ok := a.(string)
				if !ok {
					return block(fmt.Sprintf("guard.Restricted: argv[%d] is not a string", i))
				}
				argv[i] = s
			}
			return check(argv)
		}
		return core.BeforeToolCallDecision{}
	}
}

// Allow is Restricted with only AllowedPrograms set: it admits the shell calls
// whose program is in programs and blocks the rest, with Restricted's decisions
// and its "guard.Restricted: " reasons. Restricted is the way to set the other
// options (AllowShellOperators, AllowEnvPrefixes, BlockedTools,
// TerminateOnBlock). With no programs every shell call is blocked.
//
// Like Restricted, the guard is a floor, not a sandbox.
//
// Allow copies programs, so a caller who later changes or appends to the slice
// it passed does not widen the allowlist.
func Allow(programs ...string) core.BeforeToolCall {
	return Restricted(Options{AllowedPrograms: slices.Clone(programs)})
}

// firstShellOperator scans a POSIX-sh command for the first construct that
// hands control to the shell — a pipe, a list operator, redirection, a
// subshell, command substitution or parameter expansion — honouring the two
// quoting rules that matter: nothing expands inside single quotes, and only
// `$` and backtick expand inside double quotes. Returns the operator found.
func firstShellOperator(cmd string) (string, bool) {
	inSingle, inDouble := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			}
		case inDouble:
			switch c {
			case '"':
				inDouble = false
			case '\\':
				i++ // the next byte is literal
			case '$', '`':
				return string(c), true
			}
		default:
			switch c {
			case '\'':
				inSingle = true
			case '"':
				inDouble = true
			case '\\':
				i++
			case '|', ';', '&', '<', '>', '(', ')', '`', '$', '\n':
				return string(c), true
			}
		}
	}
	return "", false
}

// allowlist matches a program word as the shell or exec will resolve it.
type allowlist struct {
	names map[string]bool // bare names, looked up through PATH
	paths map[string]bool // cleaned paths, matched exactly
}

func newAllowlist(programs []string) allowlist {
	a := allowlist{names: map[string]bool{}, paths: map[string]bool{}}
	for _, p := range programs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if hasPathSeparator(p) {
			a.paths[path.Clean(p)] = true
			continue
		}
		a.names[p] = true
	}
	return a
}

// permits reports whether prog may run. A word with a path separator is a
// path — `./ls` is the workspace's ls, `/tmp/ls` is /tmp's — so it matches
// only a listed path, never a listed name that happens to be its basename.
func (a allowlist) permits(prog string) bool {
	if prog == "" {
		return false
	}
	if hasPathSeparator(prog) {
		return a.paths[path.Clean(prog)]
	}
	return a.names[prog]
}

func hasPathSeparator(s string) bool {
	return strings.ContainsAny(s, `/\`)
}

func isIdentifier(s string) bool {
	for i, c := range s {
		switch {
		case c == '_', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case i > 0 && '0' <= c && c <= '9':
		default:
			return false
		}
	}
	return s != ""
}

// envDenied reports whether an assignment to name changes which binary an
// allowed name resolves to, or injects code into the shell, the dynamic
// loader, glibc's iconv or a common interpreter or runtime (Perl, Python,
// Node, Ruby, the JVM, .NET, Lua, PHP, Tcl). HOME and ZDOTDIR are here
// because they choose the startup files programs and shells read. It is the part of "the program's own
// configuration" that is not the program's at all, so it is refused even
// under AllowEnvPrefixes. Program-specific variables (GOFLAGS=-toolexec,
// GIT_SSH_COMMAND, ...) remain the embedder's concern.
func envDenied(name string) bool {
	// LUA_* by prefix: Lua reads the versioned LUA_INIT_5_4 before LUA_INIT.
	for _, prefix := range []string{"LD_", "DYLD_", "LUA_INIT", "LUA_PATH", "LUA_CPATH"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "PATH", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "IFS", "ZDOTDIR", "HOME",
		"GIT_EXEC_PATH", "GCONV_PATH",
		"PERL5OPT", "PERL5LIB", "PERLLIB",
		"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP",
		"NODE_OPTIONS", "NODE_PATH",
		"RUBYOPT", "RUBYLIB",
		"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "JDK_JAVA_OPTIONS", "CLASSPATH",
		"DOTNET_STARTUP_HOOKS",
		"PHPRC", "PHP_INI_SCAN_DIR",
		"TCLLIBPATH":
		return true
	}
	return false
}
