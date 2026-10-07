package outline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TS-01-1 (property): Every File has a named backend and a non-nil Decls sorted
// by StartLine then Name.
func TestEveryFileBackendAndSorted_TS_01_1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Fake Runner that returns tags in random (reversed) order.
	fakeRunner := func(_ context.Context, args []string) ([]byte, error) {
		// Build tags for each file in the args (after the 6 fixed args).
		var out string
		for _, p := range args[6:] {
			out += fmt.Sprintf(`{"_type":"tag","name":"Zeta","path":%q,"line":5,"kind":"function"}`+"\n", p)
			out += fmt.Sprintf(`{"_type":"tag","name":"Alpha","path":%q,"line":5,"kind":"function"}`+"\n", p)
			out += fmt.Sprintf(`{"_type":"tag","name":"Beta","path":%q,"line":1,"kind":"function"}`+"\n", p)
		}
		return []byte(out), nil
	}

	validBackends := map[Backend]bool{
		BackendGoAST:     true,
		BackendCtags:     true,
		BackendHeuristic: true,
		BackendNone:      true,
	}

	// Create test files for each category.
	type testCase struct {
		name string
		src  string
	}
	cases := []testCase{
		// Go
		{"sample.go", "package sample\n\nfunc Hello() {}\n"},
		// Heuristic languages
		{"sample.py", "def hello():\n    pass\n"},
		{"sample.js", "function hello() {}\n"},
		{"sample.ts", "function hello(): void {}\n"},
		{"sample.rs", "fn hello() {}\n"},
		{"sample.java", "public class Hello {}\n"},
		{"sample.kt", "fun hello() {}\n"},
		{"sample.cs", "public class Hello {}\n"},
		{"sample.rb", "def hello\n  nil\nend\n"},
		{"sample.c", "void hello() {}\n"},
		{"sample.cpp", "void hello() {}\n"},
		// ctags-only language
		{"sample.lua", "function hello()\nend\n"},
		// Unknown extension
		{"sample.xyz", "whatever"},
	}

	for _, tc := range cases {
		p := filepath.Join(dir, tc.name)
		if err := os.WriteFile(p, []byte(tc.src), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Test with Runner (ctags-only languages get ctags)
	for _, tc := range cases {
		p := filepath.Join(dir, tc.name)
		f, err := Outline(ctx, p, nil, Options{Runner: fakeRunner})
		if err != nil {
			t.Fatalf("Outline(%s) error: %v", tc.name, err)
		}

		// Backend is one of the four valid values.
		if !validBackends[f.Backend] {
			t.Errorf("%s: Backend = %q, not in valid set", tc.name, f.Backend)
		}

		// Decls is non-nil.
		if f.Decls == nil {
			t.Errorf("%s: Decls is nil", tc.name)
		}

		// Sorted by (StartLine, Name).
		for i := 1; i < len(f.Decls); i++ {
			prev := f.Decls[i-1]
			cur := f.Decls[i]
			if prev.StartLine > cur.StartLine {
				t.Errorf("%s: Decls not sorted by StartLine: %d > %d", tc.name, prev.StartLine, cur.StartLine)
			}
			if prev.StartLine == cur.StartLine && prev.Name > cur.Name {
				t.Errorf("%s: Decls not sorted by Name at line %d: %q > %q", tc.name, prev.StartLine, prev.Name, cur.Name)
			}
		}
	}

	// Test with nil Runner (heuristic or none)
	for _, tc := range cases {
		p := filepath.Join(dir, tc.name)
		f, err := Outline(ctx, p, nil, Options{})
		if err != nil {
			t.Fatalf("Outline(%s) nil Runner error: %v", tc.name, err)
		}

		if !validBackends[f.Backend] {
			t.Errorf("%s (nil Runner): Backend = %q, not in valid set", tc.name, f.Backend)
		}
		if f.Decls == nil {
			t.Errorf("%s (nil Runner): Decls is nil", tc.name)
		}
		for i := 1; i < len(f.Decls); i++ {
			prev := f.Decls[i-1]
			cur := f.Decls[i]
			if prev.StartLine > cur.StartLine {
				t.Errorf("%s (nil Runner): Decls not sorted by StartLine: %d > %d", tc.name, prev.StartLine, cur.StartLine)
			}
			if prev.StartLine == cur.StartLine && prev.Name > cur.Name {
				t.Errorf("%s (nil Runner): Decls not sorted by Name at line %d: %q > %q", tc.name, prev.StartLine, prev.Name, cur.Name)
			}
		}
	}

	// Test OutlineMany with the fake Runner.
	var srcs []Source
	for _, tc := range cases {
		srcs = append(srcs, Source{Abs: filepath.Join(dir, tc.name)})
	}
	fs, _, err := OutlineMany(ctx, srcs, Options{Runner: fakeRunner})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}
	if len(fs) != len(cases) {
		t.Fatalf("OutlineMany: len(fs) = %d, want %d", len(fs), len(cases))
	}
	for i, f := range fs {
		if !validBackends[f.Backend] {
			t.Errorf("OutlineMany[%d] (%s): Backend = %q, not in valid set", i, cases[i].name, f.Backend)
		}
		if f.Decls == nil {
			t.Errorf("OutlineMany[%d] (%s): Decls is nil", i, cases[i].name)
		}
		for j := 1; j < len(f.Decls); j++ {
			prev := f.Decls[j-1]
			cur := f.Decls[j]
			if prev.StartLine > cur.StartLine {
				t.Errorf("OutlineMany[%d] (%s): Decls not sorted by StartLine: %d > %d", i, cases[i].name, prev.StartLine, cur.StartLine)
			}
			if prev.StartLine == cur.StartLine && prev.Name > cur.Name {
				t.Errorf("OutlineMany[%d] (%s): Decls not sorted by Name at line %d: %q > %q", i, cases[i].name, prev.StartLine, prev.Name, cur.Name)
			}
		}
	}
}

