//go:build !windows

// Command freshness shows how a codesearch index stays correct while the files
// under it change — the part of using the index on its own that the tools
// package otherwise does for you. No model, no API key, no network: it builds a
// scratch workspace, edits it, and prints what the index sees at each step.
//
//	cd examples/codesearch
//	go run ./freshness
//
// The rule it demonstrates is one sentence: the index never watches the
// filesystem, so whoever writes a file tells it. tools.All does that for the
// model's own writes (write_file and edit_file call Invalidate(path); the shell
// tools call Invalidate("")). An application that writes files itself, or uses
// the index without tools.All, makes the same calls.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentfox/agentkit-go/codesearch"
	"github.com/agentfox/agentkit-go/core"
	"github.com/agentfox/agentkit-go/tools"
)

func main() {
	log.SetOutput(io.Discard) // zoekt logs shard builds through the global logger
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, w io.Writer) error {
	root, err := os.MkdirTemp("", "codesearch-freshness-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	shards, err := os.MkdirTemp("", "codesearch-shards-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(shards)

	// Eighty files, so that a few edits stay under the 5% threshold below
	// which the index serves changed files from a small overlay instead of
	// rebuilding.
	for i := range 80 {
		write(root, fmt.Sprintf("pkg/file%02d.go", i), fmt.Sprintf("package pkg\n\nfunc Helper%02d() {}\n", i))
	}
	write(root, "pkg/retry.go", "package pkg\n\n// Retry runs fn until it succeeds.\nfunc Retry(fn func() error) error {\n\treturn fn()\n}\n")

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		return err
	}
	idx, err := codesearch.New(ws, codesearch.Options{
		// Both settings make the output identical on every machine: no
		// user-global git excludes, and the built-in outline backends rather
		// than whichever ctags happens to be installed.
		Ignore:       tools.NoGlobalExcludes(),
		DisableCtags: true,
		// Shards go here — outside the workspace, or the index would index
		// itself. Close removes this run's directory, and a later New sweeps
		// sibling directories older than a day left by a process that never
		// reached Close. Empty means os.TempDir().
		TempDir: shards,
	})
	if err != nil {
		return err
	}
	defer idx.Close()

	search := func(q string) string {
		in, _ := json.Marshal(map[string]any{"query": q, "context_lines": 1})
		return summarize(idx.Tools()[0].Execute(ctx, in))
	}
	counts := func() string {
		return fmt.Sprintf("builds=%d overlays=%d revalidations=%d",
			idx.BuildCount(), idx.OverlayBuildCount(), idx.RevalCount())
	}

	// 1. Lazy build. New did nothing; the first query walks and indexes.
	fmt.Fprintln(w, "1. first query builds the index")
	fmt.Fprintf(w, "   sym:Retry      -> %s\n", search("sym:Retry"))
	fmt.Fprintf(w, "   %s\n", counts())

	// 2. A write the index is not told about. Its answer is the content as of
	//    the build: it does not stat files per query, which is what makes a
	//    query cheap.
	write(root, "pkg/retry.go", "package pkg\n\n// Retry runs fn up to n times.\nfunc RetryN(n int, fn func() error) error {\n\treturn fn()\n}\n")
	fmt.Fprintln(w, "2. retry.go rewritten, index not told")
	fmt.Fprintf(w, "   sym:RetryN     -> %s\n", search("sym:RetryN"))

	// 3. Invalidate(path) marks one file dirty. The next query re-reads only
	//    that file into an overlay shard and merges its hits with the main
	//    index, so the edit is visible without a rebuild.
	idx.Invalidate("pkg/retry.go")
	fmt.Fprintln(w, "3. Invalidate(\"pkg/retry.go\")")
	fmt.Fprintf(w, "   sym:RetryN     -> %s\n", search("sym:RetryN"))
	fmt.Fprintf(w, "   %s\n", counts())

	// 4. Symbols — find_symbol's backend — honours the same marks.
	ans, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "RetryN", Exact: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "4. Symbols(RetryN) -> ok=%v matches=%d", ok, len(ans.Matches))
	for _, m := range ans.Matches {
		fmt.Fprintf(w, " %s:%d", m.Path, m.StartLine)
	}
	fmt.Fprintln(w)

	// 5. When you do not know which files changed — a shell command, a git
	//    checkout, a code generator — Invalidate("") asks for a revalidation
	//    walk: one stat per file, re-reading only those whose size or mtime
	//    moved, and picking up new and deleted files.
	//
	//    Here it rebuilds instead (builds goes up), and that is the index being
	//    careful rather than slow: within two seconds of a build an mtime
	//    cannot prove a file unchanged — a write in the same clock tick leaves
	//    it equal — so the walk counts every file as changed, which is far
	//    past the 5% threshold below. In a session that has been running for
	//    more than two seconds, the same call re-reads only the two that moved.
	write(root, "gen/backoff.go", "package gen\n\nfunc Backoff() {}\n")
	if err := os.Remove(filepath.Join(root, "pkg", "file00.go")); err != nil {
		return err
	}
	idx.Invalidate("")
	fmt.Fprintln(w, "5. gen/backoff.go added, pkg/file00.go deleted, Invalidate(\"\")")
	fmt.Fprintf(w, "   sym:Backoff    -> %s\n", search("sym:Backoff"))
	fmt.Fprintf(w, "   sym:Helper00   -> %s\n", search("sym:Helper00"))
	fmt.Fprintf(w, "   %s\n", counts())

	// 6. Past 5% of the index dirty (five of eighty files here), an overlay
	//    stops paying for itself and the next query rebuilds instead. Queries
	//    arriving during a rebuild wait for it (or for their own context),
	//    never for a stale answer.
	for i := 1; i <= 5; i++ {
		rel := fmt.Sprintf("pkg/file%02d.go", i)
		write(root, rel, fmt.Sprintf("package pkg\n\nfunc Renamed%02d() {}\n", i))
		idx.Invalidate(rel)
	}
	fmt.Fprintln(w, "6. five more files rewritten and invalidated (over 5%)")
	fmt.Fprintf(w, "   sym:Renamed    -> %s\n", search("sym:Renamed"))
	fmt.Fprintf(w, "   %s\n", counts())

	// 7. Close is idempotent, and a closed index answers with a result the
	//    model can read rather than a panic.
	if err := idx.Close(); err != nil {
		return err
	}
	fmt.Fprintln(w, "7. after Close")
	fmt.Fprintf(w, "   sym:Retry      -> %s\n", search("sym:Retry"))
	return nil
}

// summarize reduces a code_search result to the files it named, in rank order.
func summarize(r core.ToolResult) string {
	if !r.OK {
		return "error " + r.Error
	}
	files, _ := r.Data["files"].([]any)
	if len(files) == 0 {
		return "no matches"
	}
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.(map[string]any)["path"].(string))
	}
	return strings.Join(paths, ", ")
}

func write(root, rel, content string) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		panic(err)
	}
}
