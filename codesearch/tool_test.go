//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/schema"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

func TestCodeSearchIsABuiltinParallelTool(t *testing.T) {
	tl := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{}).Tools()
	if len(tl) != 1 || tl[0].Name != "code_search" || !tl[0].Builtin || tl[0].ExecutionMode != core.Parallel {
		t.Fatalf("Tools() = %+v, want one builtin parallel code_search", tl)
	}
	if s := tl[0].InputSchema; !s.IsRequired("query") || s.IsRequired("path") || s.IsRequired("max_files") || s.IsRequired("context_lines") {
		t.Errorf("only query is required: %+v", s)
	}
}

// Bad arguments are refused before any build, each with its error code.
func TestCodeSearchRefusesBadArgumentsBeforeBuilding(t *testing.T) {
	idx := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{})
	for _, c := range []struct {
		in, code, detail string
	}{
		{`{malformed`, "invalid_arguments", ""},
		{`{"query":""}`, "invalid_arguments", "must not be empty"},
		{`{"query":"   "}`, "invalid_arguments", "must not be empty"},
		{fmt.Sprintf(`{"query":%q}`, strings.Repeat("a", maxQueryBytes+1)), "invalid_arguments", "1024 byte limit"},
		{`{"query":"("}`, "invalid_arguments", "query parse error"},
		{`{"query":"x","context_lines":-1}`, "invalid_arguments", "must not be negative"},
		{`{"query":"x","path":"../outside"}`, "path_not_allowed", ""},
		{`{"query":"x","path":"nope/missing"}`, "read_failed", ""},
	} {
		r := idx.Tools()[0].Execute(context.Background(), json.RawMessage(c.in))
		if r.OK || r.Error != c.code || !strings.Contains(r.Detail, c.detail) {
			t.Errorf("%s: %v %q %q, want %s with %q", c.in, r.OK, r.Error, r.Detail, c.code, c.detail)
		}
	}
	if n := idx.BuildCount(); n != 0 {
		t.Errorf("BuildCount = %d, want 0", n)
	}
}

// max_files defaults to 10 and is capped at 25; context_lines defaults to 2
// only when absent, 0 means none, and it is capped at MaxSearchContextLines.
func TestMaxFilesAndContextLinesDefaultAndClamp(t *testing.T) {
	var long strings.Builder
	long.WriteString("package pkg\n")
	for ln := 2; ln <= 60; ln++ {
		if ln == 30 {
			long.WriteString("// matchword\n")
		} else {
			fmt.Fprintf(&long, "// line %d\n", ln)
		}
	}
	idx := newFixture(t, with(numbered(40, "pkg/f%03d.go", "package pkg\n// matchword\n"), map[string]string{"long.go": long.String()}), Options{})
	for _, c := range []struct {
		kv []any
		n  int
	}{{nil, 10}, {[]any{"max_files", 0}, 10}, {[]any{"max_files", 3}, 3}, {[]any{"max_files", 100}, 25}} {
		if got := len(paths(find(t, idx, "matchword", c.kv...))); got != c.n {
			t.Errorf("%v: %d files, want %d", c.kv, got, c.n)
		}
	}
	for _, c := range []struct {
		kv    []any
		lines int
	}{{nil, 5}, {[]any{"context_lines", 0}, 1}, {[]any{"context_lines", 500}, 2*tools.MaxSearchContextLines + 1}} {
		r := find(t, idx, "matchword", append([]any{"path", "long.go"}, c.kv...)...)
		chunks := r.Data["files"].([]any)[0].(map[string]any)["chunks"].([]any)
		if got := len(chunks[0].(map[string]any)["lines"].([]any)); len(chunks) != 1 || got != c.lines {
			t.Errorf("%v: %d chunks, %d lines, want one chunk of %d", c.kv, len(chunks), got, c.lines)
		}
	}
}

