package outline_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/outline"
	"github.com/agentfox/agentkit-go/tools"
)

// TS-01-72 (smoke): An embedder outlines a Go file with no subprocess and gets
// a root-relative go/ast File.
func TestOutlineGoFileNoSubprocess_TS_01_72(t *testing.T) {
	root := t.TempDir()
	goSrc := `package pkg

type MyStruct struct {
	Field int
}

func (m *MyStruct) Method() string {
	return ""
}

func FreeFunc(x int) int {
	return x + 1
}

const Version = "1.0"
`
	goFile := filepath.Join(root, "pkg", "a.go")
	if err := os.MkdirAll(filepath.Dir(goFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(goFile, []byte(goSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set PATH to empty so any spawn would fail (proving no subprocess).
	if runtime.GOOS != "windows" {
		t.Setenv("PATH", "")
	}

	ctx := context.Background()
	f, err := outline.Outline(ctx, goFile, nil, outline.Options{Root: root})
	if err != nil {
		t.Fatalf("Outline: %v", err)
	}

	// File.Path is root-relative, slash-separated.
	if f.Path != "pkg/a.go" {
		t.Fatalf("File.Path = %q, want %q", f.Path, "pkg/a.go")
	}

	// Backend is go/ast.
	if f.Backend != outline.BackendGoAST {
		t.Fatalf("Backend = %q, want %q", f.Backend, outline.BackendGoAST)
	}

	// Decls are sorted by StartLine.
	for i := 1; i < len(f.Decls); i++ {
		if f.Decls[i].StartLine < f.Decls[i-1].StartLine {
			t.Fatalf("Decls not sorted by StartLine: %v at %d before %v at %d",
				f.Decls[i-1].Name, f.Decls[i-1].StartLine,
				f.Decls[i].Name, f.Decls[i].StartLine)
		}
	}

	// Build a map for easy lookup.
	declMap := map[string]outline.Decl{}
	for _, d := range f.Decls {
		declMap[d.Name] = d
	}

	// Check expected declarations.
	if d, ok := declMap["MyStruct"]; !ok {
		t.Error("missing MyStruct")
	} else {
		if d.Kind != outline.KindType {
			t.Errorf("MyStruct.Kind = %q, want %q", d.Kind, outline.KindType)
		}
		if !d.Exported {
			t.Error("MyStruct should be exported")
		}
	}

	if d, ok := declMap["Method"]; !ok {
		t.Error("missing Method")
	} else {
		if d.Kind != outline.KindMethod {
			t.Errorf("Method.Kind = %q, want %q", d.Kind, outline.KindMethod)
		}
		if d.Container != "MyStruct" {
			t.Errorf("Method.Container = %q, want %q", d.Container, "MyStruct")
		}
	}

	if d, ok := declMap["FreeFunc"]; !ok {
		t.Error("missing FreeFunc")
	} else {
		if d.Kind != outline.KindFunc {
			t.Errorf("FreeFunc.Kind = %q, want %q", d.Kind, outline.KindFunc)
		}
	}

	if d, ok := declMap["Version"]; !ok {
		t.Error("missing Version")
	} else {
		if d.Kind != outline.KindConst {
			t.Errorf("Version.Kind = %q, want %q", d.Kind, outline.KindConst)
		}
	}

	// Verify ranges match go/ast by parsing the same source independently.
	fset := token.NewFileSet()
	astFile, err := parser.ParseFile(fset, goFile, goSrc, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("go/parser: %v", err)
	}
	astDecls := map[string][2]int{} // name -> [startLine, endLine]
	for _, d := range astFile.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			astDecls[name] = [2]int{
				fset.Position(d.Pos()).Line,
				fset.Position(d.End()).Line,
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					grouped := d.Lparen.IsValid()
					var sl, el int
					if grouped {
						sl = fset.Position(spec.Pos()).Line
						el = fset.Position(spec.End()).Line
					} else {
						sl = fset.Position(d.Pos()).Line
						el = fset.Position(d.End()).Line
					}
					astDecls[spec.Name.Name] = [2]int{sl, el}
				case *ast.ValueSpec:
					for _, ident := range spec.Names {
						grouped := d.Lparen.IsValid()
						var sl, el int
						if grouped {
							sl = fset.Position(spec.Pos()).Line
							el = fset.Position(spec.End()).Line
						} else {
							sl = fset.Position(d.Pos()).Line
							el = fset.Position(d.End()).Line
						}
						astDecls[ident.Name] = [2]int{sl, el}
					}
				}
			}
		}
	}

	for _, d := range f.Decls {
		expected, ok := astDecls[d.Name]
		if !ok {
			continue // e.g. _ or something we didn't track
		}
		if d.StartLine != expected[0] {
			t.Errorf("%s.StartLine = %d, want %d", d.Name, d.StartLine, expected[0])
		}
		if d.EndLine != expected[1] {
			t.Errorf("%s.EndLine = %d, want %d", d.Name, d.EndLine, expected[1])
		}
	}

	// Verify signatures are sanitised (no newlines, no control chars, <= 200 bytes).
	for _, d := range f.Decls {
		if strings.ContainsAny(d.Signature, "\n\r") {
			t.Errorf("%s signature contains newline: %q", d.Name, d.Signature)
		}
		if len(d.Signature) > 200 {
			t.Errorf("%s signature is %d bytes, want <= 200", d.Name, len(d.Signature))
		}
	}
}

// TS-01-73 (smoke): OutlineMany with the real CtagsRunner outlines Python and
// Rust files in one ctags batch.
func TestOutlineManyWithRealCtags_TS_01_73(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: ctags process handling differs")
	}
	// Skip if universal-ctags is not available.
	bin, err := exec.LookPath("ctags")
	if err != nil {
		t.Skip("ctags not on PATH; skipping real ctags test")
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || !strings.Contains(string(out), "Universal Ctags") {
		t.Skip("ctags is not Universal Ctags; skipping")
	}

	root := t.TempDir()
	pySrc := `def greet(name):
    return f"Hello, {name}"

class Greeter:
    def say_hi(self):
        pass
`
	rsSrc := `pub fn add(a: i32, b: i32) -> i32 {
    a + b
}

pub struct Point {
    x: f64,
    y: f64,
}
`
	pyFile := filepath.Join(root, "hello.py")
	rsFile := filepath.Join(root, "lib.rs")
	if err := os.WriteFile(pyFile, []byte(pySrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rsFile, []byte(rsSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	runner := tools.CtagsRunner(nil)
	srcs := []outline.Source{
		{Abs: pyFile},
		{Abs: rsFile},
	}
	files, stats, err := outline.OutlineMany(ctx, srcs, outline.Options{
		Root:   root,
		Runner: runner,
	})
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}

	// Files returned in input order.
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}

	// Python file.
	pyOut := files[0]
	if pyOut.Path != "hello.py" {
		t.Errorf("Python File.Path = %q, want %q", pyOut.Path, "hello.py")
	}
	if pyOut.Backend != outline.BackendCtags {
		t.Errorf("Python Backend = %q, want %q", pyOut.Backend, outline.BackendCtags)
	}
	// Should have at least greet and Greeter.
	pyNames := declNames(pyOut.Decls)
	if !sliceContains(pyNames, "greet") {
		t.Errorf("Python decls missing 'greet': %v", pyNames)
	}
	if !sliceContains(pyNames, "Greeter") {
		t.Errorf("Python decls missing 'Greeter': %v", pyNames)
	}

	// Rust file.
	rsOut := files[1]
	if rsOut.Path != "lib.rs" {
		t.Errorf("Rust File.Path = %q, want %q", rsOut.Path, "lib.rs")
	}
	if rsOut.Backend != outline.BackendCtags {
		t.Errorf("Rust Backend = %q, want %q", rsOut.Backend, outline.BackendCtags)
	}
	rsNames := declNames(rsOut.Decls)
	if !sliceContains(rsNames, "add") {
		t.Errorf("Rust decls missing 'add': %v", rsNames)
	}
	if !sliceContains(rsNames, "Point") {
		t.Errorf("Rust decls missing 'Point': %v", rsNames)
	}

	// Tags are attributed to the right files.
	for _, d := range pyOut.Decls {
		if sliceContains([]string{"add", "Point"}, d.Name) {
			t.Errorf("Python file contains Rust decl %q", d.Name)
		}
	}
	for _, d := range rsOut.Decls {
		if sliceContains([]string{"greet", "Greeter"}, d.Name) {
			t.Errorf("Rust file contains Python decl %q", d.Name)
		}
	}

	// Signatures are source lines (not empty).
	for _, d := range pyOut.Decls {
		if d.Signature == "" {
			t.Errorf("Python decl %q has empty signature", d.Name)
		}
	}
	for _, d := range rsOut.Decls {
		if d.Signature == "" {
			t.Errorf("Rust decl %q has empty signature", d.Name)
		}
	}

	// Stats shows no fallback.
	if stats.Fallbacks != 0 {
		t.Errorf("Stats.Fallbacks = %d, want 0", stats.Fallbacks)
	}
}

// TS-01-74 (smoke): Without universal-ctags, a Python file degrades to
// heuristics and a Lua file to none.
func TestOutlineManyDegradation_TS_01_74(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: empty PATH handling differs")
	}

	root := t.TempDir()
	pySrc := `def greet(name):
    return f"Hello, {name}"

class Greeter:
    def say_hi(self):
        pass

def _private():
    pass
`
	luaSrc := `function hello()
  print("hello")
end
`
	pyFile := filepath.Join(root, "hello.py")
	luaFile := filepath.Join(root, "util.lua")
	if err := os.WriteFile(pyFile, []byte(pySrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(luaFile, []byte(luaSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	// Set PATH to an empty directory so ctags is unavailable.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	ctx := context.Background()
	runner := tools.CtagsRunner(nil)
	srcs := []outline.Source{
		{Abs: pyFile},
		{Abs: luaFile},
	}
	files, stats, err := outline.OutlineMany(ctx, srcs, outline.Options{
		Root:   root,
		Runner: runner,
	})

	// The error is nil.
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}

	// Python file has Backend heuristic with only column-0 declarations.
	pyOut := files[0]
	if pyOut.Backend != outline.BackendHeuristic {
		t.Fatalf("Python Backend = %q, want %q", pyOut.Backend, outline.BackendHeuristic)
	}
	pyNames := declNames(pyOut.Decls)
	// Column-0 declarations: greet, Greeter, _private.
	if !sliceContains(pyNames, "greet") {
		t.Errorf("Python heuristic missing 'greet': %v", pyNames)
	}
	if !sliceContains(pyNames, "Greeter") {
		t.Errorf("Python heuristic missing 'Greeter': %v", pyNames)
	}
	if !sliceContains(pyNames, "_private") {
		t.Errorf("Python heuristic missing '_private': %v", pyNames)
	}
	// Indented method say_hi should NOT be reported.
	if sliceContains(pyNames, "say_hi") {
		t.Errorf("Python heuristic should not report indented method 'say_hi': %v", pyNames)
	}

	// Lua file has Backend none, Lang lua and empty Decls.
	luaOut := files[1]
	if luaOut.Backend != outline.BackendNone {
		t.Fatalf("Lua Backend = %q, want %q", luaOut.Backend, outline.BackendNone)
	}
	if luaOut.Lang != "Lua" {
		t.Fatalf("Lua Lang = %q, want %q", luaOut.Lang, "Lua")
	}
	if len(luaOut.Decls) != 0 {
		t.Fatalf("Lua Decls has %d entries, want 0", len(luaOut.Decls))
	}

	// Stats records the batch fallback.
	if stats.Fallbacks == 0 {
		t.Error("Stats.Fallbacks = 0, want > 0 (batch fell back from ctags)")
	}
}

// TS-01-75 (smoke): An embedder walks a workspace and outlines exactly the
// files search_files would select.
func TestWalkAndOutlineMatchesSearchFiles_TS_01_75(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".gitignore", "ignored/\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("lib.py", "def helper():\n    pass\n")
	write(".hidden.go", "package hidden\n")
	write("ignored/skip.go", "package skip\n")

	// Create a symlink.
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(filepath.Join(root, "main.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	resolvedRoot := ws.Root

	ctx := context.Background()

	// Walk with IncludeHidden=false, collecting regular files.
	var walkPaths []string
	err = tools.Walk(ctx, ws, resolvedRoot, tools.WalkOptions{
		Ignore:        tools.NoGlobalExcludes(),
		IncludeHidden: false,
	}, func(rel string, d os.DirEntry) error {
		if d.Type().IsRegular() {
			walkPaths = append(walkPaths, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	// Search with a match-everything pattern.
	res, err := tools.SearchIn(ctx, resolvedRoot, tools.SearchParams{Pattern: "."}, tools.NoGlobalExcludes())
	if err != nil {
		t.Fatalf("SearchIn: %v", err)
	}
	searchSet := map[string]bool{}
	for _, m := range res.Matches {
		searchSet[m.File] = true
	}

	// The walk set should equal the search set.
	walkSet := map[string]bool{}
	for _, f := range walkPaths {
		walkSet[f] = true
	}
	for f := range searchSet {
		if !walkSet[f] {
			t.Errorf("search_files found %q but Walk did not visit it", f)
		}
	}
	for f := range walkSet {
		if !searchSet[f] {
			t.Errorf("Walk visited %q but search_files did not search it", f)
		}
	}

	// Hidden and ignored files are absent.
	if walkSet[".hidden.go"] {
		t.Error("hidden file .hidden.go should not be in walk set")
	}
	if walkSet["ignored/skip.go"] {
		t.Error("ignored file should not be in walk set")
	}

	// Outline the walked files.
	srcs := make([]outline.Source, len(walkPaths))
	for i, rel := range walkPaths {
		srcs[i] = outline.Source{Abs: filepath.Join(resolvedRoot, filepath.FromSlash(rel))}
	}
	files, _, err := outline.OutlineMany(ctx, srcs, outline.Options{Root: resolvedRoot})
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}

	// One File per collected path.
	if len(files) != len(walkPaths) {
		t.Fatalf("OutlineMany returned %d files, want %d", len(files), len(walkPaths))
	}

	// Go files have Backend go/ast and Python files heuristic.
	for _, f := range files {
		switch {
		case strings.HasSuffix(f.Path, ".go"):
			if f.Backend != outline.BackendGoAST {
				t.Errorf("%s Backend = %q, want %q", f.Path, f.Backend, outline.BackendGoAST)
			}
		case strings.HasSuffix(f.Path, ".py"):
			if f.Backend != outline.BackendHeuristic {
				t.Errorf("%s Backend = %q, want %q", f.Path, f.Backend, outline.BackendHeuristic)
			}
		}
	}
}

// --- helpers ---

func declNames(decls []outline.Decl) []string {
	names := make([]string, len(decls))
	for i, d := range decls {
		names[i] = d.Name
	}
	return names
}

func sliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Ensure tools import is used.
var _ = tools.ErrCtagsUnavailable

// A Python method is kind "member" in universal ctags, and a C++ data member is
// the same kind in the same scope kind; with real ctags the Python methods are
// reported and the C++ data members are not.
func TestOutlineManyWithRealCtagsPythonMethods(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: ctags process handling differs")
	}
	bin, err := exec.LookPath("ctags")
	if err != nil {
		t.Skip("ctags not on PATH; skipping real ctags test")
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || !strings.Contains(string(out), "Universal Ctags") {
		t.Skip("ctags is not Universal Ctags; skipping")
	}

	root := t.TempDir()
	pySrc := `class Outer:
    attr = 1

    class Inner:
        def inner_method(self):
            pass

    @staticmethod
    def static_one():
        pass

    async def amethod(self):
        def nested_in_method():
            pass
        return nested_in_method

def top():
    class LocalClass:
        def local_class_method(self):
            pass
    return LocalClass
`
	cppSrc := `class Widget {
public:
    int size;
};
struct Pair { int a; int b; };
void Widget_draw() {}
`
	pyFile := filepath.Join(root, "svc.py")
	cppFile := filepath.Join(root, "thing.cpp")
	if err := os.WriteFile(pyFile, []byte(pySrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cppFile, []byte(cppSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	files, _, err := outline.OutlineMany(context.Background(),
		[]outline.Source{{Abs: pyFile}, {Abs: cppFile}},
		outline.Options{Root: root, Runner: tools.CtagsRunner(nil)})
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}

	py := map[string]outline.Decl{}
	for _, d := range files[0].Decls {
		py[d.Name] = d
	}
	for name, container := range map[string]string{
		"inner_method": "Outer.Inner", "static_one": "Outer", "amethod": "Outer",
	} {
		d, ok := py[name]
		if !ok {
			t.Errorf("Python method %q is missing: %v", name, declNames(files[0].Decls))
			continue
		}
		if d.Kind != outline.KindMethod || d.Container != container {
			t.Errorf("%s: Kind=%q Container=%q, want method in %q", name, d.Kind, d.Container, container)
		}
	}
	for _, local := range []string{"nested_in_method", "LocalClass", "local_class_method"} {
		if _, ok := py[local]; ok {
			t.Errorf("%q is declared inside a function and was reported", local)
		}
	}

	cppNames := declNames(files[1].Decls)
	for _, field := range []string{"size", "a", "b"} {
		if sliceContains(cppNames, field) {
			t.Errorf("C++ data member %q was reported: %v", field, cppNames)
		}
	}
	if !sliceContains(cppNames, "Widget") || !sliceContains(cppNames, "Pair") {
		t.Errorf("C++ types are missing: %v", cppNames)
	}
}

// Issue #73 §1, §3 and §5 against real universal ctags: methods in the
// languages whose parser says "method" carry their type, and a language the
// table used to gate out is outlined.
func TestOutlineManyWithRealCtagsMethodContainers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows: ctags process handling differs")
	}
	bin, err := exec.LookPath("ctags")
	if err != nil {
		t.Skip("ctags not on PATH; skipping real ctags test")
	}
	out, err := exec.Command(bin, "--version").Output()
	if err != nil || !strings.Contains(string(out), "Universal Ctags") {
		t.Skip("ctags is not Universal Ctags; skipping")
	}

	root := t.TempDir()
	srcs := map[string]string{
		"Shape.java": "public abstract class Shape {\n  public abstract double area();\n}\n",
		"runner.rb":  "module M\n  class Runner\n    def run; end\n    def self.helper; end\n  end\nend\n",
		"lib.rs":     "pub trait Tr { fn t(&self); }\npub struct S;\nimpl S { pub async fn run(&self) {} }\n",
		"m.ex":       "defmodule M do\n  def f(x), do: x\nend\n",
		"api.proto":  "syntax = \"proto3\";\nmessage Req { int32 a = 1; }\nservice Api { rpc Call(Req) returns (Req); }\n",
	}
	names := []string{"Shape.java", "runner.rb", "lib.rs", "m.ex", "api.proto"}
	var sources []outline.Source
	for _, n := range names {
		p := filepath.Join(root, n)
		if err := os.WriteFile(p, []byte(srcs[n]), 0o644); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, outline.Source{Abs: p})
	}
	files, _, err := outline.OutlineMany(context.Background(), sources,
		outline.Options{Root: root, Runner: tools.CtagsRunner(nil)})
	if err != nil {
		t.Fatalf("OutlineMany: %v", err)
	}
	type want struct {
		kind      outline.Kind
		container string
	}
	expect := []map[string]want{
		{"Shape": {outline.KindClass, ""}, "area": {outline.KindMethod, "Shape"}},
		{"M": {outline.KindModule, ""}, "Runner": {outline.KindClass, ""},
			"run": {outline.KindMethod, "M.Runner"}, "helper": {outline.KindMethod, "M.Runner"}},
		{"Tr": {outline.KindTrait, ""}, "t": {outline.KindMethod, "Tr"}, "run": {outline.KindMethod, "S"}},
		{"M": {outline.KindModule, ""}, "f": {outline.KindFunc, ""}},
		{"Req": {outline.KindType, ""}, "Api": {outline.KindInterface, ""}, "Call": {outline.KindMethod, "Api"}},
	}
	for i, f := range files {
		if f.Backend != outline.BackendCtags {
			t.Errorf("%s: backend %v, want ctags", names[i], f.Backend)
			continue
		}
		got := map[string]outline.Decl{}
		for _, d := range f.Decls {
			got[d.Name] = d
		}
		for name, w := range expect[i] {
			d, ok := got[name]
			if !ok || d.Kind != w.kind || d.Container != w.container {
				t.Errorf("%s %s = %+v (present=%v), want %s in %q", names[i], name, d, ok, w.kind, w.container)
			}
		}
	}
}
