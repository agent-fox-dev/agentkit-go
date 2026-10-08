//go:build cgo

package outline_test

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
)

// nonGoBackend is the backend a recognised non-Go file gets in this build.
const nonGoBackend = outline.BackendTreeSitter

// A cgo build outlines a Python file with tree-sitter. Without this test a
// broken build tag would fall back to the pure-Go build silently and every
// other test would stay green.
func TestCgoBuildSelectsTreeSitter(t *testing.T) {
	f, err := outline.Outline(context.Background(), "/x/a.py", []byte("def f():\n    pass\n"), outline.Options{})
	if err != nil || f.Backend != outline.BackendTreeSitter || len(f.Decls) != 1 {
		t.Fatalf("Outline(a.py) = %+v, %v; want one tree-sitter declaration", f, err)
	}
}

// show renders a declaration as "kind Container.Name L<start>-<end>", with a
// trailing "-" when it is not exported.
func show(d outline.Decl) string {
	name := d.Name
	if d.Container != "" {
		name = d.Container + "." + name
	}
	s := fmt.Sprintf("%s %s L%d-%d", d.Kind, name, d.StartLine, d.EndLine)
	if !d.Exported {
		s += " -"
	}
	return s
}

// Each grammar's query yields the closed Kind set, containers (so
// find_symbol can qualify Runner.run), the language's export rule and real
// line ranges; locals inside functions are dropped.
func TestTreeSitterDeclarations(t *testing.T) {
	cases := []struct {
		file, src string
		want      []string
	}{
		{"a.py", "X = 1\n\n@dec\ndef f():\n    def local():\n        pass\n\nclass Runner:\n    def run(self):\n        pass\n\n    def _hide(self):\n        pass\n",
			[]string{"var X L1-1", "func f L4-6", "class Runner L8-13", "method Runner.run L9-10", "method Runner._hide L12-13 -"}},
		{"a.js", "export function f() { function local() {} }\nclass W {\n  m() {}\n  #p() {}\n}\nconst g = () => 1\n",
			[]string{"func f L1-1", "class W L2-5 -", "method W.m L3-3 -", "method W.#p L4-4 -", "func g L6-6 -"}},
		{"a.ts", "export interface I { a(): void }\ntype T = string\nenum E { A }\nexport abstract class B {\n  private p(): void {}\n  abstract q(): void\n}\n",
			[]string{"interface I L1-1", "type T L2-2 -", "enum E L3-3 -", "class B L4-7", "method B.p L5-5 -", "method B.q L6-6"}},
		{"a.tsx", "export function View() { return <div/> }\n",
			[]string{"func View L1-1"}},
		{"A.java", "public class Outer {\n    @Override\n    public void run() {}\n    private int h() { return 1; }\n}\ninterface I {}\nenum E { A }\n",
			[]string{"class Outer L1-5", "method Outer.run L3-3", "method Outer.h L4-4 -", "interface I L6-6 -", "enum E L7-7 -"}},
		{"a.cs", "namespace N\n{\n    public class W\n    {\n        public void Draw() {}\n        private void H() {}\n    }\n    public struct P {}\n}\n",
			[]string{"module N L1-9 -", "class W L3-7", "method W.Draw L5-5", "method W.H L6-6 -", "type P L8-8"}},
		{"a.kt", "fun top() {}\n\nclass W {\n    private fun h() {}\n}\n\ninterface S {\n    fun area(): Double\n}\n\nenum class C {\n    A\n}\n\nobject O\n",
			[]string{"func top L1-1", "class W L3-5", "method W.h L4-4 -", "interface S L7-9", "method S.area L8-8", "enum C L11-13", "class O L15-15"}},
		{"a.scala", "class W {\n  def d(): Unit = {}\n  private def h = 1\n}\ntrait S\nobject O\n",
			[]string{"class W L1-4", "method W.d L2-2", "method W.h L3-3 -", "trait S L5-5", "class O L6-6"}},
		{"a.rs", "pub struct W;\nimpl W {\n    pub fn new() -> Self { fn local() {} W }\n    fn p(&self) {}\n}\npub trait S { fn a(&self); }\nconst M: u8 = 1;\nmacro_rules! m { () => {} }\n",
			[]string{"type W L1-1", "method W.new L3-3", "method W.p L4-4 -", "trait S L6-6", "method S.a L6-6 -", "const M L7-7 -", "macro m L8-8 -"}},
		{"a.c", "#define MAX 1\ntypedef struct p { int x; } p_t;\nenum c { R };\nstatic int h(int x) { return x; }\n",
			[]string{"macro MAX L1-1", "type p L2-2", "type p_t L2-2", "enum c L3-3", "func h L4-4"}},
		{"a.cpp", "namespace ns {\nclass W {\n  void m() {}\n};\n}\nvoid ns::W::d() {}\n",
			[]string{"module ns L1-5", "class W L2-4", "method W.m L3-3", "method ns::W.d L6-6"}},
		{"a.h", "class W { public: void m() {} };\n",
			[]string{"class W L1-1", "method W.m L1-1"}},
		{"a.php", "<?php\nfunction f() {}\nclass U {\n    public function n() {}\n    private function s() {}\n}\n",
			[]string{"func f L2-2", "class U L3-6", "method U.n L4-4", "method U.s L5-5 -"}},
		{"a.rb", "def top; end\nmodule M\n  def self.h; end\nend\nclass W\n  def _p; end\nend\n",
			[]string{"func top L1-1", "module M L2-4", "method M.h L3-3", "class W L5-7", "method W._p L6-6 -"}},
		{"a.lua", "function g() end\nlocal function l() end\nfunction M.f() end\nfunction M:m() end\n",
			[]string{"func g L1-1", "func l L2-2 -", "method M.f L3-3", "method M.m L4-4"}},
		{"a.sh", "greet() { echo; }\nfunction _p { :; }\n",
			[]string{"func greet L1-1", "func _p L2-2 -"}},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			f, err := outline.Outline(context.Background(), filepath.Join("/x", tc.file), []byte(tc.src), outline.Options{})
			if err != nil || f.Backend != outline.BackendTreeSitter {
				t.Fatalf("Outline = %+v, %v", f, err)
			}
			var got []string
			for _, d := range f.Decls {
				got = append(got, show(d))
				if want := strings.Split(tc.src, "\n")[d.StartLine-1]; d.Signature != strings.TrimSpace(want) {
					t.Errorf("%s: signature %q, want the line it starts on %q", d.Name, d.Signature, want)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("decls:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Comments and strings, including the forms a line scanner gets wrong (raw
// strings, verbatim strings, f-strings, heredocs), are spans; code is not.
func TestCommentAndStringSpans(t *testing.T) {
	cases := []struct{ file, src, inLiteral, inCode string }{
		{"a.py", "x = f\"{y} hit\"\n'''\nhit\n'''\nhit()\n", "hit\"", "hit()"},
		{"a.rs", "let s = r#\"hit\"#; /* hit */ hit();\n", "hit\"#", "hit();"},
		{"a.cs", "var s = @\"hit\\\"; hit();\n", "hit\\", "hit();"},
		{"a.rb", "x = <<~EOS\n  hit\nEOS\nhit\n", "hit\nEOS", "hit\n"},
		{"a.ts", "const s = `hit`; // hit\nhit()\n", "hit`", "hit()"},
	}
	for _, tc := range cases {
		spans, ok := outline.CommentAndStringSpans(context.Background(), "/x/"+tc.file, []byte(tc.src))
		if !ok {
			t.Fatalf("%s: no grammar", tc.file)
		}
		in := func(off int) bool {
			return slices.ContainsFunc(spans, func(s [2]int) bool { return s[0] <= off && off < s[1] })
		}
		if lit := strings.Index(tc.src, tc.inLiteral); !in(lit) {
			t.Errorf("%s: offset %d (%q) not in a literal span %v", tc.file, lit, tc.inLiteral, spans)
		}
		if code := strings.LastIndex(tc.src, tc.inCode); in(code) {
			t.Errorf("%s: code at %d (%q) is in a literal span %v", tc.file, code, tc.inCode, spans)
		}
	}
	if _, ok := outline.CommentAndStringSpans(context.Background(), "/x/a.go", nil); ok {
		t.Error("Go has no grammar: its references are resolved by type")
	}
}
