package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/agent-fox-dev/agentkit-go/mcpserve"
	"github.com/agent-fox-dev/agentkit-go/wire"
)

// runMCP serves one MCP session on stdin and stdout, as mcpserve.ServeStdio
// does, until the client closes its end. The index stays open for the whole
// session, so the first code_search builds it and later writes invalidate it.
// Nothing but protocol frames goes to stdout; logs go to stderr.
func runMCP(args []string, _, stderr io.Writer) int {
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
	srv, err := open(s, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitCode(err)
	}
	defer srv.close()
	server, err := mcpserve.New(srv.runner, mcpserve.Options{
		Name:    "agentkit-tools",
		Version: versionFrom(buildInfo()),
		Logger:  slog.New(slog.NewTextHandler(stderr, nil)),
	})
	if err != nil {
		fmt.Fprintf(stderr, "agentkit-tools: mcp: %v\n", err)
		return 1
	}
	ctx, signalled, stop := withSignals(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- server.ServeStdio(ctx) }()
	select {
	case err = <-served:
	case <-ctx.Done():
		// A signal ended the session: ServeStdio has cancelled the running
		// calls, but it can stay blocked closing a stdin that is a blocking
		// pipe, whose pending read Close does not interrupt. Give it the same
		// grace as a call's timeout, then exit with the signal's status.
		select {
		case err = <-served:
		case <-time.After(timeoutGrace):
		}
	}
	if code := signalled(); code != 0 {
		return code
	}
	if err == nil {
		return 0
	}
	// The error's own text names the keys of a rejected frame, which are
	// workspace content: only the rule it broke is reported.
	var we *wire.Error
	if errors.As(err, &we) {
		fmt.Fprintf(stderr, "agentkit-tools: mcp session ended: frame rejected: %s\n", we.Rule)
	} else {
		fmt.Fprintln(stderr, "agentkit-tools: mcp session ended: session error")
	}
	return 1
}
