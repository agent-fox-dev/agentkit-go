package outline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Issue #73 §4: declaration forms the heuristic backend missed in ordinary
// code. Each case is a whole file; want lists the declarations it must
// report (name → kind, container), absent the names it must not.
func TestHeuristicMainstreamForms(t *testing.T) {
	type decl struct {
		kind      Kind
		container string
	}
	cases := []struct {
		name, file, src string
		want            map[string]decl
		absent          []string
	}{
		{
			name: "C# block-scoped namespace", file: "Runner.cs",
			src: "using System;\n\nnamespace Foo.Bar\n{\n    public partial class Runner\n    {\n        public void Run() {}\n    }\n\n" +
				"    internal sealed record Result(int Code);\n    public interface IThing { }\n    public enum Mode { A }\n    public readonly struct Pt { }\n}\n",
			want: map[string]decl{
				"Foo.Bar": {KindModule, ""}, "Runner": {KindClass, ""}, "Result": {KindClass, ""},
				"IThing": {KindInterface, ""}, "Mode": {KindEnum, ""}, "Pt": {KindType, ""},
			},
			absent: []string{"Run"},
		},
		{
			name: "C# nested block namespaces and a file-scoped one", file: "Nested.cs",
			src:  "namespace A {\n  namespace B {\n    public static partial class Deep { }\n  }\n}\n",
			want: map[string]decl{"A": {KindModule, ""}, "B": {KindModule, ""}, "Deep": {KindClass, ""}},
		},
		{
			name: "C# file-scoped namespace", file: "Flat.cs",
			src:  "namespace Flat;\n\npublic abstract partial class Base { }\n",
			want: map[string]decl{"Flat": {KindModule, ""}, "Base": {KindClass, ""}},
		},
		{
			name: "Kotlin modifiers and objects", file: "a.kt",
			src: "sealed class Shape\nopen class Base\nabstract class Abs\ninternal data class D(val x: Int)\n" +
				"private fun hidden() {}\ninternal suspend fun load() {}\noverride fun toString() = \"\"\n" +
				"fun <T> generic(x: T) = x\nfun String.shout() = this\nobject Registry\ncompanion object\n" +
				"enum class Dir { N }\nsealed interface Ev\nfun interface Cb { fun f() }\ntypealias Id = Int\n",
			want: map[string]decl{
				"Shape": {KindClass, ""}, "Base": {KindClass, ""}, "Abs": {KindClass, ""}, "D": {KindClass, ""},
				"hidden": {KindFunc, ""}, "load": {KindFunc, ""}, "toString": {KindFunc, ""},
				"generic": {KindFunc, ""}, "shout": {KindFunc, ""}, "Registry": {KindClass, ""},
				"Dir": {KindEnum, ""}, "Ev": {KindInterface, ""}, "Cb": {KindInterface, ""}, "Id": {KindType, ""},
			},
		},
		{
			name: "TypeScript abstract classes, types and arrow functions", file: "a.ts",
			src: "export abstract class Repo {}\nabstract class Local {}\nexport default abstract class Def {}\n" +
				"export type Id = string;\ntype Local2 = number;\nexport const handler = async (req: Req) => {};\n" +
				"const helper = (x: number): number => x;\nexport const legacy = function () {};\nexport const LIMIT = 10;\n" +
				"export function* gen() {}\ndeclare function ext(): void;\n",
			want: map[string]decl{
				"Repo": {KindClass, ""}, "Local": {KindClass, ""}, "Def": {KindClass, ""},
				"Id": {KindType, ""}, "Local2": {KindType, ""}, "handler": {KindFunc, ""},
				"helper": {KindFunc, ""}, "legacy": {KindFunc, ""}, "gen": {KindFunc, ""}, "ext": {KindFunc, ""},
			},
			absent: []string{"LIMIT"},
		},
		{
			name: "JavaScript arrow functions and generators", file: "a.js",
			src:  "export const handler = async (req) => {};\nconst f = x => x;\nfunction* gen() {}\nexport async function* agen() {}\n",
			want: map[string]decl{"handler": {KindFunc, ""}, "f": {KindFunc, ""}, "gen": {KindFunc, ""}, "agen": {KindFunc, ""}},
		},
		{
			name: "Rust qualified fns and macro_rules", file: "lib.rs",
			src: "pub async fn serve() {}\npub const fn zero() -> u32 { 0 }\nasync fn inner() {}\nunsafe fn raw() {}\n" +
				"pub(crate) async unsafe fn both() {}\npub extern \"C\" fn ffi() {}\nmacro_rules! my_macro {\n    () => {};\n}\n" +
				"#[macro_export]\nmacro_rules! exported_macro { () => {} }\npub const MAX: u32 = 1;\n",
			want: map[string]decl{
				"serve": {KindFunc, ""}, "zero": {KindFunc, ""}, "inner": {KindFunc, ""}, "raw": {KindFunc, ""},
				"both": {KindFunc, ""}, "ffi": {KindFunc, ""}, "my_macro": {KindMacro, ""},
				"exported_macro": {KindMacro, ""}, "MAX": {KindConst, ""},
			},
		},
		{
			name: "Java modifiers", file: "A.java",
			src: "public abstract class Shape {}\npublic final class Circle {}\nabstract class Pkg {}\n" +
				"public sealed interface Ev permits A {}\nnon-sealed class Open {}\npublic record Point(int x) {}\n" +
				"final public class Odd {}\npublic @interface Ann {}\n",
			want: map[string]decl{
				"Shape": {KindClass, ""}, "Circle": {KindClass, ""}, "Pkg": {KindClass, ""},
				"Ev": {KindInterface, ""}, "Open": {KindClass, ""}, "Point": {KindClass, ""},
				"Odd": {KindClass, ""}, "Ann": {KindInterface, ""},
			},
		},
		{
			name: "Ruby singleton methods and predicates", file: "a.rb",
			src:    "def self.helper\nend\ndef valid?\nend\ndef save!\nend\ndef name=(v)\nend\n",
			want:   map[string]decl{"helper": {KindFunc, ""}, "valid?": {KindFunc, ""}, "save!": {KindFunc, ""}, "name=": {KindFunc, ""}},
			absent: []string{"self"},
		},
		{
			name: "C++ enum class and out-of-class definitions", file: "a.cpp",
			src: "enum class Color { Red };\nenum struct Mode : int { A };\nvoid Foo::bar() {}\nFoo::Foo(int x) : x_(x) {}\n" +
				"Foo::~Foo() {}\nstd::string ns::Widget::name() const { return \"\"; }\nstd::vector<int> make() { return {}; }\n" +
				"namespace outer {\n    class Inner {};\n}\n",
			want: map[string]decl{
				"Color": {KindEnum, ""}, "Mode": {KindEnum, ""}, "bar": {KindMethod, "Foo"},
				"Foo": {KindMethod, "Foo"}, "~Foo": {KindMethod, "Foo"}, "name": {KindMethod, "ns::Widget"},
				"make": {KindFunc, ""}, "outer": {KindModule, ""}, "Inner": {KindClass, ""},
			},
			absent: []string{"class", "struct"},
		},
		{
			name: "C typedefs", file: "a.c",
			src: "typedef unsigned long size_type;\ntypedef struct node node_t;\ntypedef void (*handler_fn)(int);\n" +
				"typedef struct point {\n    int x;\n} point_t;\ntypedef enum {\n    A,\n} mode_t;\n",
			want: map[string]decl{
				"size_type": {KindType, ""}, "node_t": {KindType, ""}, "handler_fn": {KindType, ""},
				"point_t": {KindType, ""}, "mode_t": {KindType, ""},
			},
		},
		{
			name: "C++ header", file: "w.h",
			src:  "namespace ui {\nclass Widget {\npublic:\n  void draw();\n};\nenum class Kind { A };\n}\n",
			want: map[string]decl{"ui": {KindModule, ""}, "Widget": {KindClass, ""}, "Kind": {KindEnum, ""}},
		},
	}

	dir := t.TempDir()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, c.file)
			if err := os.WriteFile(p, []byte(c.src), 0o644); err != nil {
				t.Fatal(err)
			}
			f, err := Outline(context.Background(), p, nil, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if f.Backend != BackendHeuristic {
				t.Fatalf("backend %v, want heuristic", f.Backend)
			}
			got := map[string]Decl{}
			for _, d := range f.Decls {
				got[d.Name] = d
			}
			for name, w := range c.want {
				d, ok := got[name]
				if !ok {
					t.Errorf("%q missing; got %v", name, declNames(f.Decls))
					continue
				}
				if d.Kind != w.kind || d.Container != w.container {
					t.Errorf("%q: Kind=%q Container=%q, want %q in %q", name, d.Kind, d.Container, w.kind, w.container)
				}
			}
			for _, name := range c.absent {
				if d, ok := got[name]; ok {
					t.Errorf("%q must not be reported: %+v", name, d)
				}
			}
		})
	}
}

// The C++ header above is labelled C++ and outlined with the C++ rules.
func TestHeuristicCppHeaderIsCpp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.h")
	if err := os.WriteFile(p, []byte("class Widget {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Outline(context.Background(), p, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.Lang != LangCPP || len(f.Decls) != 1 || f.Decls[0].Kind != KindClass {
		t.Errorf("w.h = %+v, want a C++ class", f)
	}
}
