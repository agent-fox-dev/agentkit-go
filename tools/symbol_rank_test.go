package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

func TestRankSymbolsOrdersByTheSixTiers(t *testing.T) {
	m := func(path, name, container string, exported bool, line int) SymbolMatch {
		return SymbolMatch{Path: path, Name: name, Container: container, Exported: exported, StartLine: line}
	}
	for _, c := range []struct {
		name, query string
		matches     []SymbolMatch // in the expected order
	}{
		{"exact before prefix", "Run", []SymbolMatch{m("b.go", "Run", "", true, 9), m("a.go", "RunAll", "", true, 1)}},
		{"a smart-case equal counts as exact", "run", []SymbolMatch{m("b.go", "Run", "", true, 9), m("a.go", "RunAll", "", true, 1)}},
		{"exact beats exported", "run", []SymbolMatch{m("b.go", "run", "", false, 9), m("a.go", "RunAll", "", true, 1)}},
		{"exported before unexported", "run", []SymbolMatch{m("b.go", "Run", "", true, 9), m("a.go", "run", "", false, 1)}},
		{"non-test before test", "Foo", []SymbolMatch{m("zz/b.go", "Foo", "", true, 9), m("a_test.go", "Foo", "", true, 1)}},
		{"shorter path first", "Foo", []SymbolMatch{m("b.go", "Foo", "", true, 9), m("a/a.go", "Foo", "", true, 1)}},
		{"then the path", "Foo", []SymbolMatch{m("a.go", "Foo", "", true, 9), m("b.go", "Foo", "", true, 1)}},
		{"then the line", "Foo", []SymbolMatch{m("a.go", "Foo", "", true, 1), m("a.go", "Foo", "", true, 9)}},
		{"a qualified query compares the qualified name", "Runner.Run",
			[]SymbolMatch{m("b.go", "Run", "Runner", true, 9), m("a.go", "RunAll", "Runner", true, 1), m("a.go", "Run", "", true, 2)}},
	} {
		got := slices.Clone(c.matches)
		slices.Reverse(got)
		rankSymbols(got, c.query, strings.Contains(c.query, "."), hasUppercase(c.query))
		if !slices.Equal(got, c.matches) {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if got := sanitizePath("bad\x01name\x7f.go"); got != "bad?name?.go" {
		t.Errorf("sanitizePath = %q", got)
	}
}

func TestMatchSymbolsAppliesNameKindAndPath(t *testing.T) {
	st := newSymbolTable()
	decl := func(kind outline.Kind, name, container string) outline.Decl {
		return outline.Decl{Kind: kind, Name: name, Container: container}
	}
	st.entries["a.go"] = &symbolEntry{file: outline.File{Backend: outline.BackendGoAST, Decls: []outline.Decl{
		decl(outline.KindFunc, "Run", ""), decl(outline.KindFunc, "RunAll", ""), decl(outline.KindFunc, "Runtime", ""),
		decl(outline.KindFunc, "runner", ""), decl(outline.KindFunc, "rUN", ""), decl(outline.KindFunc, "Other", ""),
		decl(outline.KindMethod, "Run", "Runner"), decl(outline.KindMethod, "Stop", "Runner"), decl(outline.KindMethod, "Run", "Other"),
	}}}
	st.entries["pkg/b.go"] = &symbolEntry{file: outline.File{Decls: []outline.Decl{decl(outline.KindType, "Runner", "")}}}
	for _, c := range []struct {
		name, kind, scope string
		exact             bool
		want              []string
	}{
		// A lowercase name is a case-insensitive prefix; an uppercase one is exact case.
		{"run", "", "", false, []string{"Run", "RunAll", "Runtime", "runner", "rUN", "Runner.Run", "Other.Run", "Runner"}},
		{"Run", "", "", false, []string{"Run", "RunAll", "Runtime", "Runner.Run", "Other.Run", "Runner"}},
		// exact is always case-sensitive.
		{"run", "", "", true, []string{}},
		{"Run", "", "", true, []string{"Run", "Runner.Run", "Other.Run"}},
		// A dotted name matches Container.Name, never a declaration without one.
		{"Runner.Run", "", "", true, []string{"Runner.Run"}},
		{"runner.", "", "", false, []string{"Runner.Run", "Runner.Stop"}},
		{"Run", "method", "", false, []string{"Runner.Run", "Other.Run"}},
		{"Run", "", "pkg/", false, []string{"Runner"}},
	} {
		var got []string
		for _, m := range matchSymbols(st, c.name, c.kind, c.scope, c.exact) {
			if m.Container != "" {
				m.Name = m.Container + "." + m.Name
			}
			got = append(got, m.Name)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(c.want))
		if !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%q kind %q scope %q exact %v: %v, want %v", c.name, c.kind, c.scope, c.exact, got, want)
		}
	}
}
