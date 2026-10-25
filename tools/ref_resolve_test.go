package tools

import (
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

func TestTheReferenceTargetIsResolvedFromTheSymbolTable(t *testing.T) {
	st := newSymbolTable()
	add := func(path string, decls ...outline.Decl) {
		st.entries[path] = &symbolEntry{file: outline.File{Path: path, Decls: decls}}
	}
	add("runner.go",
		outline.Decl{Kind: outline.KindType, Name: "Runner", Exported: true, StartLine: 3},
		outline.Decl{Kind: outline.KindMethod, Name: "Run", Container: "Runner", Exported: true, StartLine: 7},
		outline.Decl{Kind: outline.KindFunc, Name: "Run", Exported: true, StartLine: 12},
		outline.Decl{Kind: outline.KindMethod, Name: "Execute", Container: "a.b.Manager", StartLine: 20})
	add("pkg/config.go", outline.Decl{Kind: outline.KindType, Name: "Config", Exported: true, StartLine: 5})
	add("pkg/setup.go", outline.Decl{Kind: outline.KindFunc, Name: "Config", Exported: true, StartLine: 8})
	add("cmd/main.go", outline.Decl{Kind: outline.KindType, Name: "Config", Exported: true, StartLine: 15})
	st.entries["nil.go"] = nil
	for _, c := range []struct{ name, kind, path, want string }{
		{"Run", "", "", "runner.go:7 Runner.Run, runner.go:12 Run"},
		{"Runner.Run", "", "", "runner.go:7 Runner.Run"},
		{"a.b.Manager.Execute", "", "", "runner.go:20 a.b.Manager.Execute"}, // the container is all before the last dot
		{"Nobody.Run", "", "", ""},
		{"Config", "type", "pkg", "pkg/config.go:5 Config"},
		{"Config", "", "./pkg/", "pkg/config.go:5 Config, pkg/setup.go:8 Config"},
		// Exact matches win; only without one is a case variant or prefix taken.
		{"run", "", "", "runner.go:7 Runner.Run, runner.go:12 Run"},
		{"Ru", "", "", "runner.go:3 Runner, runner.go:7 Runner.Run, runner.go:12 Run"},
		{"ru", "", "", "runner.go:3 Runner, runner.go:7 Runner.Run, runner.go:12 Run"},
		{"Runner.", "", "", ""},
		{"", "", "", ""},
	} {
		var got []string
		for _, m := range resolveSymbolCandidates(st, c.name, c.kind, c.path) {
			n := m.Name
			if m.Container != "" {
				n = m.Container + "." + n
			}
			got = append(got, m.Path+":"+itoa(m.StartLine)+" "+n)
		}
		slices.Sort(got)
		want := []string{}
		if c.want != "" {
			want = strings.Split(c.want, ", ")
		}
		slices.Sort(want)
		if !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%q kind %q path %q: %v, want %v", c.name, c.kind, c.path, got, want)
		}
	}
	if got := resolveSymbolCandidates(nil, "Run", "", ""); len(got) != 0 {
		t.Error("a nil table")
	}
	// The top candidate is find_symbol's top rank.
	top := disambiguateSymbol([]SymbolMatch{
		{Path: "test/handle_test.go", Name: "handle", StartLine: 10}, {Path: "a.go", Name: "handle", StartLine: 10},
		{Path: "test/handle_test.go", Name: "Handle", Exported: true, StartLine: 5}, {Path: "pkg/handle.go", Name: "Handle", Exported: true, StartLine: 15, Kind: "func", Signature: "func Handle()", EndLine: 17},
	}, "Handle")
	if d := top.Decl(); top.Path != "pkg/handle.go" || d != (outline.Decl{Kind: outline.KindFunc, Name: "Handle", Signature: "func Handle()", Exported: true, StartLine: 15, EndLine: 17}) {
		t.Errorf("top = %+v", top)
	}
	if (disambiguateSymbol(nil, "x") != SymbolMatch{}) {
		t.Error("no candidates")
	}
}
