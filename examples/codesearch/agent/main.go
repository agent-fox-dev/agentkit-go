//go:build !windows

// Command agent gives a read-only coding agent the code_search tool. It is
// examples/codingagent with one addition — an index — and the three lines that
// addition costs are the point of the example.
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	cd examples/codesearch
//	go run ./agent --dir ../.. "Where is the retry logic for provider requests?"
//	AGENTKIT_MODEL=openai/gpt-5.6-terra go run ./agent --dir ../.. "Which types implement tools.Index?"
//
// With --compare the same question is asked twice, once without the index, and
// the two runs' tool calls and cost are printed side by side.
//
// See examples/README.md for the full environment-variable table.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	agentkit "github.com/agentfox/agentkit-go"
	"github.com/agentfox/agentkit-go/catalog"
	"github.com/agentfox/agentkit-go/codesearch"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/provider"
	"github.com/agentfox/agentkit-go/provider/anthropic"
	"github.com/agentfox/agentkit-go/provider/google"
	"github.com/agentfox/agentkit-go/provider/ollama"
	"github.com/agentfox/agentkit-go/provider/openai"
	"github.com/agentfox/agentkit-go/provider/openairesponses"
	"github.com/agentfox/agentkit-go/stop"
	"github.com/agentfox/agentkit-go/tools"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// readOnlyTools is the allowlist. Everything that reads is in it; nothing that
// writes or runs a shell is — so this agent needs no execute guard, and the
// index never has to be told about a write.
//
// code_search is not a built-in, so naming it here is required: an allowlist
// that omits it hides the tool even though tools.All returned it.
var readOnlyTools = []string{
	"read_file", "list_files", "find_files", "search_files",
	"file_outline", "find_symbol",
	"code_search",
}

func run() error {
	dir := flag.String("dir", ".", "workspace root; the file tools cannot reach outside it")
	compare := flag.Bool("compare", false, "also answer without the index and compare the two runs")
	flag.Parse()

	task := strings.Join(flag.Args(), " ")
	if task == "" {
		task = "Which types implement the tools.Index interface, and where are they constructed?"
	}

	// zoekt logs shard builds through the global logger; keep stderr for the
	// tool trace below.
	log.SetOutput(io.Discard)

	model, err := catalog.ResolveModel(modelSpec())
	if err != nil {
		return err
	}
	if err := checkCredentials(model); err != nil {
		return err
	}

	ws, err := tools.NewWorkspace(*dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "workspace: %s\n", ws.Root)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	with, err := ask(ctx, model, ws, task, true)
	if err != nil {
		return err
	}
	if !*compare {
		return nil
	}
	without, err := ask(ctx, model, ws, task, false)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%-14s %6s %8s %10s\n", "", "turns", "tools", "cost")
	for _, r := range []struct {
		name string
		s    summary
	}{{"with index", with}, {"without index", without}} {
		fmt.Fprintf(os.Stderr, "%-14s %6d %8d %10s\n", r.name, r.s.turns, r.s.toolCalls, fmt.Sprintf("$%.5f", r.s.cost))
	}
	return nil
}

type summary struct {
	turns, toolCalls int
	cost             float64
}

