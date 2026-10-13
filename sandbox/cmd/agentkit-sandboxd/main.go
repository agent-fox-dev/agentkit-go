// Command agentkit-sandboxd is the AgentKit agent inside a sandbox. It
// serves the agentkit.sandbox.v1 Agent service as one HTTP/2 connection on
// its stdin and stdout, opens no listening socket, and runs the built-in
// tools against --workdir with its own environment, verbatim. On Linux it
// first confines itself, and so every process a tool starts, with a
// Landlock ruleset (sandbox/internal/confine); --network, --landlock-ro and
// --landlock-rw shape it, and no flag turns it off.
//
// It exits 0 when the host closes the connection, 2 on a usage error
// (including a refused --workdir, --network, --landlock-ro or --landlock-rw
// value), and 1 on a startup failure, a ruleset that cannot be applied, or
// when --ttl expires, with one line on stderr.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/buildver"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/confine"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/stdioconn"
)

// config is the parsed command line.
type config struct {
	workdir    string
	ttl        time.Duration
	version    bool
	network    sandbox.NetworkMode
	landlockRO []string
	landlockRW []string
	// networkFlag is --network as given; parseFlags checks it into network.
	networkFlag string
}

// flagSet defines the command's flags into c.
func flagSet(c *config) *flag.FlagSet {
	fs := flag.NewFlagSet("agentkit-sandboxd", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.workdir, "workdir", "/workspace", "the workspace directory")
	fs.DurationVar(&c.ttl, "ttl", time.Hour, "how long to serve")
	fs.BoolVar(&c.version, "version", false, "print the agent and protocol versions and exit")
	fs.StringVar(&c.networkFlag, "network", "none", "the sandbox's network mode: none, allowlist or unrestricted")
	fs.Func("landlock-ro", "an extra read-only Landlock root (repeatable)", func(v string) error {
		c.landlockRO = append(c.landlockRO, v)
		return nil
	})
	fs.Func("landlock-rw", "an extra writable Landlock root (repeatable)", func(v string) error {
		c.landlockRW = append(c.landlockRW, v)
		return nil
	})
	return fs
}

// networkModes are the --network values, exactly.
var networkModes = map[string]sandbox.NetworkMode{
	"none": sandbox.NetworkNone, "allowlist": sandbox.NetworkAllowlist, "unrestricted": sandbox.NetworkUnrestricted,
}

// parseFlags parses args. A zero or negative --ttl is a usage error.
func parseFlags(args []string) (config, error) {
	var c config
	fs := flagSet(&c)
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if c.ttl <= 0 {
		return c, fmt.Errorf("--ttl must be positive, got %v", c.ttl)
	}
	m, ok := networkModes[c.networkFlag]
	if !ok {
		return c, fmt.Errorf("--network: invalid value %q (want none, allowlist or unrestricted)", c.networkFlag)
	}
	c.network = m
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
	// Confinement comes before any other work: before stdout is redirected,
	// before the agent exists and before any goroutine of the command's own
	// (an AST test pins the order). Every process a tool starts inherits it.
	info, err := confine.Setup(confine.Inputs{Workdir: cfg.workdir, Env: os.Environ(), RO: cfg.landlockRO, RW: cfg.landlockRW, Mode: cfg.network})
	if err != nil {
		var usage *confine.UsageError
		if errors.As(err, &usage) {
			fail(err)
			return 2
		}
		fail(fmt.Errorf("landlock: %w", err))
		return 1
	}
	out := redirectStdout()
	a, err := agent.New(agent.Config{Workdir: cfg.workdir, Env: os.Environ(), TTL: cfg.ttl, Landlock: info})
	if err != nil {
		fail(err)
		return 1
	}
	err = a.Serve(stdioconn.New(pollable(os.Stdin, 0), pollable(out, 1)))
	// pollable wraps fd 1 in a new *os.File. The original, unreachable once
	// os.Stdout was redirected, would close fd 1 (the protocol stream) when
	// the garbage collector finalized it, so it must outlive Serve.
	runtime.KeepAlive(out)
	if err != nil {
		fail(err)
		return 1
	}
	return 0
}

// fail writes err to stderr as one line.
func fail(err error) {
	fmt.Fprintf(os.Stderr, "agentkit-sandboxd: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
}
