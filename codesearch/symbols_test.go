//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// symbolNames is the Container.Name of each match, sorted.
func symbolNames(a tools.SymbolAnswer) []string {
	var out []string
	for _, m := range a.Matches {
		if m.Container != "" {
			out = append(out, m.Container+"."+m.Name)
		} else {
			out = append(out, m.Path+":"+m.Name)
		}
	}
	slices.Sort(out)
	return out
}

// Symbols answers only from a complete index an earlier code_search built,
// never builds one, never waits for one, and answers ok=false with no error
// when it cannot.
func TestSymbolsDeclinesWhenItCannotAnswerInFull(t *testing.T) {
	var big strings.Builder
	big.WriteString("package pkg\n\n")
	for i := range maxSymbolMatches + 10 {
		fmt.Fprintf(&big, "func Sym%04d() {}\n", i)
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		o     Options
		setup func(t *testing.T, idx *Index)
	}{
		{"unbuilt", filler(3), Options{}, func(*testing.T, *Index) {}},
		{"during the first build", filler(3), Options{}, func(t *testing.T, idx *Index) {
			started, _ := blockOutline(t, idx)
			async(context.Background(), idx, "token000")
			recv(t, started, "the build")
		}},
		{"partial", filler(20), Options{MaxFiles: 5}, func(t *testing.T, idx *Index) { find(t, idx, "token000") }},
		{"closed", filler(3), Options{}, func(t *testing.T, idx *Index) {
			find(t, idx, "token000")
			_ = idx.Close()
		}},
		{"over a thousand matches", map[string]string{"big.go": big.String()}, Options{}, func(t *testing.T, idx *Index) {
			find(t, idx, "Sym0001")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			idx := newFixture(t, c.files, c.o)
			c.setup(t, idx)
			builds := idx.BuildCount()
			if _, ok, err := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "Sym"}); ok || err != nil {
				t.Errorf("Symbols = ok:%v err:%v, want false and nil", ok, err)
			}
			if n := idx.BuildCount(); n != builds {
				t.Errorf("BuildCount %d -> %d: Symbols built", builds, n)
			}
		})
	}
}

// The matching rules are find_symbol's: smart-case prefix, exact, qualified
// Container.Name, kind and path; FilesIndexed and Backends count the files of
// the queried scope.
func TestSymbolsMatchesAsFindSymbolDoes(t *testing.T) {
	idx := newFixture(t, map[string]string{
		"a/runner.go": "package a\n\nfunc Runner() {}\nfunc runner() {}\n",
		"a/server.go": "package a\n\ntype Server struct{}\nfunc (s Server) Run() {}\n",
		"b/helper.go": "package b\n\nfunc RunHelper() {}\n",
		"b/four.py":   "def four():\n    pass\n",
	}, Options{})
	find(t, idx, "Runner")
	py := 1
	if !treeSitter() {
		py = 0
	}
	for _, c := range []struct {
		q       tools.SymbolQuery
		want    []string
		indexed int
	}{
		{tools.SymbolQuery{Name: "run"}, []string{"Server.Run", "a/runner.go:Runner", "a/runner.go:runner", "b/helper.go:RunHelper"}, 4},
		{tools.SymbolQuery{Name: "Run"}, []string{"Server.Run", "a/runner.go:Runner", "b/helper.go:RunHelper"}, 4},
		{tools.SymbolQuery{Name: "Run", Exact: true}, []string{"Server.Run"}, 4},
		{tools.SymbolQuery{Name: "Server.Run"}, []string{"Server.Run"}, 4},
		{tools.SymbolQuery{Name: "server.r"}, []string{"Server.Run"}, 4},
		{tools.SymbolQuery{Name: "Server.Ru", Exact: true}, nil, 4},
		{tools.SymbolQuery{Name: "run", Kind: "method"}, []string{"Server.Run"}, 4},
		{tools.SymbolQuery{Name: "run", Path: "a"}, []string{"Server.Run", "a/runner.go:Runner", "a/runner.go:runner"}, 2},
		{tools.SymbolQuery{Name: "run", Path: "b/"}, []string{"b/helper.go:RunHelper"}, 1 + py},
	} {
		a, ok, err := idx.Symbols(context.Background(), c.q)
		if !ok || err != nil {
			t.Fatalf("%+v: ok:%v err:%v", c.q, ok, err)
		}
		if got := symbolNames(a); !slices.Equal(got, c.want) || a.FilesIndexed != c.indexed {
			t.Errorf("%+v: %v in %d files, want %v in %d", c.q, got, a.FilesIndexed, c.want, c.indexed)
		}
	}
	if a, _, _ := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "x", Path: "a"}); len(a.Backends) != 1 || a.Backends["go/ast"] != 2 {
		t.Errorf("Backends for a/ = %v, want go/ast: 2", a.Backends)
	}
}

