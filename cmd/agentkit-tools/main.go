// Command agentkit-tools serves AgentKit's built-in tools, code_search and
// code_mode for one directory, to a harness or a relay that runs beside it.
//
//	agentkit-tools mcp   [flags]   one MCP session on stdin and stdout
//	agentkit-tools call  [flags]   one tool call read from a request file
//	agentkit-tools tools [flags]   the served tool definitions as JSON
//	agentkit-tools --version
//
// It serves tools; it never calls a model. See docs/cli.md.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
)

func main() { os.Exit(runMain(os.Args[1:], os.Stdout, os.Stderr)) }

const usage = `usage: agentkit-tools <command> [flags]

commands:
  mcp     serve one MCP session on stdin and stdout
  call    make one tool call read from --request <file>
  tools   print the served tool definitions as a JSON array

  agentkit-tools --version prints the version.

flags of every command:
  --dir <path>   the directory to serve (default: the working directory)

flags of call:
  --request <file>       the request file: {"name": string, "input": object}
  --timeout <duration>   the bound on the call (default 10m)
`

// errUsage is a command line the command cannot read; it prints the usage and
// exits 2.
var errUsage = errors.New("usage")

// runMain runs the command with args and returns its exit status.
func runMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version") {
		fmt.Fprintln(stdout, versionFrom(buildInfo()))
		return 0
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "tools":
		return runTools(args[1:], stdout, stderr)
	case "call":
		return runCall(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "agentkit-tools: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

// parse reads args into fs, which has the command's flags. A flag fs does not
// define, a value it cannot read or a stray argument prints the usage.
func parse(fs *flag.FlagSet, args []string, stderr io.Writer) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "agentkit-tools %s: %v\n\n%s", fs.Name(), err, usage)
		return errUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agentkit-tools %s: unexpected argument %q\n\n%s", fs.Name(), strings.Join(fs.Args(), " "), usage)
		return errUsage
	}
	return nil
}

// runCall makes one tool call.
func runCall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("call", flag.ContinueOnError)
	var s settings
	s.register(fs)
	fs.String("request", "", "")
	fs.Duration("timeout", 0, "")
	if parse(fs, args, stderr) != nil {
		return 2
	}
	if err := s.checkDir(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stderr, "agentkit-tools call: not implemented")
	return 2
}

// runMCP serves one MCP session.
func runMCP(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	var s settings
	s.register(fs)
	if parse(fs, args, stderr) != nil {
		return 2
	}
	if err := s.checkDir(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	fmt.Fprintln(stderr, "agentkit-tools mcp: not implemented")
	return 2
}

// selfPath is this module's path, which the build information names.
const selfPath = "github.com/agent-fox-dev/agentkit-go/cmd/agentkit-tools"

// buildInfo is the running binary's build information, or nil.
func buildInfo() *debug.BuildInfo {
	bi, _ := debug.ReadBuildInfo()
	return bi
}

// versionFrom is the version of this module that bi records: the main
// module's, when the binary is built from this module, otherwise the
// dependency's, or its replacement's when it is replaced. It is "(devel)" when
// there is none.
func versionFrom(bi *debug.BuildInfo) string {
	if bi == nil {
		return "(devel)"
	}
	v := ""
	if bi.Main.Path == selfPath {
		v = bi.Main.Version
	} else {
		for _, d := range bi.Deps {
			if d.Path != selfPath {
				continue
			}
			v = d.Version
			if d.Replace != nil {
				v = d.Replace.Version
			}
		}
	}
	if v == "" {
		return "(devel)"
	}
	return v
}