// path is a case-sensitive conjunction with the query: a directory is a
// prefix with its slash, a file is itself, and the root adds nothing.
func TestPathConfinesTheQuery(t *testing.T) {
	needCaseSensitiveFS(t)
	const body = "package p\n// retry fetch\n"
	idx := newFixture(t, map[string]string{
		"pkg/a/x.go": body, "pkg/ab/y.go": body, "other/z.go": body, "c++[1]/w.go": body, "src/s.go": body, "Src/S.go": body,
	}, Options{})
	for path, want := range map[string][]string{
		"pkg/a":      {"pkg/a/x.go"},
		"pkg/a/x.go": {"pkg/a/x.go"},
		"c++[1]":     {"c++[1]/w.go"},
		"src":        {"src/s.go"},
		".":          {"Src/S.go", "c++[1]/w.go", "other/z.go", "pkg/a/x.go", "pkg/ab/y.go", "src/s.go"},
	} {
		got := paths(find(t, idx, "retry or fetch", "path", path))
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("path %q: %v, want %v", path, got, want)
		}
	}
}

func TestACancelledCallIsAborted(t *testing.T) {
	idx := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := call(ctx, idx, map[string]any{"query": "main"}); r.Error != "aborted" || r.Detail != "Operation aborted" {
		t.Errorf("= %v %q %q, want aborted", r.OK, r.Error, r.Detail)
	}
}

func TestABuildThatCannotWriteItsShardsIsIndexFailed(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	testutil.WriteTree(t, filepath.Dir(notADir), map[string]string{"file": "x"})
	idx := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{TempDir: notADir})
	if r := call(context.Background(), idx, map[string]any{"query": "main"}); r.Error != "index_failed" || r.Detail == "" {
		t.Errorf("= %v %q %q, want index_failed with its cause", r.OK, r.Error, r.Detail)
	}
}

// A failing search is search_failed; one that runs out of the query timeout
// says to narrow it; one whose caller gives up is aborted.
func TestAFailedSearchIsSearchFailed(t *testing.T) {
	for _, c := range []struct {
		name         string
		hook         func(ctx context.Context, cancel func()) error
		code, detail string
	}{
		{"an error", func(context.Context, func()) error { return errors.New("injected") }, "search_failed", "injected"},
		{"the query timeout", func(ctx context.Context, _ func()) error { <-ctx.Done(); return ctx.Err() },
			"search_failed", "query timed out; narrow the query or add a path constraint"},
		{"the caller gives up", func(ctx context.Context, cancel func()) error { cancel(); <-ctx.Done(); return ctx.Err() },
			"aborted", "Operation aborted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{})
			idx.queryTimeout = 50 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			idx.testSearchHook = func(sctx context.Context, _ string) (*searchHookResult, error) {
				return nil, c.hook(sctx, cancel)
			}
			if r := call(ctx, idx, map[string]any{"query": "main"}); r.Error != c.code || !strings.Contains(r.Detail, c.detail) {
				t.Errorf("= %v %q %q, want %s %q", r.OK, r.Error, r.Detail, c.code, c.detail)
			}
		})
	}

	idx := newFixture(t, map[string]string{"main.go": "package main\n"}, Options{})
	idx.queryTimeout = time.Minute
	if got := paths(find(t, idx, "main")); !slices.Equal(got, []string{"main.go"}) {
		t.Errorf("a search under a set timeout found %v", got)
	}
}

// New and Symbols build nothing; the first code_search builds, once.
func TestTheFirstCodeSearchBuildsTheIndexOnce(t *testing.T) {
	idx := newFixture(t, map[string]string{"main.go": "package main\nfunc main() {}\n"}, Options{})
	if _, ok, _ := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "main"}); ok || idx.BuildCount() != 0 {
		t.Fatalf("Symbols before any code_search: ok %v, BuildCount %d", ok, idx.BuildCount())
	}
	for range 2 {
		find(t, idx, "main")
		if n := idx.BuildCount(); n != 1 {
			t.Fatalf("BuildCount = %d, want 1", n)
		}
	}
}

