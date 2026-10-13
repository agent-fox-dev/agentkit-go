// Command agentkit-sandboxd is the AgentKit agent inside a sandbox. It
// serves the agentkit.sandbox.v1 Agent service as one HTTP/2 connection on
// its stdin and stdout, opens no listening socket, and runs the built-in
// tools against --workdir with its own environment, verbatim.
//
// It exits 0 when the host closes the connection, 2 on a usage error, and 1
// on a startup failure or when --ttl expires, with one line on stderr.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// config is the parsed command line.
type config struct {
	workdir string
	ttl     time.Duration
	version bool
}

// parseFlags parses args. A zero or negative --ttl is a usage error.
func parseFlags(args []string) (config, error) {
	fs := flag.NewFlagSet("agentkit-sandboxd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var c config
	fs.StringVar(&c.workdir, "workdir", "/workspace", "the workspace directory")
	fs.DurationVar(&c.ttl, "ttl", time.Hour, "how long to serve")
	fs.BoolVar(&c.version, "version", false, "print the agent and protocol versions and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.ttl <= 0 {
		return c, fmt.Errorf("--ttl must be positive, got %v", c.ttl)
	}
	return c, nil
}

// redirectStdout points os.Stdout at stderr, so a stray write cannot
// corrupt the protocol stream, and returns the original stdout.
func redirectStdout() *os.File {
	orig := os.Stdout
	os.Stdout = os.Stderr
	return orig
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	cfg, err := parseFlags(args)
	if err != nil {
		fail(err)
		return 2
	}
	if cfg.version {
		fmt.Printf("agentkit-sandboxd %s (protocol %d.%d)\n", buildver.Version(), sandbox.ProtocolMajor, sandbox.ProtocolMinor)
		return 0
	}
	out := redirectStdout()
	a, err := agent.New(agent.Config{Workdir: cfg.workdir, Env: os.Environ(), TTL: cfg.ttl})
	if err != nil {
		fail(err)
		return 1
	}
	if err := a.Serve(stdioconn.New(pollable(os.Stdin, 0), pollable(out, 1))); err != nil {
		fail(err)
		return 1
	}
	return 0
}

// fail writes err to stderr as one line.
func fail(err error) {
	fmt.Fprintf(os.Stderr, "agentkit-sandboxd: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
}
