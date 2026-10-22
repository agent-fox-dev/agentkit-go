// Command indexedsearch shows the code-search index: the zoekt-backed
// code_search tool, how it joins a tool set, how it answers find_symbol, how
// it learns about edits, and what bounds it.
//
//	cd examples/indexedsearch && go run .
//
// It runs offline, with no API key, on a sample project it writes to a
// temporary directory and removes. The calls are made through a Runner, as an
// agent's are; the last section hands the same tools to an agent whose model
// is provider/faux replaying one code_search call.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	agentkit "github.com/agent-fox-dev/agentkit-go"
	"github.com/agent-fox-dev/agentkit-go/codesearch"
	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/provider/faux"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "indexedsearch:", err)
		os.Exit(1)
	}
}

// session is what the sections share: the workspace, the index and a runner
// over the tools that use it.
type session struct {
	ctx    context.Context
	w      io.Writer
	ws     *workspace.Workspace
	idx    *codesearch.Index
	runner *agentkit.Runner
	picked []core.Tool
}

func run(ctx context.Context, w io.Writer) error {
	dir, err := os.MkdirTemp("", "agentkit-indexedsearch-example-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// zoekt reports each shard it writes or loads through the standard
	// logger. This demo keeps those lines out of its output; an embedder
	// decides where they go.
	defer log.SetOutput(log.Writer())
	log.SetOutput(io.Discard)
	if err := writeSample(dir); err != nil {
		return err
	}
	// NoGlobalExcludes keeps the demo the same on every machine.
	ws, err := workspace.Open(dir, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		return err
	}

	// 1. Opt in. New builds nothing: no walk, no process, no file. It returns
	//    a tools.Index, and an error that says why there is none: on
	//    windows/amd64, where zoekt cannot run, it is ErrUnsupported, and an
	//    embedder carries on with Options.Index nil.
	index, err := codesearch.New(ws, codesearch.Options{})
	switch {
	case errors.Is(err, codesearch.ErrUnsupported):
		fmt.Fprintln(w, "code search is not available on this platform; tools.Options{} runs without it")
		return nil
	case err != nil:
		return err
	}
	defer index.Close()

	s := &session{ctx: ctx, w: w, ws: ws, idx: index.(*codesearch.Index)}
	for _, section := range []func() error{
		s.optIn, s.firstQuery, s.syntax, s.scope, s.mistakes, s.symbols, s.edits, s.bounds, s.agent, s.closing,
	} {
		if err := section(); err != nil {
			return err
		}
	}
	fmt.Fprintln(w, "\nCode search example completed successfully")
	return nil
}

// optIn hands the index to tools.For through Options.Index. The tool set gets
// code_search after the built-in tools, and write_file, edit_file and the
// shell tools tell the index about every change they make.
func (s *session) optIn() error {
	built, err := tools.For(s.ws, tools.Options{Index: s.idx})
	if err != nil {
		return err
	}
	// The five tools this demo needs. None is a shell tool, so the runner
	// needs no Guard.
	s.picked, err = core.Named(built, "code_search", "find_symbol", "read_file", "write_file", "edit_file")
	if err != nil {
		return err
	}
	s.runner, err = agentkit.NewRunner(agentkit.Toolset{Tools: s.picked})
	if err != nil {
		return err
	}
	s.head("1. Opt in: codesearch.New, then tools.Options{Index: idx}")
	fmt.Fprintf(s.w, "tools.For returned %d tools; the last is %s, the index's\n", len(built), built[len(built)-1].Name)
	fmt.Fprintf(s.w, "New built nothing yet: builds %d, files indexed %d\n", s.idx.BuildCount(), len(s.idx.IndexedFiles()))

	// find_symbol asks the index first, and the index answers only once a
	// code_search has built it: Symbols never starts a build. Until then
	// find_symbol uses its own symbol table, and says so in its header.
	_, ok, err := s.idx.Symbols(s.ctx, tools.SymbolQuery{Name: "Cart"})
	if err != nil {
		return err
	}
	fmt.Fprintf(s.w, "idx.Symbols before any build: ok=%v, so find_symbol answers from its own table:\n", ok)
	s.call(`{"name":"Cart","kind":"type"}`, "find_symbol")
	return nil
}

// firstQuery runs the first code_search, which builds the index.
func (s *session) firstQuery() error {
	s.head("2. The first code_search builds the index (idx.Build does it ahead of time)")
	res := s.call(`{"query":"Checkout"}`, "code_search")
	st := s.idx.BuildStats()
	fmt.Fprintf(s.w, "builds %d; indexed %d files; skipped: %d binary, %d too small, %d over 1 MiB, %d with too many trigrams\n",
		s.idx.BuildCount(), st.FilesIndexed, st.BinarySkipped, st.TooSmallSkipped, st.OversizedSkipped, st.TooManyTrigramsSkipped)
	fmt.Fprintf(s.w, "Data[\"skipped\"] says the same to a program: %s\n", jsonOf(res.Data["skipped"]))
	shards, _ := os.ReadDir(s.idx.RunDir())
	fmt.Fprintf(s.w, "the index lives in %d shard file(s) in a run directory under the temp directory\n", len(shards))
	return nil
}

// syntax shows the query language, zoekt's.
func (s *session) syntax() error {
	s.head("3. The query language is zoekt's")
	for _, q := range []struct{ about, input string }{
		{"a word is a substring; all lower case matches any case", `{"query":"checkout","max_files":2,"context_lines":0}`},
		{"a capital makes it case-sensitive", `{"query":"Checkout","max_files":2,"context_lines":0}`},
		{"sym: matches declared names only, never a mention, and ranks them first", `{"query":"sym:Cart","context_lines":0}`},
		{"file: limits by a regular expression on the path; - excludes", `{"query":"retry file:payments/ -file:_test","context_lines":0}`},
		{"lang: limits by language; load_orders is in the note as well as the script", `{"query":"lang:python load_orders"}`},
		{"or, and, parentheses; case:no ignores case", `{"query":"(receipt or backoff) case:no -file:docs/","context_lines":0}`},
		{"a regular expression", `{"query":"Err[A-Z][a-z]+","context_lines":0}`},
	} {
		s.sub(q.about)
		res := s.call(q.input, "code_search")
		if strings.HasPrefix(q.input, `{"query":"sym:`) {
			s.dataOfFirstFile(res)
		}
	}
	return nil
}

// dataOfFirstFile prints what a program reads from a code_search result.
func (s *session) dataOfFirstFile(res core.ToolResult) {
	files, _ := res.Data["files"].([]any)
	if len(files) == 0 {
		return
	}
	first, _ := files[0].(map[string]any)
	fmt.Fprintf(s.w, "  Data[\"files\"][0] for a program: path %v, %v matches, symbols %v; also its score and its chunks of lines\n",
		first["path"], first["matches"], jsonOf(first["symbols"]))
}

// scope shows the three arguments besides the query.
func (s *session) scope() error {
	s.head("4. path, max_files and context_lines")
	for _, q := range []struct{ about, input string }{
		{"path: only under a directory (or one file)", `{"query":"Total","path":"cart"}`},
		{"the workspace's ignore rules apply: .gitignore lists build/, so its file is not indexed", `{"query":"SecretBuildOutput"}`},
		{"max_files: more files matched than asked for, and the marker names the call that works", `{"query":"Gen","max_files":3,"context_lines":0}`},
		{"context_lines: 0 for the matched lines alone, up to 20 around them", `{"query":"sym:Pay","context_lines":6}`},
	} {
		s.sub(q.about)
		s.call(q.input, "code_search")
	}
	return nil
}

// mistakes shows that a refused query is a result with a code, not a Go error.
func (s *session) mistakes() error {
	s.head("5. A query that cannot run is a result with an error code")
	for _, q := range []struct{ about, input string }{
		{"an empty query", `{"query":"  "}`},
		{"a query zoekt cannot parse", `{"query":"("}`},
		{"a path outside the workspace", `{"query":"x","path":"../.."}`},
	} {
		s.sub(q.about)
		s.call(q.input, "code_search")
	}
	return nil
}

// symbols shows find_symbol now served by the index.
func (s *session) symbols() error {
	s.head("6. find_symbol is answered by the index once it is built")
	_, ok, err := s.idx.Symbols(s.ctx, tools.SymbolQuery{Name: "Cart"})
	if err != nil {
		return err
	}
	fmt.Fprintf(s.w, "idx.Symbols now: ok=%v; compare the header with section 1\n", ok)
	s.call(`{"name":"Cart","kind":"type"}`, "find_symbol")
	s.call(`{"name":"load_orders"}`, "find_symbol")
	return nil
}

// edits shows how the index learns what changed.
func (s *session) edits() error {
	s.head("7. Edits: the index is told, and the next search sees them")

	s.sub("write_file through the tool set marks the file; the index searches it from a small overlay")
	s.call(`{"path":"checkout/discount.go","content":"package checkout\n\n// LoyaltyDiscount is the share of a total a returning customer saves.\nfunc LoyaltyDiscount(total int) int { return total / 10 }\n"}`, "write_file")
	res := s.call(`{"query":"LoyaltyDiscount","context_lines":0}`, "code_search")
	fmt.Fprintf(s.w, "builds %d (no rebuild), overlay builds %d, dirty files %v\n", s.idx.BuildCount(), s.idx.OverlayBuildCount(), res.Data["dirty_files"])
	s.call(`{"name":"LoyaltyDiscount"}`, "find_symbol")

	s.sub("an edit made outside the tool set is invisible until Invalidate names the file")
	cart := filepath.Join(s.ws.Root(), "cart", "cart.go")
	old, err := os.ReadFile(cart)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cart, append(old, "\n// ExpressLane is the till for baskets of ten items or fewer.\n"...), 0o644); err != nil {
		return err
	}
	fmt.Fprint(s.w, "after os.WriteFile:   ")
	s.count(`{"query":"ExpressLane"}`)
	s.idx.Invalidate("cart/cart.go")
	fmt.Fprintf(s.w, "after Invalidate(%q): ", "cart/cart.go")
	s.count(`{"query":"ExpressLane"}`)

	s.sub(`Invalidate("") says anything may have changed (a git checkout, a build step); the next search walks the tree`)
	before := s.idx.RevalCount()
	s.idx.Invalidate("")
	s.count(`{"query":"LoyaltyDiscount"}`)
	fmt.Fprintf(s.w, "revalidation walks: %d -> %d\n", before, s.idx.RevalCount())

	s.sub("more than 5% of the indexed files edited: the next search rebuilds instead of overlaying")
	for _, name := range []string{"gen/gen_01.go", "gen/gen_02.go", "gen/gen_03.go"} {
		s.call(fmt.Sprintf(`{"path":%q,"edits":[{"old_string":"is generated","new_string":"was edited"}]}`, name), "edit_file")
	}
	res = s.call(`{"query":"was edited","context_lines":0,"max_files":1}`, "code_search")
	fmt.Fprintf(s.w, "builds %d (it rebuilt), dirty files %v\n", s.idx.BuildCount(), res.Data["dirty_files"])
	return nil
}