// TS-01-27 (unit): A nil Runner skips ctags and uses heuristic or none.
func TestNilRunnerSkipsCtags_TS_01_27(t *testing.T) {
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "hello.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	luaFile := filepath.Join(dir, "hello.lua")
	if err := os.WriteFile(luaFile, []byte("function hello()\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	fs, st, err := OutlineMany(ctx, []Source{
		{Abs: pyFile},
		{Abs: luaFile},
	}, Options{Runner: nil})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}

	if len(fs) != 2 {
		t.Fatalf("len(fs) = %d, want 2", len(fs))
	}

	// Python file has heuristic backend.
	if fs[0].Backend != BackendHeuristic {
		t.Errorf("Python Backend = %q, want %q", fs[0].Backend, BackendHeuristic)
	}

	// Lua file has none backend (no heuristic for Lua).
	if fs[1].Backend != BackendNone {
		t.Errorf("Lua Backend = %q, want %q", fs[1].Backend, BackendNone)
	}

	// No fallback counted (ctags was never attempted).
	if st.Fallbacks != 0 {
		t.Errorf("Fallbacks = %d, want 0", st.Fallbacks)
	}
}

// TS-01-37 (unit): A Runner error or unusable output falls back per file with
// a nil error and a Stats record.
func TestRunnerErrorFallback_TS_01_37(t *testing.T) {
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "hello.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	luaFile := filepath.Join(dir, "hello.lua")
	if err := os.WriteFile(luaFile, []byte("function hello()\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Case 1: Runner returns an error.
	errRunner := func(_ context.Context, _ []string) ([]byte, error) {
		return nil, errors.New("ctags crashed")
	}

	fs, st, err := OutlineMany(ctx, []Source{
		{Abs: pyFile},
		{Abs: luaFile},
	}, Options{Runner: errRunner})
	if err != nil {
		t.Fatalf("Case 1: OutlineMany returned error: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("Case 1: len(fs) = %d, want 2", len(fs))
	}

	// Python falls back to heuristic.
	if fs[0].Backend != BackendHeuristic {
		t.Errorf("Case 1: Python Backend = %q, want %q", fs[0].Backend, BackendHeuristic)
	}
	// Python should have declarations from heuristic.
	if len(fs[0].Decls) == 0 {
		t.Error("Case 1: Python Decls is empty, want at least one decl from heuristic")
	}

	// Lua falls back to none (no heuristic for Lua).
	if fs[1].Backend != BackendNone {
		t.Errorf("Case 1: Lua Backend = %q, want %q", fs[1].Backend, BackendNone)
	}
	if fs[1].Decls == nil {
		t.Error("Case 1: Lua Decls is nil")
	}
	if len(fs[1].Decls) != 0 {
		t.Errorf("Case 1: Lua Decls has %d entries, want 0", len(fs[1].Decls))
	}

	// Stats records the fallback.
	if st.Fallbacks != 1 {
		t.Errorf("Case 1: Fallbacks = %d, want 1", st.Fallbacks)
	}

	// Case 2: Runner returns non-JSON garbage.
	garbageRunner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte("this is not json\nalso not json\n"), nil
	}

	fs2, st2, err2 := OutlineMany(ctx, []Source{
		{Abs: pyFile},
		{Abs: luaFile},
	}, Options{Runner: garbageRunner})
	if err2 != nil {
		t.Fatalf("Case 2: OutlineMany returned error: %v", err2)
	}
	if len(fs2) != 2 {
		t.Fatalf("Case 2: len(fs) = %d, want 2", len(fs2))
	}

	// With garbage output, all lines are malformed. The batch should fall back.
	// Python falls back to heuristic.
	if fs2[0].Backend != BackendHeuristic {
		t.Errorf("Case 2: Python Backend = %q, want %q", fs2[0].Backend, BackendHeuristic)
	}
	// Lua falls back to none.
	if fs2[1].Backend != BackendNone {
		t.Errorf("Case 2: Lua Backend = %q, want %q", fs2[1].Backend, BackendNone)
	}

	// Stats records the malformed lines and fallback.
	if st2.MalformedLines != 2 {
		t.Errorf("Case 2: MalformedLines = %d, want 2", st2.MalformedLines)
	}
	if st2.Fallbacks != 1 {
		t.Errorf("Case 2: Fallbacks = %d, want 1", st2.Fallbacks)
	}
}

// TS-01-38 (unit): Cancellation while the Runner runs returns ctx.Err() and
// does not fall back to heuristics.
func TestCancellationDuringRunner_TS_01_38(t *testing.T) {
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "hello.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	blockingRunner := func(ctx context.Context, _ []string) ([]byte, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	go func() {
		<-started
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, st, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: blockingRunner})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	// No fallback counted.
	if st.Fallbacks != 0 {
		t.Errorf("Fallbacks = %d, want 0", st.Fallbacks)
	}
}

