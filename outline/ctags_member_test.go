package outline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tagLine is one line of universal-ctags JSON output, as `ctags
// --output-format=json --fields=+neKS` prints it. The values in the tests
// below are copied from real Universal Ctags 6.2.1 output.
func tagLine(path, name string, line int, kind, scope, scopeKind string) string {
	s := fmt.Sprintf(`{"_type":"tag","name":%q,"path":%q,"line":%d,"kind":%q`, name, path, line, kind)
	if scope != "" {
		s += fmt.Sprintf(`,"scope":%q,"scopeKind":%q`, scope, scopeKind)
	}
	return s + "}"
}

// outlineWithTags outlines a file named name (its extension picks the language)
// whose ctags output is the given tags.
func outlineWithTags(t *testing.T, name string, tags func(path string) []string) map[string]Decl {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("x\n", 40)), 0o644); err != nil {
		t.Fatal(err)
	}
	output := strings.Join(tags(path), "\n") + "\n"
	runner := func(_ context.Context, _ []string) ([]byte, error) { return []byte(output), nil }

	fs, _, err := OutlineMany(context.Background(), []Source{{Abs: path}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}
	if len(fs) != 1 || fs[0].Backend != BackendCtags {
		t.Fatalf("got %d files, backend %v; want 1 file from the ctags backend", len(fs), fs[0].Backend)
	}
	return declMap(fs[0].Decls)
}

// 01-REQ-5.2 and PRD §4: a consumer wanting indented declarations needs ctags.
// Universal ctags reports a Python method as kind "member" scoped in its
// class, so a Python member in a type-like scope is a method with Container
// set to the scope.
func TestCtagsPythonClassMembersAreMethods(t *testing.T) {
	dm := outlineWithTags(t, "svc.py", func(p string) []string {
		return []string{
			tagLine(p, "Outer", 3, "class", "", ""),
			tagLine(p, "attr", 4, "variable", "Outer", "class"),
			tagLine(p, "Inner", 6, "class", "Outer", "class"),
			tagLine(p, "inner_method", 7, "member", "Outer.Inner", "class"),
			tagLine(p, "static_one", 11, "member", "Outer", "class"),
			tagLine(p, "amethod", 18, "member", "Outer", "class"),
			tagLine(p, "__init__", 23, "member", "Outer", "class"),
			tagLine(p, "top", 26, "function", "", ""),
		}
	})

	for name, container := range map[string]string{
		"inner_method": "Outer.Inner", "static_one": "Outer", "amethod": "Outer", "__init__": "Outer",
	} {
		d, ok := dm[name]
		if !ok {
			t.Errorf("method %q was dropped", name)
			continue
		}
		if d.Kind != KindMethod || d.Container != container {
			t.Errorf("%s: Kind=%q Container=%q, want method in %q", name, d.Kind, d.Container, container)
		}
	}
	if d := dm["__init__"]; d.Exported {
		t.Error("__init__ should not be exported: its name begins with an underscore")
	}
	if d, ok := dm["top"]; !ok || d.Kind != KindFunc || d.Container != "" {
		t.Errorf("top = %+v, want an unchanged top-level func", d)
	}
}

// 01-REQ-1.2: struct members and fields are dropped. A C or C++ data member is
// the same (member, type-like scope) pair as a Python method; only the language
// tells them apart, so the rule is per language, not by scope kind.
func TestCtagsDataMembersOfOtherLanguagesStayDropped(t *testing.T) {
	cpp := outlineWithTags(t, "thing.cpp", func(p string) []string {
		return []string{
			tagLine(p, "Widget", 1, "class", "", ""),
			tagLine(p, "size", 3, "member", "Widget", "class"),
			tagLine(p, "count", 5, "member", "Widget", "class"),
			tagLine(p, "draw", 7, "function", "Widget", "class"),
			tagLine(p, "Pair", 8, "struct", "", ""),
			tagLine(p, "a", 8, "member", "Pair", "struct"),
			tagLine(p, "U", 9, "union", "", ""),
			tagLine(p, "i", 9, "member", "U", "union"),
		}
	})
	for _, name := range []string{"size", "count", "a", "i"} {
		if _, ok := cpp[name]; ok {
			t.Errorf("C++ data member %q was reported; 01-REQ-1.2 drops struct members", name)
		}
	}
	if d, ok := cpp["draw"]; !ok || d.Kind != KindMethod || d.Container != "Widget" {
		t.Errorf("draw = %+v, want the C++ method it already was", d)
	}

	c := outlineWithTags(t, "thing.c", func(p string) []string {
		return []string{
			tagLine(p, "Point", 1, "struct", "", ""),
			tagLine(p, "x", 1, "member", "Point", "struct"),
			tagLine(p, "y", 1, "member", "Point", "struct"),
		}
	})
	if _, ok := c["x"]; ok {
		t.Error("C struct member x was reported")
	}
}