// bounds shows an index that stopped at a limit.
func (s *session) bounds() error {
	s.head("8. Bounds: MaxFiles, MaxBytes and MaxBuildTime stop a build, and the index says so")
	fmt.Fprintf(s.w, "the full index holds %d files\n", len(s.idx.IndexedFiles()))
	fmt.Fprintln(s.w, "a partial index still searches what it has, but it does not answer find_symbol:")
	fmt.Fprintln(s.w, "idx.Symbols reports ok=false, and find_symbol builds and uses its own table")
	for _, b := range []struct {
		label string
		o     codesearch.Options
	}{
		{"Options{MaxFiles: 10}", codesearch.Options{MaxFiles: 10}},
		{"Options{MaxBytes: 4000}", codesearch.Options{MaxBytes: 4_000}},
	} {
		if err := s.bounded(b.label, b.o); err != nil {
			return err
		}
	}
	return nil
}

// bounded builds an index under o and asks it, without a Runner, what it has.
func (s *session) bounded(label string, o codesearch.Options) error {
	index, err := codesearch.New(s.ws, o)
	if err != nil {
		return err
	}
	defer index.Close()
	part := index.(*codesearch.Index)
	// Build does what the first code_search would do, ahead of time.
	if err := part.Build(s.ctx); err != nil {
		return err
	}
	// The tool's own Execute, and the index's Symbols, called directly.
	res := index.Tools()[0].Execute(s.ctx, json.RawMessage(`{"query":"Cart","max_files":1,"context_lines":0}`))
	_, ok, err := index.Symbols(s.ctx, tools.SymbolQuery{Name: "Cart"})
	if err != nil {
		return err
	}
	fmt.Fprintf(s.w, "\n# %s\n  indexed %d files; partial=%v, limit %q\n  note: %s\n  Symbols ok=%v\n",
		label, part.BuildStats().FilesIndexed, res.Data["partial"], res.Data["partial_reason"],
		lastLine(fmt.Sprint(res.Data["note"])), ok)
	return nil
}

