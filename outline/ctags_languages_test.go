package outline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Issue #73 §1: universal ctags gives a method kind "method" (not "function")
// in Java, C#, JS/TS, Ruby, Kotlin and Rust, and Rust scopes it in an
// "implementation". Each must come out as a method whose Container is its
// scope, or a qualified find_symbol can never match it. The tag values are
// copied from Universal Ctags 6.2.1 output.
func TestCtagsMethodTagsKeepTheirContainer(t *testing.T) {
	cases := []struct {
		file                    string
		name, scope, scopeKind  string
		wantKind                Kind
		wantContainer           string
		extraTags, absentInTags []string
	}{
		{file: "A.java", name: "area", scope: "Shape", scopeKind: "class", wantKind: KindMethod, wantContainer: "Shape"},
		{file: "A.cs", name: "Run", scope: "Foo.Bar.Runner", scopeKind: "class", wantKind: KindMethod, wantContainer: "Foo.Bar.Runner"},
		{file: "A.cs", name: "Y", scope: "Foo.Bar.IX", scopeKind: "interface", wantKind: KindMethod, wantContainer: "Foo.Bar.IX"},
		{file: "a.js", name: "run", scope: "Runner", scopeKind: "class", wantKind: KindMethod, wantContainer: "Runner"},
		{file: "a.ts", name: "run", scope: "Runner", scopeKind: "class", wantKind: KindMethod, wantContainer: "Runner"},
		{file: "a.rb", name: "run", scope: "M.Runner", scopeKind: "class", wantKind: KindMethod, wantContainer: "M.Runner"},
		{file: "a.kt", name: "m", scope: "S", scopeKind: "class", wantKind: KindMethod, wantContainer: "S"},
		{file: "a.kt", name: "x", scope: "O", scopeKind: "object", wantKind: KindMethod, wantContainer: "O"},
		{file: "a.rs", name: "run", scope: "S", scopeKind: "implementation", wantKind: KindMethod, wantContainer: "S"},
		{file: "a.rs", name: "t", scope: "Tr", scopeKind: "interface", wantKind: KindMethod, wantContainer: "Tr"},
		{file: "a.mm", name: "m", scope: "K", scopeKind: "implementation", wantKind: KindMethod, wantContainer: "K"},
		// Ruby: `def self.helper` is a singletonMethod.
		{file: "a.rb", name: "helper", scope: "M.Runner", scopeKind: "class", wantKind: KindMethod, wantContainer: "M.Runner"},
		// Protobuf: an rpc is a method of its service.
		{file: "a.proto", name: "R", scope: "S", scopeKind: "service", wantKind: KindMethod, wantContainer: "S"},
		// Kotlin reports a top-level `fun` as kind "method" with no scope: it
		// is a function, not a method of nothing.
		{file: "a.kt", name: "g", wantKind: KindFunc},
		// GDScript does the same.
		{file: "a.gd", name: "f", wantKind: KindFunc},
	}
	kindOf := map[string]string{"helper": "singletonMethod", "R": "rpc"}
	for _, c := range cases {
		kind := "method"
		if k, ok := kindOf[c.name]; ok {
			kind = k
		}
		dm := outlineWithTags(t, c.file, func(p string) []string {
			return []string{tagLine(p, c.name, 3, kind, c.scope, c.scopeKind)}
		})
		d, ok := dm[c.name]
		if !ok {
			t.Errorf("%s %s: dropped", c.file, c.name)
			continue
		}
		if d.Kind != c.wantKind || d.Container != c.wantContainer {
			t.Errorf("%s %s: Kind=%q Container=%q, want %q in %q",
				c.file, c.name, d.Kind, d.Container, c.wantKind, c.wantContainer)
		}
	}
}