// A function nested in a Python method has scope kind "member". It is a local
// and is dropped; before, it was reported as a top-level function.
func TestCtagsFunctionNestedInAPythonMethodIsALocal(t *testing.T) {
	dm := outlineWithTags(t, "svc.py", func(p string) []string {
		return []string{
			tagLine(p, "Outer", 3, "class", "", ""),
			tagLine(p, "amethod", 18, "member", "Outer", "class"),
			tagLine(p, "nested_in_method", 19, "function", "Outer.amethod", "member"),
			tagLine(p, "top", 26, "function", "", ""),
			tagLine(p, "nested_in_func", 27, "function", "top", "function"),
		}
	})
	if _, ok := dm["nested_in_method"]; ok {
		t.Error("a function nested in a method was reported")
	}
	if _, ok := dm["nested_in_func"]; ok {
		t.Error("a function nested in a function was reported")
	}
	if _, ok := dm["amethod"]; !ok {
		t.Error("the enclosing method was dropped")
	}
}

// Dropping a type nested in a function drops what is declared in it: its
// methods are locals too, not orphans of a class the table never shows.
func TestCtagsMethodsOfALocalClassAreDropped(t *testing.T) {
	t.Run("python", func(t *testing.T) {
		dm := outlineWithTags(t, "svc.py", func(p string) []string {
			return []string{
				tagLine(p, "top", 26, "function", "", ""),
				tagLine(p, "LocalClass", 29, "class", "top", "function"),
				tagLine(p, "local_class_method", 30, "member", "top.LocalClass", "class"),
				tagLine(p, "Outer", 40, "class", "", ""),
				tagLine(p, "kept", 41, "member", "Outer", "class"),
				tagLine(p, "m", 42, "member", "Outer", "class"),
				tagLine(p, "InMethod", 43, "class", "Outer.m", "member"),
				tagLine(p, "deep", 44, "member", "Outer.m.InMethod", "class"),
			}
		})
		for _, name := range []string{"LocalClass", "local_class_method", "InMethod", "deep"} {
			if _, ok := dm[name]; ok {
				t.Errorf("%q is declared inside a function and was reported", name)
			}
		}
		if _, ok := dm["kept"]; !ok {
			t.Error("a method of a top-level class was dropped")
		}
	})

	// Seen with real ctags on JavaScript: Inner is dropped as a local, and its
	// method, kind "method" scoped in outer.Inner, used to be reported.
	t.Run("javascript", func(t *testing.T) {
		dm := outlineWithTags(t, "local.js", func(p string) []string {
			return []string{
				tagLine(p, "outer", 1, "function", "", ""),
				tagLine(p, "Inner", 2, "class", "outer", "function"),
				tagLine(p, "method", 3, "method", "outer.Inner", "class"),
				tagLine(p, "Top", 8, "class", "", ""),
				tagLine(p, "m", 8, "method", "Top", "class"),
			}
		})
		if _, ok := dm["Inner"]; ok {
			t.Error("the local class Inner was reported")
		}
		if d, ok := dm["method"]; ok {
			t.Errorf("method %+v of a local class was reported as an orphan", d)
		}
		if _, ok := dm["m"]; !ok {
			t.Error("a method of a top-level class was dropped")
		}
	})

	// Locals are per file: a local class in one file does not hide a class
	// of the same name in another.
	t.Run("per file", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.py")
		b := filepath.Join(dir, "b.py")
		for _, p := range []string{a, b} {
			if err := os.WriteFile(p, []byte(strings.Repeat("x\n", 20)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		out := strings.Join([]string{
			tagLine(a, "f", 1, "function", "", ""),
			tagLine(a, "C", 2, "class", "f", "function"),
			tagLine(b, "f", 1, "function", "", ""),
			tagLine(b, "m", 5, "member", "f.C", "class"),
		}, "\n") + "\n"
		runner := func(_ context.Context, _ []string) ([]byte, error) { return []byte(out), nil }
		fs, _, err := OutlineMany(context.Background(), []Source{{Abs: a}, {Abs: b}}, Options{Runner: runner})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := declMap(fs[1].Decls)["m"]; !ok {
			t.Error("b.py's m was dropped because a.py has a local class named C")
		}
	})
}
