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

	"github.com/agent-fox-dev/agentkit-go/core"
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
// assignments and its program word, unquoted. Words are split as POSIX sh
// splits them — a quoted span is part of one word, so `X='a ls' rm` runs rm,
// not ls. A word is an assignment only when NAME is a shell identifier
// spelled without quotes: bash runs `x-y=1 ls` and `'X=1' ls` as the commands
// `x-y=1` and `X=1`, so that word is the program, and it is refused. An
// unterminated quote is a syntax error that runs nothing; prog is then empty.
func splitCommand(cmd string) (names []string, prog string) {
	words, ok := shellWords(cmd)
	if !ok {
		return nil, ""
	}
	for _, w := range words {
		if i := strings.IndexByte(w.raw, '='); i > 0 && isIdentifier(w.raw[:i]) {
			names = append(names, w.raw[:i])
			continue
		}
		return names, w.text
	}
	return names, ""
}

// shellWord is one word of a command: as written, and with its quotes and
// escapes removed.
type shellWord struct{ raw, text string }

// shellWords splits cmd into words under the quoting rules firstShellOperator
// honours: blanks separate words only outside quotes, single quotes are
// literal, and a backslash escapes the next byte outside quotes and only
// `$`, backtick, `"`, `\` and newline inside double quotes. ok is false when
// a quote is left open.
func shellWords(cmd string) (words []shellWord, ok bool) {
	var raw, text strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, shellWord{raw.String(), text.String()})
		}
		raw.Reset()
		text.Reset()
		inWord = false
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch c {
		case ' ', '\t', '\n':
			flush()
			continue
		case '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			raw.WriteString(cmd[i : i+end+2])
			text.WriteString(cmd[i+1 : i+1+end])
			i += end + 1
		case '"':
			j := i + 1
			for ; j < len(cmd) && cmd[j] != '"'; j++ {
				if cmd[j] == '\\' && j+1 < len(cmd) && strings.IndexByte("$`\"\\\n", cmd[j+1]) >= 0 {
					j++
				}
				text.WriteByte(cmd[j])
			}
			if j == len(cmd) {
				return nil, false
			}
			raw.WriteString(cmd[i : j+1])
			i = j
		case '\\':
			raw.WriteByte(c)
			if i+1 < len(cmd) {
				i++
				raw.WriteByte(cmd[i])
				text.WriteByte(cmd[i])
			}
		default:
			raw.WriteByte(c)
			text.WriteByte(c)
		}
		inWord = true
	}
	flush()
	return words, true
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