// agent hands the tools to an agent.
func (s *session) agent() error {
	s.head("9. In an agent: the model is offered code_search with the other tools")
	model := faux.New(
		faux.ToolCall("call_1", "code_search", `{"query":"retry file:payments/ -file:_test","context_lines":1}`),
		faux.Text("Retries happen in payments.Stripe.Pay, with a growing wait; checkout.Checkout never retries."),
	)
	agent, err := agentkit.New(agentkit.Config{
		Client:   model,
		Model:    faux.Model().ID,
		Toolset:  agentkit.Toolset{Tools: s.picked},
		MaxTurns: 4,
	})
	if err != nil {
		return err
	}
	var names []string
	for _, t := range agent.Runner().Tools() {
		names = append(names, t.Name)
	}
	fmt.Fprintf(s.w, "tools the model sees: %s\n", strings.Join(names, ", "))
	res, err := agent.Run(s.ctx, "Where are payments retried?")
	if err != nil {
		return err
	}
	for _, m := range res.Messages {
		if r, ok := m.(core.ToolResultMessage); ok {
			fmt.Fprintf(s.w, "%s returned to the model:\n%s\n", r.ToolName, indent(r.Content.Text(), 4))
		}
	}
	fmt.Fprintf(s.w, "model: %s\n", res.FinalText())
	return nil
}