// Past 5% of the indexed files dirty, a query rebuilds the index once and
// removes the old shards; at 4% the overlay serves them.
func TestMoreThanFivePercentDirtyRebuildsTheIndex(t *testing.T) {
	idx := newFixture(t, filler(100), Options{})
	find(t, idx, "token000")
	oldRunDir := idx.RunDir()
	edit := func(from, to int) {
		for i := from; i < to; i++ {
			rel := fmt.Sprintf("f%03d.go", i)
			write(t, idx, rel, fmt.Sprintf("package pkg\n// updated%03d\nfunc F%03d() {}\n", i, i))
			idx.Invalidate(rel)
		}
	}
	edit(0, 4)
	if r := find(t, idx, "updated"); idx.BuildCount() != 1 || r.Data["dirty_files"] != 4 {
		t.Fatalf("at 4%%: BuildCount %d, dirty_files %v, want 1 and 4", idx.BuildCount(), r.Data["dirty_files"])
	}
	edit(4, 6)
	r := find(t, idx, "updated")
	if idx.BuildCount() != 2 || r.Data["dirty_files"] != 0 || len(paths(r)) != 6 {
		t.Errorf("at 6%%: BuildCount %d, dirty_files %v, %d files; want 2, 0 and 6", idx.BuildCount(), r.Data["dirty_files"], len(paths(r)))
	}
	if !gone(oldRunDir) {
		t.Errorf("the old run directory %s survived the rebuild", oldRunDir)
	}
}

// Queries arriving during a rebuild wait for it and use the new index; one
// whose context ends first gives up without waiting.
func TestQueriesDuringARebuildWaitOrGiveUp(t *testing.T) {
	idx := newFixture(t, filler(100), Options{})
	find(t, idx, "token000")
	for i := range 6 {
		rel := fmt.Sprintf("f%03d.go", i)
		write(t, idx, rel, fmt.Sprintf("package pkg\n// newword%03d\n", i))
		idx.Invalidate(rel)
	}
	started, release := blockOutline(t, idx)
	first := async(context.Background(), idx, "newword")
	recv(t, started, "the rebuild")
	waiter := async(context.Background(), idx, "newword")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if r := recv(t, async(ctx, idx, "newword"), "the impatient query"); r.Error != "aborted" {
		t.Errorf("impatient query = %v %q, want aborted", r.OK, r.Error)
	}
	if !stays(first) {
		t.Fatal("the rebuilding query returned while its rebuild was blocked")
	}
	release()
	for _, ch := range []<-chan core.ToolResult{first, waiter} {
		if r := recv(t, ch, "a query"); !r.OK || len(paths(r)) != 6 {
			t.Errorf("after the rebuild: %v %s, %v", r.OK, r.Detail, paths(r))
		}
	}
	if n := idx.BuildCount(); n != 2 {
		t.Errorf("BuildCount = %d, want one rebuild", n)
	}
}

// Data conforms to the declared OutputSchema with symbols, with matches on
// lines no declaration starts on, and with no match at all.
func TestCodeSearchDataConformsToItsOutputSchema(t *testing.T) {
	idx := newFixture(t, map[string]string{
		"main.go":   "package main\n\n// Greet says hello.\nfunc Greet() string { return \"hi\" }\n\nfunc main() { _ = Greet() }\n",
		"notes.txt": "greeting notes\nanother line\n",
	}, Options{})
	cs := idx.Tools()[0]
	wrapped := schema.Object(schema.Prop("data", cs.OutputSchema))
	for _, q := range []string{"Greet", "notes", "no_such_token_xyz"} {
		r := find(t, idx, q)
		if err := schema.Validate(wrapped, testutil.Object(t, []byte(testutil.MustJSON(t, map[string]any{"data": r.Data})))); err != nil {
			t.Errorf("%s: Data does not conform: %v", q, err)
		}
	}
}