// Issue #73 §5: kinds universal ctags reports for declarations the closed set
// has a place for, and which used to be dropped.
func TestCtagsKindsThatUsedToBeDropped(t *testing.T) {
	cases := []struct {
		file, name, kind string
		want             Kind
	}{
		{"a.rs", "Tr", "interface", KindTrait}, // a Rust trait arrives as "interface"
		{"a.ts", "Alias", "alias", KindType},
		{"a.elm", "Al", "alias", KindType},
		{"a.js", "gen", "generator", KindFunc},
		{"a.ts", "gen", "generator", KindFunc},
		{"a.kt", "O", "object", KindClass},
		{"a.kt", "T", "typealias", KindType},
		{"a.proto", "M", "message", KindType},
		{"a.proto", "S", "service", KindInterface},
		{"a.thrift", "Sv", "service", KindInterface},
		{"a.ex", "P", "protocol", KindInterface},
		{"a.sql", "t", "table", KindType},
		{"a.sql", "v", "view", KindType},
		{"a.erl", "r", "record", KindType},
		{"a.ads", "P", "packspec", KindModule},
		{"a.mm", "K", "interface", KindClass}, // Objective-C @interface declares a class
		{"a.tf", "b", "resource", KindVar},
		{"a.tf", "y", "data", KindVar},
		{"a.tf", "o", "output", KindVar},
	}
	for _, c := range cases {
		dm := outlineWithTags(t, c.file, func(p string) []string {
			return []string{tagLine(p, c.name, 2, c.kind, "", "")}
		})
		if d, ok := dm[c.name]; !ok || d.Kind != c.want {
			t.Errorf("%s %s (ctags kind %q): got %+v (present=%v), want kind %q", c.file, c.name, c.kind, d, ok, c.want)
		}
	}

	// The same words mean something else, or nothing worth outlining, in other
	// parsers: a Rust "implementation" duplicates its type, a Protobuf field
	// is a field.
	dm := outlineWithTags(t, "a.rs", func(p string) []string {
		return []string{tagLine(p, "S", 2, "implementation", "", "")}
	})
	if d, ok := dm["S"]; ok {
		t.Errorf("a Rust impl block was reported as %+v", d)
	}
	dm = outlineWithTags(t, "a.proto", func(p string) []string {
		return []string{tagLine(p, "a", 2, "field", "M", "message")}
	})
	if d, ok := dm["a"]; ok {
		t.Errorf("a Protobuf field was reported as %+v", d)
	}
}

// Issue #73 §2 and spec 01 §3 ("output not usable → heuristic"): a file
// ctags emits nothing for gets the heuristic's declarations, not an empty
// ctags outline that is worse than having no ctags at all.
func TestEmptyCtagsOutputFallsBackToTheHeuristic(t *testing.T) {
	dir := t.TempDir()
	tsx := filepath.Join(dir, "view.tsx") // universal ctags has no .tsx mapping
	if err := os.WriteFile(tsx, []byte("export function View() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty.py") // nothing to find either way
	if err := os.WriteFile(empty, []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lua := filepath.Join(dir, "a.lua") // no heuristic: stays ctags
	if err := os.WriteFile(lua, []byte("local x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	silent := func(context.Context, []string) ([]byte, error) { return nil, nil }

	fs, _, err := OutlineMany(context.Background(), []Source{{Abs: tsx}, {Abs: empty}, {Abs: lua}}, Options{Runner: silent})
	if err != nil {
		t.Fatal(err)
	}
	if fs[0].Backend != BackendHeuristic || len(fs[0].Decls) != 1 || fs[0].Decls[0].Name != "View" {
		t.Errorf("OutlineMany view.tsx = %+v, want the heuristic's View", fs[0])
	}
	if fs[1].Backend != BackendCtags || len(fs[1].Decls) != 0 {
		t.Errorf("OutlineMany empty.py = %+v, want ctags with nothing: the heuristic found nothing either", fs[1])
	}
	if fs[2].Backend != BackendCtags {
		t.Errorf("OutlineMany a.lua backend = %v, want ctags: there is no heuristic to fall back to", fs[2].Backend)
	}

	f, err := Outline(context.Background(), tsx, nil, Options{Runner: silent})
	if err != nil {
		t.Fatal(err)
	}
	if f.Backend != BackendHeuristic || len(f.Decls) != 1 {
		t.Errorf("Outline view.tsx = %+v, want the heuristic's View", f)
	}
}
