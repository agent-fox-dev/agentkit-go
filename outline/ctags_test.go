package outline

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// closedKindSet is the set of valid Kind values.
var closedKindSet = map[Kind]bool{
	KindFunc:      true,
	KindMethod:    true,
	KindType:      true,
	KindClass:     true,
	KindInterface: true,
	KindEnum:      true,
	KindTrait:     true,
	KindConst:     true,
	KindVar:       true,
	KindModule:    true,
	KindMacro:     true,
}

// TS-01-2: Every Decl from any backend carries a Kind from the closed set.
func TestEveryDeclKindInClosedSet_TS_01_2(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// --- Go sources with locals, struct fields, imports and labels ---
	goSrc := []byte(`package sample

import "fmt"

func Top() {
	localVar := 1
	_ = localVar
	type localType struct{}
	_ = localType{}
	goto done
done:
	fmt.Println("done")
}

type MyStruct struct {
	Field1 int
	Field2 string
}

func (m *MyStruct) Method() {}

const C = 42
var V int
`)
	goFile := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(goFile, goSrc, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Outline(ctx, goFile, nil, Options{})
	if err != nil {
		t.Fatalf("Go Outline error: %v", err)
	}
	for _, d := range f.Decls {
		if !closedKindSet[d.Kind] {
			t.Errorf("Go backend: Decl %q has Kind %q not in closed set", d.Name, d.Kind)
		}
	}
	// Verify no locals, fields, imports, or labels appear.
	for _, d := range f.Decls {
		if d.Name == "localVar" || d.Name == "localType" || d.Name == "done" ||
			d.Name == "Field1" || d.Name == "Field2" || d.Name == "fmt" {
			t.Errorf("Go backend: unexpected Decl %q (should be filtered)", d.Name)
		}
	}

	// --- Fake ctags output with kinds that should be dropped ---
	pyFile := filepath.Join(dir, "sample.py")
	if err := os.WriteFile(pyFile, []byte("def hello():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctagsOutput := strings.Join([]string{
		fmt.Sprintf(`{"_type":"tag","name":"hello","path":%q,"line":1,"kind":"function"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"myfield","path":%q,"line":2,"kind":"field"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"loc","path":%q,"line":3,"kind":"local"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"param","path":%q,"line":4,"kind":"parameter"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"val","path":%q,"line":5,"kind":"enumerator"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"lbl","path":%q,"line":6,"kind":"label"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"imp","path":%q,"line":7,"kind":"import"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"mem","path":%q,"line":8,"kind":"member"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"unkn","path":%q,"line":9,"kind":"xyzzy"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(ctagsOutput), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("ctags OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}
	for _, d := range fs[0].Decls {
		if !closedKindSet[d.Kind] {
			t.Errorf("ctags backend: Decl %q has Kind %q not in closed set", d.Name, d.Kind)
		}
	}
	// Only "hello" should survive.
	droppedNames := map[string]bool{
		"myfield": true, "loc": true, "param": true,
		"val": true, "lbl": true, "imp": true, "mem": true, "unkn": true,
	}
	for _, d := range fs[0].Decls {
		if droppedNames[d.Name] {
			t.Errorf("ctags backend: Decl %q should have been dropped", d.Name)
		}
	}
	if len(fs[0].Decls) != 1 || fs[0].Decls[0].Name != "hello" {
		t.Errorf("ctags backend: expected only [hello], got %v", declNames(fs[0].Decls))
	}
}

// TS-01-3: Only top-level declarations and methods of a declared type are kept,
// with 1-based inclusive lines.
func TestTopLevelAndMethodsOnly_TS_01_3(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// Go file with a top-level func, a method, a func-local var and type, a struct field.
	goSrc := []byte(`package sample

func Top() {
	localVar := 1
	_ = localVar
}

type MyType struct {
	Field int
}

func (m *MyType) Method() int {
	return 0
}
`)
	goFile := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(goFile, goSrc, 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := Outline(ctx, goFile, nil, Options{})
	if err != nil {
		t.Fatalf("Go Outline error: %v", err)
	}

	names := declNames(f.Decls)
	// Top and Method and MyType should be present.
	if !sliceContains(names, "Top") {
		t.Errorf("expected Top in decls, got %v", names)
	}
	if !sliceContains(names, "Method") {
		t.Errorf("expected Method in decls, got %v", names)
	}
	if !sliceContains(names, "MyType") {
		t.Errorf("expected MyType in decls, got %v", names)
	}
	// localVar, Field should NOT be present.
	if sliceContains(names, "localVar") {
		t.Errorf("localVar should not be in decls, got %v", names)
	}
	if sliceContains(names, "Field") {
		t.Errorf("Field should not be in decls, got %v", names)
	}

	// Check StartLine and EndLine for Top.
	dm := declMap(f.Decls)
	topDecl := dm["Top"]
	if topDecl.StartLine != 3 {
		t.Errorf("Top.StartLine = %d, want 3", topDecl.StartLine)
	}
	if topDecl.EndLine != 6 {
		t.Errorf("Top.EndLine = %d, want 6", topDecl.EndLine)
	}

	methodDecl := dm["Method"]
	if methodDecl.StartLine != 12 {
		t.Errorf("Method.StartLine = %d, want 12", methodDecl.StartLine)
	}
	if methodDecl.EndLine != 14 {
		t.Errorf("Method.EndLine = %d, want 14", methodDecl.EndLine)
	}

	// --- ctags fake with a tag whose scope is a function (a local) ---
	pyFile := filepath.Join(dir, "sample.py")
	if err := os.WriteFile(pyFile, []byte("def outer():\n    def inner():\n        pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctagsOutput := strings.Join([]string{
		fmt.Sprintf(`{"_type":"tag","name":"outer","path":%q,"line":1,"kind":"function"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"inner","path":%q,"line":2,"kind":"function","scope":"outer","scopeKind":"function"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(ctagsOutput), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("ctags OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}
	// Only outer should be present; inner is a local (scoped in a function).
	ctagsNames := declNames(fs[0].Decls)
	if !sliceContains(ctagsNames, "outer") {
		t.Errorf("expected outer in ctags decls, got %v", ctagsNames)
	}
	if sliceContains(ctagsNames, "inner") {
		t.Errorf("inner should not be in ctags decls (it's a local), got %v", ctagsNames)
	}

	// A backend that cannot tell end yields EndLine 0.
	// The ctags tag for "outer" has no "end" field, so EndLine should be 0.
	if fs[0].Decls[0].EndLine != 0 {
		t.Errorf("outer.EndLine = %d, want 0 (no end field)", fs[0].Decls[0].EndLine)
	}
}

// TS-01-23: When the parser yields no AST, the file falls through to the next
// backend without an error.
func TestGoParserNoAST_TS_01_23(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	goFile := filepath.Join(dir, "a.go")
	goSrc := []byte("package a\n\nfunc Hello() {}\n")
	if err := os.WriteFile(goFile, goSrc, 0o644); err != nil {
		t.Fatal(err)
	}

	// Save original parseGo and replace with a stub returning nil.
	origParseGo := parseGo
	defer func() { parseGo = origParseGo }()

	parseGo = func(fset *token.FileSet, filename string, src any, mode parser.Mode) (*ast.File, error) {
		return nil, fmt.Errorf("simulated parse failure")
	}

	// Case 1: with a fake Runner returning a valid tag for the .go file.
	ctagsOutput := fmt.Sprintf(
		`{"_type":"tag","name":"Hello","path":%q,"line":3,"kind":"function"}`+"\n",
		goFile,
	)
	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(ctagsOutput), nil
	}

	f, err := Outline(ctx, goFile, goSrc, Options{Runner: runner})
	if err != nil {
		t.Fatalf("with Runner: unexpected error: %v", err)
	}
	if f.Backend != BackendCtags {
		t.Errorf("with Runner: Backend = %q, want %q", f.Backend, BackendCtags)
	}
	if len(f.Decls) == 0 {
		t.Error("with Runner: expected at least one Decl")
	}

	// Case 2: without a Runner.
	f2, err := Outline(ctx, goFile, goSrc, Options{})
	if err != nil {
		t.Fatalf("without Runner: unexpected error: %v", err)
	}
	if f2.Backend != BackendNone {
		t.Errorf("without Runner: Backend = %q, want %q", f2.Backend, BackendNone)
	}
	if f2.Decls == nil {
		t.Error("without Runner: Decls is nil, want non-nil empty slice")
	}
	if len(f2.Decls) != 0 {
		t.Errorf("without Runner: expected 0 Decls, got %d", len(f2.Decls))
	}
	if f2.Lang != LangGo {
		t.Errorf("without Runner: Lang = %q, want %q", f2.Lang, LangGo)
	}
}

// TS-01-32: ctags kinds are normalised into the closed set, struct-like kinds
// to type, others dropped.
func TestCtagsKindNormalisation_TS_01_32(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "sample.py")
	if err := os.WriteFile(pyFile, []byte(strings.Repeat("x\n", 20)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Build tags with all the kinds we want to test.
	type testTag struct {
		name     string
		kind     string
		wantKind Kind
		wantDrop bool
	}
	tags := []testTag{
		{"fn1", "function", KindFunc, false},
		{"cls1", "class", KindClass, false},
		{"st1", "struct", KindType, false},
		{"iface1", "interface", KindInterface, false},
		{"en1", "enum", KindEnum, false},
		{"tr1", "trait", KindTrait, false},
		{"v1", "variable", KindVar, false},
		{"c1", "constant", KindConst, false},
		{"mod1", "module", KindModule, false},
		{"mac1", "macro", KindMacro, false},
		// Aliases
		{"td1", "typedef", KindType, false},
		{"un1", "union", KindType, false},
		{"ns1", "namespace", KindModule, false},
		{"pkg1", "package", KindModule, false},
		{"def1", "define", KindMacro, false},
		// Dropped kinds
		{"mem1", "member", "", true},
		{"fld1", "field", "", true},
		{"loc1", "local", "", true},
		{"enm1", "enumerator", "", true},
		{"lbl1", "label", "", true},
		{"imp1", "import", "", true},
		{"par1", "parameter", "", true},
	}

	var lines []string
	for i, tt := range tags {
		lines = append(lines, fmt.Sprintf(
			`{"_type":"tag","name":%q,"path":%q,"line":%d,"kind":%q}`,
			tt.name, pyFile, i+1, tt.kind,
		))
	}
	output := strings.Join(lines, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}

	dm := declMap(fs[0].Decls)

	for _, tt := range tags {
		d, found := dm[tt.name]
		if tt.wantDrop {
			if found {
				t.Errorf("tag %q (kind %q) should have been dropped, but found with Kind %q",
					tt.name, tt.kind, d.Kind)
			}
			continue
		}
		if !found {
			t.Errorf("tag %q (kind %q) not found, expected Kind %q", tt.name, tt.kind, tt.wantKind)
			continue
		}
		if d.Kind != tt.wantKind {
			t.Errorf("tag %q (kind %q): got Kind %q, want %q", tt.name, tt.kind, d.Kind, tt.wantKind)
		}
	}
}

// TS-01-33: A function tag scoped in a type-like kind becomes a method with
// the scope as Container.
func TestCtagsFunctionScopeBecomesMethod_TS_01_33(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "sample.py")
	if err := os.WriteFile(pyFile, []byte(strings.Repeat("x\n", 10)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Function tags with various scope kinds.
	lines := []string{
		fmt.Sprintf(`{"_type":"tag","name":"m1","path":%q,"line":1,"kind":"function","scope":"Foo","scopeKind":"class"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"m2","path":%q,"line":2,"kind":"function","scope":"Bar","scopeKind":"struct"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"m3","path":%q,"line":3,"kind":"function","scope":"Baz","scopeKind":"interface"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"m4","path":%q,"line":4,"kind":"function","scope":"Qux","scopeKind":"enum"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"m5","path":%q,"line":5,"kind":"function","scope":"Quux","scopeKind":"trait"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"m6","path":%q,"line":6,"kind":"function","scope":"Corge","scopeKind":"impl"}`, pyFile),
		// A function scoped in a function — should be dropped as a local.
		fmt.Sprintf(`{"_type":"tag","name":"local1","path":%q,"line":7,"kind":"function","scope":"outer","scopeKind":"function"}`, pyFile),
	}
	output := strings.Join(lines, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}

	dm := declMap(fs[0].Decls)

	// Type-scoped tags should be methods.
	for _, name := range []string{"m1", "m2", "m3", "m4", "m5", "m6"} {
		d, ok := dm[name]
		if !ok {
			t.Errorf("tag %q not found", name)
			continue
		}
		if d.Kind != KindMethod {
			t.Errorf("tag %q: Kind = %q, want %q", name, d.Kind, KindMethod)
		}
		if d.Container == "" {
			t.Errorf("tag %q: Container is empty, want non-empty", name)
		}
	}

	// Check specific containers.
	if dm["m1"].Container != "Foo" {
		t.Errorf("m1.Container = %q, want %q", dm["m1"].Container, "Foo")
	}
	if dm["m6"].Container != "Corge" {
		t.Errorf("m6.Container = %q, want %q", dm["m6"].Container, "Corge")
	}

	// Function-scoped tag should be dropped.
	if _, ok := dm["local1"]; ok {
		t.Error("local1 should have been dropped (function-scoped function)")
	}
}

// TS-01-34: EndLine comes from the ctags end field when present, otherwise 0.
func TestCtagsEndLine_TS_01_34(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "sample.py")
	if err := os.WriteFile(pyFile, []byte("def a():\n    pass\ndef b():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := strings.Join([]string{
		fmt.Sprintf(`{"_type":"tag","name":"a","path":%q,"line":1,"kind":"function","end":12}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"b","path":%q,"line":3,"kind":"function"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}

	dm := declMap(fs[0].Decls)

	if dm["a"].EndLine != 12 {
		t.Errorf("a.EndLine = %d, want 12", dm["a"].EndLine)
	}
	if dm["b"].EndLine != 0 {
		t.Errorf("b.EndLine = %d, want 0", dm["b"].EndLine)
	}
}

// TS-01-35: Signature is the trimmed source line at StartLine, with ctags
// signature only as fallback.
func TestCtagsSignature_TS_01_35(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "sample.py")
	// Line 1: empty, Line 2: empty, Line 3: "    def foo(a, b):  "
	src := "# line 1\n# line 2\n    def foo(a, b):  \n"
	if err := os.WriteFile(pyFile, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	output := strings.Join([]string{
		// Tag at line 3 with a ctags signature field — source line should be preferred.
		fmt.Sprintf(`{"_type":"tag","name":"foo","path":%q,"line":3,"kind":"function","signature":"(x)"}`, pyFile),
		// Tag at line 99 (beyond end of source) — ctags signature field should be used.
		fmt.Sprintf(`{"_type":"tag","name":"bar","path":%q,"line":99,"kind":"function","signature":"(x)"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}

	dm := declMap(fs[0].Decls)

	// foo: source line at line 3, trimmed.
	if dm["foo"].Signature != "def foo(a, b):" {
		t.Errorf("foo.Signature = %q, want %q", dm["foo"].Signature, "def foo(a, b):")
	}

	// bar: line 99 is out of range, so ctags signature field is used.
	if dm["bar"].Signature != "(x)" {
		t.Errorf("bar.Signature = %q, want %q", dm["bar"].Signature, "(x)")
	}
}

// TS-01-36: ctags Decls are exported unless the name begins with an underscore,
// and the File Backend is ctags.
func TestCtagsExportedAndBackend_TS_01_36(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	pyFile := filepath.Join(dir, "sample.py")
	if err := os.WriteFile(pyFile, []byte("x\nx\nx\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	output := strings.Join([]string{
		fmt.Sprintf(`{"_type":"tag","name":"Public","path":%q,"line":1,"kind":"function"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"_private","path":%q,"line":2,"kind":"function"}`, pyFile),
		fmt.Sprintf(`{"_type":"tag","name":"__dunder__","path":%q,"line":3,"kind":"function"}`, pyFile),
	}, "\n") + "\n"

	runner := func(_ context.Context, _ []string) ([]byte, error) {
		return []byte(output), nil
	}

	fs, _, err := OutlineMany(ctx, []Source{{Abs: pyFile}}, Options{Runner: runner})
	if err != nil {
		t.Fatalf("OutlineMany error: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("expected 1 file, got %d", len(fs))
	}

	// Backend should be ctags.
	if fs[0].Backend != BackendCtags {
		t.Errorf("Backend = %q, want %q", fs[0].Backend, BackendCtags)
	}

	dm := declMap(fs[0].Decls)

	if !dm["Public"].Exported {
		t.Error("Public should be Exported=true")
	}
	if dm["_private"].Exported {
		t.Error("_private should be Exported=false")
	}
	if dm["__dunder__"].Exported {
		t.Error("__dunder__ should be Exported=false")
	}
}
