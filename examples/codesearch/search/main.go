//go:build !windows

// Command search uses the codesearch index on its own: no agent, no model, no
// API key. It indexes a directory and answers zoekt queries and symbol lookups
// from it, the same way the code_search and find_symbol tools would for a
// model.
//
//	cd examples/codesearch
//	go run ./search --dir ../.. 'sym:NewWorkspace'
//	go run ./search --dir ../.. 'retry file:\.go$ -file:_test' 'lang:go "func New"'
//	go run ./search --dir ../.. --path tools --max-files 3 'ClampLimit'
//	go run ./search --dir ../.. --symbol Runner --kind func
//	go run ./search --dir ../.. --json 'sym:Build'
//
// Every query runs against one index, built once on the first query, so the
// second and later queries are cheap. That is the point of keeping an Index
// around rather than calling a search function per query.
//
// zoekt does not build on Windows, so neither does this example; on that
// platform codesearch.New returns codesearch.ErrUnsupported.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/agentfox/agentkit-go/codesearch"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// config is the parsed command line, separate from the flag set so the test
// can drive search with a literal.
type config struct {
	dir          string
	path         string
	maxFiles     int
	contextLines int
	symbol       string
	kind         string
	exact        bool
	asJSON       bool
	queries      []string
}

func run(args []string, stdout, stderr io.Writer) error {
	var c config
	fs := flag.NewFlagSet("search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.dir, "dir", ".", "directory to index; nothing outside it is read")
	fs.StringVar(&c.path, "path", "", "restrict every query to this file or directory (relative to --dir)")
	fs.IntVar(&c.maxFiles, "max-files", 0, "files per query (default 10, capped at 25)")
	fs.IntVar(&c.contextLines, "context", 0, "context lines around each match (default 2)")
	fs.StringVar(&c.symbol, "symbol", "", "look up a declaration by name instead of running a query")
	fs.StringVar(&c.kind, "kind", "", "with --symbol: only this kind (func, method, type, …)")
	fs.BoolVar(&c.exact, "exact", false, "with --symbol: exact name instead of prefix")
	fs.BoolVar(&c.asJSON, "json", false, "print the structured result instead of the rendered text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c.queries = fs.Args()
	if len(c.queries) == 0 && c.symbol == "" {
		c.queries = []string{"sym:NewWorkspace"}
	}

	// zoekt reports shard builds and loads through the standard library's
	// global logger. That is the process's logger, not the index's, so
	// silencing it is the application's call — and a CLI that prints results
	// on stdout wants it quiet.
	log.SetOutput(io.Discard)

	// Ctrl-C cancels the context, and both a build and a query honour it: a
	// cancelled build is discarded rather than kept half-done.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return search(ctx, c, stdout, stderr)
}

func search(ctx context.Context, c config, stdout, stderr io.Writer) error {
	// 1. The workspace is the same containment boundary the file tools use.
	//    The index walks only inside it, and a --path that escapes it is
	//    refused by code_search with path_not_allowed.
	ws, err := tools.NewWorkspace(c.dir)
	if err != nil {
		return err
	}

	// 2. New does no work: no goroutine, no walk, no disk. Everything is
	//    deferred to the first query (or an explicit Build), so constructing
	//    an index for an agent that never searches costs nothing.
	//
	//    Ignore is the same value you would pass to tools.Options.Ignore —
	//    .gitignore files are honoured either way; this only controls the
	//    user-global excludes layer.
	index, err := codesearch.New(ws, codesearch.Options{
		// The three bounds below are the defaults, spelled out. When one
		// stops a build the index keeps what it has, marks itself partial,
		// and says so on the first line of every result.
		MaxFiles:     100_000,
		MaxBytes:     1 << 30,
		MaxBuildTime: 60 * time.Second,
	})
	if errors.Is(err, codesearch.ErrUnsupported) {
		return fmt.Errorf("code search is not available on this platform: %w", err)
	}
	if err != nil {
		return err
	}
	// New returns a tools.Index, which is all an embedder needs. This example
	// also builds explicitly and reads the build statistics, which live on
	// the concrete type.
	idx := index.(*codesearch.Index)
	// 3. Close deletes the shard directory under TempDir. It waits for any
	//    query still running, and a second call is a no-op.
	defer idx.Close()

	// 4. Building explicitly is optional — the first query would do it — but
	//    doing it up front separates "how long does indexing take" from "how
	//    long does a query take", which is what you want to measure.
	start := time.Now()
	if err := idx.Build(ctx); err != nil {
		return fmt.Errorf("building index: %w", err)
	}
	st := idx.BuildStats()
	fmt.Fprintf(stderr, "indexed %d files in %s (skipped %d binary, %d over 1 MiB)\n",
		st.FilesIndexed, time.Since(start).Round(time.Millisecond), st.BinarySkipped, st.OversizedSkipped)

	if c.symbol != "" {
		return lookupSymbol(ctx, idx, c, stdout)
	}

	// 5. The index's only tool is code_search. Calling its Execute directly is
	//    exactly what the agent loop does after validating the model's
	//    arguments, so this is the whole query surface — not a reduced one.
	codeSearch := idx.Tools()[0]
	for i, q := range c.queries {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintf(stdout, "## %s\n", q)

		in, err := json.Marshal(map[string]any{
			"query":         q,
			"path":          c.path,
			"max_files":     c.maxFiles,
			"context_lines": c.contextLines,
		})
		if err != nil {
			return err
		}
		start := time.Now()
		r := codeSearch.Execute(ctx, in)
		elapsed := time.Since(start)

		// 6. A bad query is a result, not a Go error: the model has to be able
		//    to read it and try again. Error is a stable code
		//    (invalid_arguments, path_not_allowed, search_failed, aborted…)
		//    and Detail says what to change.
		if !r.OK {
			fmt.Fprintf(stdout, "%s: %s\n", r.Error, r.Detail)
			continue
		}
		if err := printResult(stdout, r, c.asJSON); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "(%s)\n", elapsed.Round(time.Microsecond))
	}
	return nil
}

// printResult prints what the model would see (Text), or the structured Data
// an application would consume instead.
func printResult(w io.Writer, r core.ToolResult, asJSON bool) error {
	if !asJSON {
		_, err := fmt.Fprintln(w, r.Text)
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r.Data)
}

// lookupSymbol answers a find_symbol-style query from the index. Symbols is
// the method tools.All's find_symbol calls when an index is set; here it is
// called directly.
func lookupSymbol(ctx context.Context, idx *codesearch.Index, c config, w io.Writer) error {
	ans, ok, err := idx.Symbols(ctx, tools.SymbolQuery{
		Name:  c.symbol,
		Kind:  strings.ToLower(c.kind),
		Path:  strings.Trim(c.path, "/"),
		Exact: c.exact,
	})
	if err != nil {
		return err
	}
	// ok=false is not an error. It means "I cannot answer authoritatively"
	// — the index is partial, closed, not yet built, or the name matched
	// more than 1000 declarations — and find_symbol falls back to its own
	// table when it sees it.
	if !ok {
		return fmt.Errorf("the index cannot answer %q authoritatively; narrow the name or --path", c.symbol)
	}
	fmt.Fprintf(w, "%d matches for %q in %d indexed files\n", len(ans.Matches), c.symbol, ans.FilesIndexed)
	for _, m := range ans.Matches {
		// The signature is the declaration's first line when the backend
		// recorded one; the qualified name otherwise.
		decl := m.Signature
		if decl == "" {
			decl = m.Name
			if m.Container != "" {
				decl = m.Container + "." + decl
			}
		}
		fmt.Fprintf(w, "  %s:%d  %-6s %s\n", m.Path, m.StartLine, m.Kind, decl)
	}
	return nil
}