// toolsWith is the tools.For list over the index's workspace with the index
// set, by name.
func toolsWith(t *testing.T, idx *Index) ([]string, map[string]core.Tool) {
	t.Helper()
	all, err := tools.For(idx.ws, tools.Options{Index: idx, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	byName := map[string]core.Tool{}
	for _, tl := range all {
		names = append(names, tl.Name)
		byName[tl.Name] = tl
	}
	return names, byName
}

func exec(t *testing.T, tl core.Tool, args map[string]any) core.ToolResult {
	t.Helper()
	r := tl.Execute(context.Background(), json.RawMessage(testutil.MustJSON(t, args)))
	if !r.OK {
		t.Fatalf("%s %v: %s: %s", tl.Name, args, r.Error, r.Detail)
	}
	return r
}

// An embedder hands the index to tools.For: code_search follows the
// built-ins, and it and search_files see only what the ignore rules let in.
func TestToolsForAddsCodeSearchAfterTheBuiltins(t *testing.T) {
	const body = "package p\n\n// needleToken\nfunc F() {}\n"
	idx := newFixture(t, map[string]string{
		".gitignore": "ignored/\n", "nested/.gitignore": "inner.go\n",
		"kept.go": body, "nested/ok.go": body, "ignored/hidden.go": body, "nested/inner.go": body,
		"pkg/runner.go": "package pkg\n\n// Runner handles retry logic.\nfunc Runner() {}\n",
	}, Options{})
	names, byName := toolsWith(t, idx)
	builtins := []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
		"file_outline", "find_symbol", "find_references", "execute", "run_command"}
	if !slices.Equal(names, append(builtins, "code_search")) {
		t.Fatalf("tools = %v", names)
	}

	var found []string
	for _, m := range exec(t, byName["search_files"], map[string]any{"pattern": "needleToken"}).Data["matches"].([]tools.SearchMatch) {
		found = append(found, m.File)
	}
	cs := exec(t, byName["code_search"], map[string]any{"query": "needleToken"})
	got := paths(cs)
	slices.Sort(found)
	slices.Sort(got)
	if want := []string{"kept.go", "nested/ok.go"}; !slices.Equal(found, want) || !slices.Equal(got, want) {
		t.Errorf("search_files found %v, code_search %v, want %v", found, got, want)
	}
	if got := paths(exec(t, byName["code_search"], map[string]any{"query": "sym:Runner", "path": "pkg"})); !slices.Equal(got, []string{"pkg/runner.go"}) {
		t.Errorf("sym:Runner under pkg found %v", got)
	}
}

func TestAnEditThroughEditFileIsFoundAtOnce(t *testing.T) {
	idx := newFixture(t, with(filler(25), map[string]string{"target.go": "package pkg\n\n// oldUniqueMarker\nfunc Target() {}\n"}), Options{})
	_, byName := toolsWith(t, idx)
	cs := byName["code_search"]
	exec(t, cs, map[string]any{"query": "oldUniqueMarker"})
	exec(t, byName["edit_file"], map[string]any{"path": "target.go",
		"edits": []map[string]string{{"old_string": "oldUniqueMarker", "new_string": "newUniqueMarker"}}})

	r := exec(t, cs, map[string]any{"query": "newUniqueMarker"})
	if !slices.Equal(paths(r), []string{"target.go"}) || r.Data["dirty_files"] != 1 {
		t.Errorf("newUniqueMarker in %v, dirty_files %v", paths(r), r.Data["dirty_files"])
	}
	if got := paths(exec(t, cs, map[string]any{"query": "oldUniqueMarker"})); len(got) != 0 {
		t.Errorf("oldUniqueMarker still found in %v", got)
	}
}

func TestAFileDeletedByAShellCommandLeavesTheResults(t *testing.T) {
	idx := newFixture(t, map[string]string{
		"keep.go":   "package pkg\n\n// keepMarker\nfunc Keep() {}\n",
		"victim.go": "package pkg\n\n// victimOnlyToken\nfunc Victim() {}\n",
	}, Options{})
	_, byName := toolsWith(t, idx)
	cs := byName["code_search"]
	exec(t, cs, map[string]any{"query": "victimOnlyToken"})
	exec(t, byName["execute"], map[string]any{"command": "rm victim.go", "timeout_s": 5})
	if !gone(filepath.Join(idx.ws.Root(), "victim.go")) {
		t.Fatal("the command did not delete the file")
	}
	if got := paths(exec(t, cs, map[string]any{"query": "victimOnlyToken"})); len(got) != 0 {
		t.Errorf("the deleted file is still found: %v", got)
	}
	if got := paths(exec(t, cs, map[string]any{"query": "keepMarker"})); !slices.Equal(got, []string{"keep.go"}) {
		t.Errorf("keepMarker found in %v", got)
	}
}