// closing shows the lifecycle's end.
func (s *session) closing() error {
	s.head("10. Close releases the shards")
	dir := s.idx.RunDir()
	_, before := os.Stat(dir)
	if err := s.idx.Close(); err != nil {
		return err
	}
	_, after := os.Stat(dir)
	again := s.idx.Close()
	fmt.Fprintf(s.w, "run directory present before Close: %v, after: %v; a second Close returns %v\n",
		before == nil, after == nil, again)
	s.call(`{"query":"Checkout"}`, "code_search")
	return nil
}

// call runs one tool through the runner and prints the call and its result.
func (s *session) call(input, tool string) core.ToolResult {
	res := s.runner.Call(s.ctx, tool, json.RawMessage(input))
	fmt.Fprintf(s.w, "$ %s %s\n", tool, input)
	show(s.w, res, 14)
	return res
}

// count runs a code_search and prints only how many files it found.
func (s *session) count(input string) {
	res := s.runner.Call(s.ctx, "code_search", json.RawMessage(input))
	files, _ := res.Data["files"].([]any)
	fmt.Fprintf(s.w, "%d file(s) found\n", len(files))
}

func (s *session) head(title string) { fmt.Fprintf(s.w, "\n## %s\n", title) }
func (s *session) sub(title string)  { fmt.Fprintf(s.w, "\n# %s\n", title) }

// show prints the text a model reads, or the error code and detail.
func show(w io.Writer, res core.ToolResult, maxLines int) {
	if !res.OK {
		fmt.Fprintf(w, "  error %s: %s\n", res.Error, res.Detail)
		return
	}
	lines := strings.Split(strings.TrimRight(res.LLMText(), "\n"), "\n")
	more := 0
	if len(lines) > maxLines {
		lines, more = lines[:maxLines], len(lines)-maxLines
	}
	fmt.Fprintln(w, indent(strings.Join(lines, "\n"), 2))
	if more > 0 {
		fmt.Fprintf(w, "  ... %d more lines\n", more)
	}
}

// lastLine is the last line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	return pad + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n"+pad)
}

func jsonOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return err.Error()
	}
	return string(b)
}
