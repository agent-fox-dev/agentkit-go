// Package guard is OQ-8's resolution: the `execute` boundary in
// non-interactive deployments.
//
// REQ-SEC-03 replaced the command allowlist with a per-call interceptor on the
// grounds that a static allowlist is both trivially escaped and too narrow to
// run a build. That reasoning assumes an embedder that can answer a permission
// question. A daemon triaging issues overnight cannot, and "allow" by default
// is strictly worse than the allowlist it replaced. So, per OQ-8's
// recommendation (b) plus (a):
//
//   - a run fails LOUDLY when a shell tool is in the resolved set and no
//     interceptor is configured (core.ErrUnguardedExecute, checked by the
//     Agent at the head of every run), and
//   - Restricted ships as an importable, REPLACEABLE starting point — kept
//     out of the SDK's enforcement path so it can be swapped rather than
//     only narrowed.
package guard

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/agentfox/agentkit-go/core"
)

// ShellToolNames are the tools the guard treats as a shell. A caller-supplied
// tool of the same name counts: the name is what the model calls, and a
// custom `execute` is no less a shell for being custom.
var ShellToolNames = []string{"execute", "run_command", "powershell"}

// AllowAll is the explicit opt-out from the guard: an interceptor
// that never blocks. Passing it is the affirmative act OQ-8 asks for — the
// embedder has said, in code, that this agent runs an unrestricted shell.
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

// Options configures RestrictedPolicy.
type Options struct {
	// AllowedPrograms are the programs (argv[0], or the first word of an
	// `execute` command after any assignments) that may run. A bare name
	// ("go") admits only that bare name, resolved through PATH. A path
	// ("/usr/bin/git") admits that exact path, and its bare basename too. A
	// program word containing a path separator is refused unless that path
	// itself is listed: `./go` names a file in the workspace, not the go on
	// PATH. Empty means every shell call is blocked, which is the safe
	// default for a policy whose only purpose is to say no.
	AllowedPrograms []string
	// AllowShellOperators permits pipes, `;`, `&&`, `||`, redirection,
	// subshells, command substitution and variable expansion in `execute`
	// commands. Off by default. The grammar this filter understands is POSIX
	// sh / bash quoting — single quotes literal, double quotes expanding —
	// and it is declared here because REQ-SEC-04 requires a filter to say
	// which grammar it filters.
	AllowShellOperators bool
	// AllowEnvPrefixes permits `NAME=value` assignments in front of the
	// program in `execute` commands (`GOFLAGS=-mod=mod go build`). Off by
	// default: an assignment configures the program it precedes, and an
	// allowlist of names says nothing about how those programs are
	// configured. Even when on, a name that changes which binary runs or
	// what is loaded into it — PATH, LD_*, DYLD_*, BASH_ENV, interpreter
	// injection variables, see envDenied — is refused.
	AllowEnvPrefixes bool
	// PowerShellFilter decides `powershell` calls. There is no PowerShell
	// grammar filter in this file, so per REQ-SEC-04 the tool is REFUSED
	// OUTRIGHT unless the embedder supplies one: a control that silently does
	// not hold on one of the supported shells is worse than no shell.
	PowerShellFilter func(command string) (block bool, reason string)
	// BlockedTools are refused by name, shell or not.
	BlockedTools []string
	// TerminateOnBlock casts the REQ-TOOL-13.2 vote so that a refusal ends
	// the run instead of looping the model into retrying.
	TerminateOnBlock bool
}

// Restricted is the reference interceptor for headless embedders: an
// allowlist of programs plus shell-operator rejection.
//
// It is a FLOOR, not a sandbox. REQ-SEC-03's argument still holds — an
// allowlist wide enough to run a build is escapable through the allowed
// programs' own configuration and subprocess surfaces — and this policy does
// nothing about that. What it does is make the unattended default "no"
// instead of "yes", which is the difference OQ-8 exists to close. Embedders
// with real context should replace it, not extend it.
//
// What it does check, it checks against what will actually run: the program
// is matched as spelled, not by basename, and an assignment prefix that
// would change which binary a listed name resolves to, or load code into
// it, is refused.
func Restricted(o Options) core.BeforeToolCall {
	allowed := newAllowlist(o.AllowedPrograms)
	blocked := make(map[string]bool, len(o.BlockedTools))
	for _, t := range o.BlockedTools {
		blocked[t] = true
	}
	block := func(reason string) core.BeforeToolCallDecision {
		return core.BeforeToolCallDecision{Block: true, Terminate: o.TerminateOnBlock, Reason: reason}
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
			names, prog := splitCommand(cmd)
			if len(names) > 0 && !o.AllowEnvPrefixes {
				return block(fmt.Sprintf("guard.Restricted: environment assignment %s= in front of the "+
					"program is not permitted; run the program without it", names[0]))
			}
			for _, n := range names {
				if envDenied(n) {
					return block(fmt.Sprintf("guard.Restricted: environment assignment %s= is not permitted: "+
						"it changes which program runs or what is loaded into it", n))
				}
			}
			if !allowed.permits(prog) {
				return block(fmt.Sprintf("guard.Restricted: program %q is not on the allowlist", prog))
			}
		case "run_command":
			argv, _ := in.Arguments["argv"].([]any)
			prog := ""
			if len(argv) > 0 {
				prog, _ = argv[0].(string)
			}
			if !allowed.permits(prog) {
				return block(fmt.Sprintf("guard.Restricted: program %q is not on the allowlist", prog))
			}
		case "powershell":
			if o.PowerShellFilter == nil {
				return block("guard.Restricted: powershell is refused outright; this policy filters " +
					"POSIX sh only and has no PowerShell grammar (REQ-SEC-04)")
			}
			cmd, _ := in.Arguments["command"].(string)
			if b, reason := o.PowerShellFilter(cmd); b {
				return block("guard.Restricted: " + reason)
			}
		}
		return core.BeforeToolCallDecision{}
	}
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
		}
		a.names[path.Base(p)] = true
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

// splitCommand returns the names of a command's leading NAME=value
// assignments and its program word, unquoted. A word is an assignment only
// when NAME is a shell identifier: bash runs `x-y=1 ls` as the command
// `x-y=1`, so that word is the program, and it is refused.
func splitCommand(cmd string) (names []string, prog string) {
	for _, w := range strings.Fields(cmd) {
		if i := strings.IndexByte(w, '='); i > 0 && isIdentifier(w[:i]) {
			names = append(names, w[:i])
			continue
		}
		return names, strings.Trim(w, `"'`)
	}
	return names, ""
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
// loader or a common interpreter. It is the part of "the program's own
// configuration" that is not the program's at all, so it is refused even
// under AllowEnvPrefixes. Program-specific variables (GOFLAGS=-toolexec,
// GIT_SSH_COMMAND, ...) remain the embedder's concern.
func envDenied(name string) bool {
	if strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_") {
		return true
	}
	switch name {
	case "PATH", "BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "IFS",
		"GIT_EXEC_PATH",
		"PERL5OPT", "PERL5LIB", "PERLLIB",
		"PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP",
		"NODE_OPTIONS", "NODE_PATH",
		"RUBYOPT", "RUBYLIB":
		return true
	}
	return false
}