// TS-01-39 (unit): Heuristic fixtures for the ten languages match their golden
// expectations with Container empty and EndLine 0.
func TestHeuristicFixtures_TS_01_39(t *testing.T) {
	ctx := context.Background()

	type expected struct {
		name     string
		kind     Kind
		exported bool
	}

	tests := []struct {
		fixture  string
		lang     string
		expected []expected
	}{
		{
			fixture: "testdata/heuristic_python.py",
			lang:    LangPython,
			expected: []expected{
				{"public_func", KindFunc, true},
				{"_private_func", KindFunc, false},
				{"MyClass", KindClass, true},
				{"async_func", KindFunc, true},
			},
		},
		{
			fixture: "testdata/heuristic_javascript.js",
			lang:    LangJavaScript,
			expected: []expected{
				{"localFunc", KindFunc, false},
				{"exportedFunc", KindFunc, true},
				{"defaultFunc", KindFunc, true},
				{"asyncExported", KindFunc, true},
				{"LocalClass", KindClass, false},
				{"ExportedClass", KindClass, true},
			},
		},
		{
			fixture: "testdata/heuristic_typescript.ts",
			lang:    LangTypeScript,
			expected: []expected{
				{"localFunc", KindFunc, false},
				{"exportedFunc", KindFunc, true},
				{"defaultFunc", KindFunc, true},
				{"asyncExported", KindFunc, true},
				{"LocalClass", KindClass, false},
				{"ExportedClass", KindClass, true},
				{"ExportedInterface", KindInterface, true},
				{"LocalInterface", KindInterface, false},
				{"Direction", KindEnum, true},
			},
		},
		{
			fixture: "testdata/heuristic_rust.rs",
			lang:    LangRust,
			expected: []expected{
				{"private_fn", KindFunc, false},
				{"public_fn", KindFunc, true},
				{"crate_fn", KindFunc, true},
				{"PrivateStruct", KindType, false},
				{"PublicStruct", KindType, true},
				{"Direction", KindEnum, true},
				{"PrivateTrait", KindTrait, false},
				{"PublicTrait", KindTrait, true},
			},
		},
		{
			fixture: "testdata/heuristic_java.java",
			lang:    LangJava,
			expected: []expected{
				{"MyClass", KindClass, true},
				{"PackageClass", KindClass, false},
				{"MyInterface", KindInterface, true},
				{"Color", KindEnum, true},
			},
		},
		{
			fixture: "testdata/heuristic_kotlin.kt",
			lang:    LangKotlin,
			expected: []expected{
				{"publicFun", KindFunc, true},
				{"packageFun", KindFunc, true}, // Kotlin's default visibility is public
				{"PublicClass", KindClass, true},
				{"PackageClass", KindClass, true},
				{"PublicInterface", KindInterface, true},
				{"Direction", KindEnum, true},
				{"privateFun", KindFunc, false},
				{"InternalClass", KindClass, false},
			},
		},
		{
			fixture: "testdata/heuristic_csharp.cs",
			lang:    LangCSharp,
			expected: []expected{
				{"MyClass", KindClass, true},
				{"InternalClass", KindClass, false},
				{"IMyInterface", KindInterface, true},
				{"Color", KindEnum, true},
			},
		},
		{
			fixture: "testdata/heuristic_ruby.rb",
			lang:    LangRuby,
			expected: []expected{
				{"public_method", KindFunc, true},
				{"_private_method", KindFunc, false},
				{"MyClass", KindClass, true},
				{"MyModule", KindModule, true},
			},
		},
		{
			fixture: "testdata/heuristic_c.c",
			lang:    LangC,
			expected: []expected{
				{"hello", KindFunc, true},
				{"add", KindFunc, true},
				{"Point", KindType, true},
				{"Color", KindEnum, true},
				{"MAX_SIZE", KindMacro, true},
			},
		},
		{
			fixture: "testdata/heuristic_cpp.cpp",
			lang:    LangCPP,
			expected: []expected{
				{"hello", KindFunc, true},
				{"add", KindFunc, true},
				{"MyClass", KindClass, true},
				{"Point", KindType, true},
				{"MyNamespace", KindModule, true},
				{"Color", KindEnum, true},
				{"MAX_SIZE", KindMacro, true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			abs, err := filepath.Abs(tt.fixture)
			if err != nil {
				t.Fatal(err)
			}

			f, err := Outline(ctx, abs, nil, Options{})
			if err != nil {
				t.Fatalf("Outline error: %v", err)
			}

			if f.Backend != BackendHeuristic {
				t.Errorf("Backend = %q, want %q", f.Backend, BackendHeuristic)
			}
			if f.Lang != tt.lang {
				t.Errorf("Lang = %q, want %q", f.Lang, tt.lang)
			}

			dm := declMap(f.Decls)

			for _, exp := range tt.expected {
				d, ok := dm[exp.name]
				if !ok {
					t.Errorf("expected decl %q not found; got %v", exp.name, declNames(f.Decls))
					continue
				}
				if d.Kind != exp.kind {
					t.Errorf("decl %q: Kind = %q, want %q", exp.name, d.Kind, exp.kind)
				}
				if d.Exported != exp.exported {
					t.Errorf("decl %q: Exported = %v, want %v", exp.name, d.Exported, exp.exported)
				}
				// Container must be empty for heuristic.
				if d.Container != "" {
					t.Errorf("decl %q: Container = %q, want empty", exp.name, d.Container)
				}
				// EndLine must be 0 for heuristic.
				if d.EndLine != 0 {
					t.Errorf("decl %q: EndLine = %d, want 0", exp.name, d.EndLine)
				}
			}
		})
	}
}

// TS-01-40 (unit): Heuristic Exported follows each language's visibility rule.
func TestHeuristicExported_TS_01_40(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	type visCase struct {
		lang     string
		ext      string
		src      string
		name     string
		exported bool
	}

	cases := []visCase{
		// Python: underscore = not exported
		{LangPython, ".py", "def hello():\n    pass\n", "hello", true},
		{LangPython, ".py", "def _private():\n    pass\n", "_private", false},

		// Ruby: underscore = not exported
		{LangRuby, ".rb", "def hello\n  nil\nend\n", "hello", true},
		{LangRuby, ".rb", "def _private\n  nil\nend\n", "_private", false},

		// Rust: pub = exported, no pub = not exported
		{LangRust, ".rs", "pub fn public_fn() {}\n", "public_fn", true},
		{LangRust, ".rs", "fn private_fn() {}\n", "private_fn", false},

		// JavaScript: export = exported, no export = not exported
		{LangJavaScript, ".js", "export function exported() {}\n", "exported", true},
		{LangJavaScript, ".js", "function local() {}\n", "local", false},

		// TypeScript: export = exported, no export = not exported
		{LangTypeScript, ".ts", "export function exported(): void {}\n", "exported", true},
		{LangTypeScript, ".ts", "function local(): void {}\n", "local", false},

		// Java: public = exported, no public = not exported
		{LangJava, ".java", "public class Pub {}\n", "Pub", true},
		{LangJava, ".java", "class Pkg {}\n", "Pkg", false},

		// Kotlin: PUBLIC BY DEFAULT — exported unless private, internal or
		// protected (issue #89; erratum 01_outline_kotlin_unicode_swift).
		{LangKotlin, ".kt", "public fun pubFun() {}\n", "pubFun", true},
		{LangKotlin, ".kt", "fun pkgFun() {}\n", "pkgFun", true},
		{LangKotlin, ".kt", "class Plain\n", "Plain", true},
		{LangKotlin, ".kt", "data class Point(val x: Int)\n", "Point", true},
		{LangKotlin, ".kt", "private fun hidden() {}\n", "hidden", false},
		{LangKotlin, ".kt", "internal class Inner\n", "Inner", false},
		{LangKotlin, ".kt", "protected open fun prot() {}\n", "prot", false},
		{LangKotlin, ".kt", "private data class P(val x: Int)\n", "P", false},

		// C#: public = exported, no public = not exported
		{LangCSharp, ".cs", "public class Pub {}\n", "Pub", true},
		{LangCSharp, ".cs", "class Pkg {}\n", "Pkg", false},

		// C: always exported
		{LangC, ".c", "void hello() {}\n", "hello", true},

		// C++: always exported
		{LangCPP, ".cpp", "void hello() {}\n", "hello", true},
	}

	for i, tc := range cases {
		t.Run(fmt.Sprintf("%s_%s_%d", tc.lang, tc.name, i), func(t *testing.T) {
			p := filepath.Join(dir, fmt.Sprintf("test_%d%s", i, tc.ext))
			if err := os.WriteFile(p, []byte(tc.src), 0o644); err != nil {
				t.Fatal(err)
			}

			f, err := Outline(ctx, p, nil, Options{})
			if err != nil {
				t.Fatalf("Outline error: %v", err)
			}

			dm := declMap(f.Decls)
			d, ok := dm[tc.name]
			if !ok {
				t.Fatalf("decl %q not found; got %v", tc.name, declNames(f.Decls))
			}
			if d.Exported != tc.exported {
				t.Errorf("decl %q: Exported = %v, want %v", tc.name, d.Exported, tc.exported)
			}
		})
	}
}

// TS-01-41 (unit): Indented declarations such as methods in a class are not
// reported by the heuristic.
func TestHeuristicNoIndented_TS_01_41(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Python: class A with indented def m(self)
	pySrc := "class A:\n    def m(self):\n        pass\n"
	pyFile := filepath.Join(dir, "indent.py")
	if err := os.WriteFile(pyFile, []byte(pySrc), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Outline(ctx, pyFile, nil, Options{})
	if err != nil {
		t.Fatalf("Python Outline error: %v", err)
	}

	names := declNames(f.Decls)
	if !sliceContains(names, "A") {
		t.Errorf("expected A in decls, got %v", names)
	}
	if sliceContains(names, "m") {
		t.Errorf("indented method m should not be in decls, got %v", names)
	}

	// JavaScript: class with indented method
	jsSrc := "export class Foo {\n    bar() {}\n    baz() {}\n}\n"
	jsFile := filepath.Join(dir, "indent.js")
	if err := os.WriteFile(jsFile, []byte(jsSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	f2, err := Outline(ctx, jsFile, nil, Options{})
	if err != nil {
		t.Fatalf("JS Outline error: %v", err)
	}

	names2 := declNames(f2.Decls)
	if !sliceContains(names2, "Foo") {
		t.Errorf("expected Foo in decls, got %v", names2)
	}
	if sliceContains(names2, "bar") {
		t.Errorf("indented method bar should not be in decls, got %v", names2)
	}
	if sliceContains(names2, "baz") {
		t.Errorf("indented method baz should not be in decls, got %v", names2)
	}
}

// TS-01-42 (unit): A language with no heuristic and no usable ctags yields none
// with its Lang and empty Decls.
func TestNoHeuristicNoCtags_TS_01_42(t *testing.T) {
	dir := t.TempDir()

	luaFile := filepath.Join(dir, "hello.lua")
	if err := os.WriteFile(luaFile, []byte("function hello()\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	// Case 1: nil Runner.
	f, err := Outline(ctx, luaFile, nil, Options{})
	if err != nil {
		t.Fatalf("nil Runner: unexpected error: %v", err)
	}
	if f.Backend != BackendNone {
		t.Errorf("nil Runner: Backend = %q, want %q", f.Backend, BackendNone)
	}
	if f.Lang != LangLua {
		t.Errorf("nil Runner: Lang = %q, want %q", f.Lang, LangLua)
	}
	if f.Decls == nil {
		t.Error("nil Runner: Decls is nil, want non-nil empty slice")
	}
	if len(f.Decls) != 0 {
		t.Errorf("nil Runner: Decls has %d entries, want 0", len(f.Decls))
	}

	// Case 2: failing Runner.
	failRunner := func(_ context.Context, _ []string) ([]byte, error) {
		return nil, errors.New("ctags not found")
	}

	f2, err := Outline(ctx, luaFile, nil, Options{Runner: failRunner})
	if err != nil {
		t.Fatalf("failing Runner: unexpected error: %v", err)
	}
	if f2.Backend != BackendNone {
		t.Errorf("failing Runner: Backend = %q, want %q", f2.Backend, BackendNone)
	}
	if f2.Lang != LangLua {
		t.Errorf("failing Runner: Lang = %q, want %q", f2.Lang, LangLua)
	}
	if f2.Decls == nil {
		t.Error("failing Runner: Decls is nil, want non-nil empty slice")
	}
	if len(f2.Decls) != 0 {
		t.Errorf("failing Runner: Decls has %d entries, want 0", len(f2.Decls))
	}

	// Case 3: OutlineMany with nil Runner.
	fs, _, err := OutlineMany(ctx, []Source{{Abs: luaFile}}, Options{})
	if err != nil {
		t.Fatalf("OutlineMany nil Runner: unexpected error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("OutlineMany nil Runner: len(fs) = %d, want 1", len(fs))
	}
	if fs[0].Backend != BackendNone {
		t.Errorf("OutlineMany nil Runner: Backend = %q, want %q", fs[0].Backend, BackendNone)
	}
	if fs[0].Lang != LangLua {
		t.Errorf("OutlineMany nil Runner: Lang = %q, want %q", fs[0].Lang, LangLua)
	}
	if fs[0].Decls == nil {
		t.Error("OutlineMany nil Runner: Decls is nil")
	}
	if len(fs[0].Decls) != 0 {
		t.Errorf("OutlineMany nil Runner: Decls has %d entries, want 0", len(fs[0].Decls))
	}

	// Case 4: OutlineMany with failing Runner.
	fs2, _, err := OutlineMany(ctx, []Source{{Abs: luaFile}}, Options{Runner: failRunner})
	if err != nil {
		t.Fatalf("OutlineMany failing Runner: unexpected error: %v", err)
	}
	if len(fs2) != 1 {
		t.Fatalf("OutlineMany failing Runner: len(fs) = %d, want 1", len(fs2))
	}
	if fs2[0].Backend != BackendNone {
		t.Errorf("OutlineMany failing Runner: Backend = %q, want %q", fs2[0].Backend, BackendNone)
	}
	if fs2[0].Lang != LangLua {
		t.Errorf("OutlineMany failing Runner: Lang = %q, want %q", fs2[0].Lang, LangLua)
	}
	if fs2[0].Decls == nil {
		t.Error("OutlineMany failing Runner: Decls is nil")
	}
}

// TestHeuristicIdentifiersAreUnicode (issue #89): an identifier is letters,
// digits and underscore in any script, plus the characters a language adds to
// its own — JavaScript's $, Ruby's trailing ? and ! — so a name is never cut
// at its first non-ASCII letter.
func TestHeuristicIdentifiersAreUnicode(t *testing.T) {
	dir := t.TempDir()
	for i, c := range []struct{ file, src, want string }{
		{"a.py", "def café():\n    pass\n", "café"},
		{"b.py", "class Größe:\n    pass\n", "Größe"},
		{"c.java", "public class Ölfass {}\n", "Ölfass"},
		{"d.kt", "fun größe() {}\n", "größe"},
		{"e.js", "function $init() {}\n", "$init"},
		{"f.ts", "export const $store = () => 1\n", "$store"},
		{"g.rb", "def valid?\nend\n", "valid?"},
		{"h.rs", "pub fn naïve() {}\n", "naïve"},
	} {
		p := filepath.Join(dir, fmt.Sprintf("%d_%s", i, c.file))
		if err := os.WriteFile(p, []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := Outline(context.Background(), p, nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := declMap(f.Decls)[c.want]; !ok {
			t.Errorf("%s: decls %v, want %q", c.file, declNames(f.Decls), c.want)
		}
	}
}

// TestHeuristicReadsLineOneBehindABOM (issue #89): a UTF-8 byte-order mark
// is not part of the first declaration's line.
func TestHeuristicReadsLineOneBehindABOM(t *testing.T) {
	dir := t.TempDir()
	bom := "\xEF\xBB\xBF"
	for _, c := range []struct {
		file, src string
		want      []string
	}{
		{"a.java", bom + "public class First {}\npublic class Second {}\n", []string{"First", "Second"}},
		{"b.py", bom + "def first():\n    pass\n", []string{"first"}},
	} {
		p := filepath.Join(dir, c.file)
		if err := os.WriteFile(p, []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := Outline(context.Background(), p, nil, Options{})
		if err != nil {
			t.Fatal(err)
		}
		dm := declMap(f.Decls)
		for _, w := range c.want {
			d, ok := dm[w]
			if !ok {
				t.Errorf("%s: decls %v, want %q", c.file, declNames(f.Decls), w)
				continue
			}
			if d.StartLine == 1 && d.Signature != "" && d.Signature[0] == 0xEF {
				t.Errorf("%s: %q's signature carries the BOM: %q", c.file, w, d.Signature)
			}
		}
	}
}

// TestSwiftAndScalaAreNotInTheTable (issue #89): Universal Ctags has no
// parser for either, and neither has a heuristic, so listing them only turned
// "no backend" into a false "0 declarations".
func TestSwiftAndScalaAreNotInTheTable(t *testing.T) {
	for _, ext := range []string{".swift", ".scala"} {
		if lang := LangForExt(ext); lang != "" {
			t.Errorf("LangForExt(%q) = %q, want it outside the table", ext, lang)
		}
	}
}
