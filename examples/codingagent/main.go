// Command codingagent is a coding agent with the built-in file and shell
// tools, a workspace root, an execute policy and a live event stream.
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./examples/codingagent "Which files define the tool policy?"
//
// The workspace root is the only directory the file tools can reach, and it
// defaults to the current one:
//
//	go run ./examples/codingagent --dir ./tools "Summarise this package."
//	AGENTKIT_MODEL=anthropic/claude-opus-5-5 go run ./examples/codingagent --dir /tmp/scratch
//
// See examples/README.md for the full environment-variable table.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/catalog"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/guard"
	"github.com/agent-fox-dev/agentkit-go/provider/anthropic"
	"github.com/agent-fox-dev/agentkit-go/tools"
	sdk "github.com/anthropics/anthropic-sdk-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// allowedPrograms is the shell allowlist. It is deliberately short: every
// entry is a program whose output the agent reads, none of them writes.
var allowedPrograms = []string{"go", "git", "ls", "cat", "rg"}

func run() error {
	dir := flag.String("dir", ".", "workspace root; the file tools cannot reach outside it")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" {
		task = "List the Go files in this directory and summarise what the package does."
	}

	// An id the catalog does not list still works, with default limits and
	// no price. The lookup here is only for the summary line; New does its
	// own from Config.Model.
	model, _ := catalog.Lookup(modelSpec())

	// 1. The workspace is the containment boundary, not a convenience. Every
	//    path a file tool is handed is resolved against this root — symlinks
	//    included — so a model that asks for ../../etc/passwd is refused by
	//    the tool rather than by a prompt asking it not to.
	ws, err := tools.NewWorkspace(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "workspace: %s\n", ws.Root)

	// 2. All() is the default set: read, write, edit, list, find, search, the
	//    three navigation tools (file_outline, find_symbol, find_references)
	//    and the two shell tools.
	built, err := tools.All(tools.Options{Workspace: ws})
	if err != nil {
		return err
	}

	// 3. The OQ-8 guard. `execute` and `run_command` are in the set above, so
	//    a nil Config.Guard makes agentkit.New return an error wrapping
	//    core.ErrUnguardedExecute — the agent is never built, so no request is
	//    sent, and an unrestricted shell never shows up on a bill. There are
	//    exactly two ways past it: an interceptor, or guard.AllowAll, which is
	//    the explicit "yes, this agent runs an unrestricted shell".
	//
	//    guard.Restricted is the shipped starting point: an allowlist of
	//    program names plus rejection of shell operators (pipes, `;`, `&&`,
	//    redirection, substitution) whose grammar is POSIX sh. It is a FLOOR,
	//    not a sandbox — `go` alone can run arbitrary code through a test
	//    file or a generator — so an embedder that knows what it is running
	//    should replace it rather than widen it.
	policy := guard.Restricted(guard.Options{
		AllowedPrograms: allowedPrograms,
		// A refusal is fed back to the model as a blocked tool result, and it
		// will usually try a different command. Set TerminateOnBlock to end
		// the run instead, for a deployment where a denied call is a signal
		// that something is wrong rather than a wrong first guess.
		TerminateOnBlock: false,
	})

	// 4. Wrapping the policy rather than replacing it is how an interceptor
	//    gains a side effect — here a line per authorized call, so the
	//    terminal shows what the agent is doing to the filesystem. The
	//    decision itself still comes from the policy: the wrapper reports,
	//    it does not judge.
	logged := func(ctx context.Context, in core.BeforeToolCallContext) core.BeforeToolCallDecision {
		d := policy(ctx, in)
		if d.Block {
			fmt.Fprintf(os.Stderr, "  blocked %s: %s\n", in.ToolName, d.Reason)
		} else {
			fmt.Fprintf(os.Stderr, "  allow   %s %s\n", in.ToolName, summarize(in.Arguments))
		}
		return d
	}

	client, err := resolveClient()
	if err != nil {
		return err
	}

	// 5. Turns and budget are separate bounds because they fail differently.
	//    A tool-using agent can loop cheaply for a long time (MaxTurns catches
	//    that) or spend a lot in three turns over a large file (MaxCostUSD
	//    catches that). The run stops on whichever comes first, with
	//    RunStopMaxTurns or RunStopBudgetExceeded as its stop reason.
	agent, err := agentkit.New(agentkit.Config{
		Client: client,
		Model:  model.ID,
		System: "You are a careful coding assistant. Read before you write. " +
			"Prefer the search and read tools over shell commands. Be concise.",
		Tools:      built,
		Guard:      logged,
		MaxTurns:   20,
		MaxCostUSD: 2.00, // dollars, cumulative for the run
	})
	if err != nil {
		return err
	}

	// 6. Streaming, not Run, because a coding agent is slow and silent
	//    otherwise: the tool events are the only evidence that it is working.
	//    The producer never blocks on this loop, so a slow consumer here
	//    cannot stall the run.
	stream, err := agent.Stream(context.Background(), task)
	if err != nil {
		return err
	}

	for e := range stream.Events() {
		switch v := e.(type) {
		case core.TextDeltaEvent:
			fmt.Print(v.Delta)
		case core.TextEndEvent:
			fmt.Println()
		case core.ToolExecutionStartEvent:
			// Emitted after the interceptor allowed the call and before the
			// handler runs, so this is the moment work actually starts.
			fmt.Fprintf(os.Stderr, "  run     %s\n", v.Name)
		case core.ToolResultEvent:
			fmt.Fprintf(os.Stderr, "  result  %s%s\n",
				errMark(v.Message.IsError), firstLine(v.Message.Content.Text()))
		}
	}

	// 7. RunResult is available whether or not the stream was drained, and it
	//    carries the error the loop ended with. A stream that ended on an
	//    error still yielded every event produced before it. Hitting MaxTurns
	//    or MaxCostUSD is an error too (core.ErrMaxTurns,
	//    core.ErrBudgetExceeded), but an expected one: the summary below
	//    reports it as the stop reason instead of failing.
	res, err := stream.RunResult()
	if err != nil && !errors.Is(err, core.ErrMaxTurns) && !errors.Is(err, core.ErrBudgetExceeded) {
		return err
	}

	u := res.Usage
	fmt.Fprintf(os.Stderr, "\n[%s · %d turns · stop %s · in %d / out %d tokens · $%.5f]\n",
		model.ID, res.TurnCount, res.StopReason, u.InputTokens, u.OutputTokens, u.CostUSD)
	return nil
}

// summarize renders tool arguments as one short line. Keys are sorted so two
// runs of the same call print the same way; JSON object order is not stable.
func summarize(args map[string]any) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, truncate(fmt.Sprint(args[k]), 60)))
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return truncate(s, 100)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func errMark(isErr bool) string {
	if isErr {
		return "error: "
	}
	return ""
}

// modelSpec is "vendor/model-id", or a bare id when it is unambiguous.
func modelSpec() string {
	if s := os.Getenv("AGENTKIT_MODEL"); s != "" {
		return s
	}
	return "anthropic/claude-sonnet-5"
}

// resolveClient fails BEFORE the request with the reason a client cannot be
// built — typically the variables to set — rather than after a 401 that names
// none of them. Resolving reads the environment only; a cloud deployment's own
// credentials (Google's, AWS's) are checked on first use.
func resolveClient() (*sdk.Client, error) {
	client, _, err := anthropic.Resolve(anthropic.OSEnv{})
	return client, err
}