func ask(ctx context.Context, model *core.Model, ws *tools.Workspace, task string, withIndex bool) (summary, error) {
	opts := tools.Options{Workspace: ws}

	if withIndex {
		// The whole opt-in. New is free — nothing is walked or written until
		// the model's first code_search — so an agent that never searches
		// pays nothing for having the tool.
		idx, err := codesearch.New(ws, codesearch.Options{
			// The same ignore configuration the file tools use, so
			// code_search and search_files agree on what the workspace is.
			Ignore: opts.Ignore,
		})
		switch {
		case errors.Is(err, codesearch.ErrUnsupported):
			// Not reachable from this file (it does not build on Windows),
			// but this is the shape an embedder that does build there
			// needs: no index, and the agent falls back to search_files.
			fmt.Fprintln(os.Stderr, "code search unavailable on this platform; continuing without it")
		case err != nil:
			return summary{}, err
		default:
			// Close after the run, not after tools.All: the tools hold the
			// index and query it for as long as the agent runs.
			defer idx.Close()
			// With Index set, tools.All appends code_search after the
			// built-ins, write_file/edit_file/the shell tools call
			// Invalidate on every write, and find_symbol answers from the
			// index once a code_search has built it.
			opts.Index = idx
		}
	}

	built, err := tools.All(opts)
	if err != nil {
		return summary{}, err
	}

	cfg := core.AgentConfig{Model: model}
	agentkit.RegisterDefaults(&cfg,
		anthropic.Provider(anthropic.Options{}),
		openai.Provider(openai.Options{}),
		openairesponses.Provider(openairesponses.Options{}),
		google.Provider(google.Options{}),
		ollama.Provider(ollama.Options{}),
	)
	// An allowlist naming a tool that is not registered is simply that tool
	// absent, so the same list serves the run without an index.
	cfg.ToolPolicy.ToolNames = readOnlyTools
	cfg.StopPolicy = stop.Any(stop.AfterTurns(20), stop.OverBudget(1.00))
	// The tool's own PromptGuidelines entry already tells the model when to
	// prefer code_search over search_files; the system prompt need not.
	cfg.SystemPrompt = "You are a careful assistant answering questions about a codebase. " +
		"Cite file paths and line numbers. Be concise."

	agent, err := agentkit.NewAgent(cfg)
	if err != nil {
		return summary{}, err
	}
	for _, t := range built {
		if err := agent.RegisterTool(t); err != nil {
			return summary{}, err
		}
	}

	label := "without index"
	if withIndex {
		label = "with index"
	}
	fmt.Fprintf(os.Stderr, "\n== %s ==\n", label)

	stream, err := agent.Stream(ctx, task)
	if err != nil {
		return summary{}, err
	}
	var s summary
	for e := range stream.Events() {
		switch v := e.(type) {
		case core.TextDeltaEvent:
			fmt.Print(v.Delta)
		case core.TextEndEvent:
			fmt.Println()
		case core.ToolExecutionStartEvent:
			s.toolCalls++
			fmt.Fprintf(os.Stderr, "  %s\n", v.Name)
		case core.ToolResultEvent:
			if v.Message.IsError {
				fmt.Fprintf(os.Stderr, "    error: %s\n", firstLine(v.Message.Content.Text()))
			} else if v.Message.ToolName == "code_search" {
				// The first line of a code_search result is the index's own
				// summary: file count, size, symbol backends, partial or not.
				fmt.Fprintf(os.Stderr, "    %s\n", firstLine(v.Message.Content.Text()))
			}
		}
	}
	res, err := stream.RunResult()
	if err != nil {
		return summary{}, err
	}
	s.turns, s.cost = res.TurnCount, res.Usage.CostUSD
	fmt.Fprintf(os.Stderr, "[%s · %d turns · %d tool calls · $%.5f]\n", model.ID, s.turns, s.toolCalls, s.cost)
	return s, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

// modelSpec is "vendor/model-id", or a bare id when it is unambiguous.
func modelSpec() string {
	if s := os.Getenv("AGENTKIT_MODEL"); s != "" {
		return s
	}
	return "anthropic/claude-sonnet-5"
}

// checkCredentials fails BEFORE any index is built or request sent, with a
// message naming the variable to set. "ambient" (an instance role, ADC) passes;
// only "none" fails.
func checkCredentials(m *core.Model) error {
	var auth provider.VendorAuth
	switch m.API {
	case anthropic.API:
		auth = anthropic.VendorAuth
	case google.API:
		auth = google.VendorAuth
	case ollama.API:
		auth = ollama.VendorAuth
	default:
		auth = openai.AuthFor(m.Provider)
	}
	if provider.ResolveAuth(auth, provider.Env{}).State != provider.CredentialNone {
		return nil
	}
	names := make([]string, 0, len(auth.Vars))
	for _, v := range auth.Vars {
		names = append(names, v.Name)
	}
	return fmt.Errorf("no credential for vendor %q: set one of %s (see examples/README.md)",
		m.Provider, strings.Join(names, ", "))
}