// Symbols runs a pending revalidation and outlines dirty files again before
// answering, so it sees an edit and a rename.
func TestSymbolsSeesEditsAndRenames(t *testing.T) {
	idx := newFixture(t, with(filler(30), map[string]string{
		"a.go": "package pkg\n\nfunc Original() {}\n",
		"x.go": "package pkg\n\nfunc InX() {}\n",
	}), Options{})
	find(t, idx, "Original")

	write(t, idx, "a.go", "package pkg\n\nfunc Fresh() {}\n")
	idx.Invalidate("a.go")
	if a, ok, _ := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "Fresh"}); !ok || !slices.Equal(symbolNames(a), []string{"a.go:Fresh"}) {
		t.Errorf("after the edit: ok:%v %v", ok, symbolNames(a))
	}
	if a, _, _ := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "Original"}); len(a.Matches) != 0 {
		t.Errorf("the edited-away declaration is still found: %v", symbolNames(a))
	}

	if err := os.Rename(filepath.Join(idx.ws.Root(), "x.go"), filepath.Join(idx.ws.Root(), "y.go")); err != nil {
		t.Fatal(err)
	}
	idx.Invalidate("")
	if a, ok, _ := idx.Symbols(context.Background(), tools.SymbolQuery{Name: "InX"}); !ok || !slices.Equal(symbolNames(a), []string{"y.go:InX"}) {
		t.Errorf("after the rename: ok:%v %v", ok, symbolNames(a))
	}
	if n := idx.RevalCount(); n != 1 {
		t.Errorf("RevalCount = %d, want 1", n)
	}
}

// A context that has ended is the one case Symbols returns an error for,
// whether it ended before the call or during the revalidation walk, which is
// then not counted.
func TestSymbolsReturnsTheContextError(t *testing.T) {
	idx := newFixture(t, filler(20), Options{})
	find(t, idx, "token000")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok, err := idx.Symbols(cancelled, tools.SymbolQuery{Name: "F"}); ok || err == nil {
		t.Errorf("a cancelled context: ok:%v err:%v, want its error", ok, err)
	}

	idx.Invalidate("")
	ctx, cancel := context.WithCancel(context.Background())
	idx.testRevalHook = cancel
	_, ok, err := idx.Symbols(ctx, tools.SymbolQuery{Name: "F"})
	idx.testRevalHook = nil
	if ok || err == nil || idx.RevalCount() != 0 {
		t.Errorf("ended during the walk: ok:%v err:%v RevalCount %d, want the error and no walk counted", ok, err, idx.RevalCount())
	}
}

// find_symbol asks the index first: before a code_search it answers from its
// own table, after it from the index, with the same matches in the same order.
func TestFindSymbolGivesTheSameMatchesThroughTheIndex(t *testing.T) {
	idx := newFixture(t, map[string]string{
		"main.go":        "package main\n\nfunc Main() {}\nfunc helper() {}\n",
		"lib.go":         "package main\n\nfunc Lib() {}\nfunc lib() {}\n",
		"pkg/server.go":  "package pkg\n\ntype Server struct{}\nfunc (s Server) Run() {}\nfunc NewServer() *Server { return nil }\n",
		"pkg/util.go":    "package pkg\n\nfunc Util() {}\nvar UtilVar int\n",
		"test/t_test.go": "package test\n\nfunc TestSomething() {}\n",
	}, Options{})
	findSymbol := func(opts tools.Options) core.Tool {
		all, err := tools.For(idx.ws, opts)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range all {
			if tl.Name == "find_symbol" {
				return tl
			}
		}
		t.Fatal("no find_symbol")
		return core.Tool{}
	}
	withIndex, table := findSymbol(tools.Options{Index: idx}), findSymbol(tools.Options{})
	exec := func(tl core.Tool, args map[string]any) core.ToolResult {
		in, _ := json.Marshal(args)
		r := tl.Execute(context.Background(), in)
		if !r.OK {
			t.Fatalf("find_symbol %v: %s: %s", args, r.Error, r.Detail)
		}
		return r
	}

	if r := exec(withIndex, map[string]any{"name": "Main"}); strings.Contains(r.Text, "codesearch") {
		t.Errorf("before any code_search find_symbol was answered by the index:\n%s", r.Text)
	}
	find(t, idx, "func")
	for _, q := range []map[string]any{
		{"name": "Main"}, {"name": "lib"}, {"name": "Lib", "exact": true}, {"name": "Server.Run"},
		{"name": "run"}, {"name": "Util", "kind": "func"}, {"name": "New", "path": "pkg"},
	} {
		got, want := exec(withIndex, q), exec(table, q)
		if !strings.Contains(got.Text, "codesearch") {
			t.Errorf("%v was not answered by the index:\n%s", q, got.Text)
		}
		if g, w := got.Data["symbols"].([]tools.SymbolMatch), want.Data["symbols"].([]tools.SymbolMatch); !slices.Equal(g, w) {
			t.Errorf("%v: through the index %+v, from the table %+v", q, g, w)
		}
	}
}
