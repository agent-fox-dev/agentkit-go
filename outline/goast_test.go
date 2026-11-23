package outline

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

// The fixture is read from disk, outlined in-process, and its declarations
// come out sorted, with doc comments outside their ranges, one-line
// signatures and Go's export rule.
func TestOutlineOfAGoFile(t *testing.T) {
	root, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "") // nothing to start
	f, err := Outline(context.Background(), filepath.Join(root, "go_sample.go"), nil, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "go_sample.go" || f.Lang != LangGo || f.Backend != BackendGoAST {
		t.Fatalf("File = %q %q %q", f.Path, f.Lang, f.Backend)
	}
	assertParsedClean(t, f)
	want := [][2]string{
		{"func Greet L4-6", "func Greet(name string) string"},
		{"type Server L8-10", "type Server struct"},
		{"method Server.Start L13-15", "func (s *Server) Start() error"},
		{"interface Handler L17-19", "type Handler interface"},
		{"type Config L21-21", "type Config map[string]string"},
		{"type Alias L23-23", "type Alias = []byte"},
		{"const MaxRetries L26-26", "const MaxRetries"},
		{"const minDelay L27-27 -", "const minDelay"},
		{"var DefaultAddr L31-31", "var DefaultAddr string"},
		{"var verbose L32-32 -", "var verbose bool"},
	}
	var got [][2]string
	for _, d := range f.Decls {
		got = append(got, [2]string{show(d), d.Signature})
	}
	if !slices.Equal(got, want) {
		t.Errorf("decls:\n got %q\nwant %q", got, want)
	}
}

func TestGoDeclarationsHaveTheirKindContainerAndRange(t *testing.T) {
	f := outlineSrc(t, "a.go", `package a

func F() {}
func _() {}

type T struct{}
func (t *T) M() {}
func (T) N() {}
func ((T)) O() {}

type G[K comparable, V any] struct{}
func (g *G[K, V]) P() {}
type H[K any] struct{}
func (h H[K]) Q() {}

type I interface{ M() }
type D int
type (
	// Doc for S.
	S struct {
		X int
	}
	_ int
)

const (
	A1 = 1
	_  = 2
)
var b, a int
var (
	// Doc for X.
	X int
)
var _ = 3
func Ωmega() {}
func αlpha() {}
`)
	want := []string{
		"func F L3-3",
		"type T L6-6", "method T.M L7-7", "method T.N L8-8", "method T.O L9-9",
		"type G L11-11", "method G.P L12-12", "type H L13-13", "method H.Q L14-14",
		"interface I L16-16", "type D L17-17", "type S L20-22",
		"const A1 L27-27",
		"var a L30-30 -", "var b L30-30 -", "var X L33-33",
		"func Ωmega L36-36", "func αlpha L37-37 -",
	}
	if got := shows(f.Decls); !slices.Equal(got, want) {
		t.Errorf("decls:\n got %q\nwant %q", got, want)
	}
}

func TestGoSignaturesAreOneLineWithoutTheBody(t *testing.T) {
	f := outlineSrc(t, "a.go", "package a\n\nfunc MultiLine(\n\ta int,\n\tb string,\n) error {\n\treturn nil\n}\n\n"+
		"type P *int\ntype Ch chan<- int\nvar W interface{}\nvar Inferred = 1\nconst Typed int = 2\n")
	want := []string{
		"func MultiLine( a int, b string, ) error", "type P *int", "type Ch chan<- int",
		"var W interface{}", "var Inferred", "const Typed int",
	}
	var got []string
	for _, d := range f.Decls {
		got = append(got, d.Signature)
	}
	if !slices.Equal(got, want) {
		t.Errorf("signatures:\n got %q\nwant %q", got, want)
	}
}

// A damaged file keeps the declarations the parser recovers, exactly as
// before the parse reported its errors.
func TestABrokenGoFileStillYieldsWhatParses(t *testing.T) {
	f := outlineSrc(t, "broken.go", "package a\n\nfunc A() {}\n\nfunc broken( {\n}\n\nfunc B() {}\n")
	if want := []string{"func A L3-3", "func broken L5-0 -"}; f.Backend != BackendGoAST || !slices.Equal(shows(f.Decls), want) {
		t.Errorf("Outline(broken.go) = %s %q, want go/ast %q", f.Backend, shows(f.Decls), want)
	}
}

// manyErrors is a Go file whose function F holds n lines `f(1 2)`, from
// line 6 on: one syntax error on each.
func manyErrors(n int) []byte {
	return []byte("package a\n\nfunc A() {}\n\nfunc F() {\n" + strings.Repeat("\tf(1 2)\n", n) + "}\n\nfunc Z() {}\n")
}

// outlineWith outlines src as the file /x/<name> with opts.
func outlineWith(t *testing.T, name string, src []byte, opts Options) File {
	t.Helper()
	f, err := Outline(context.Background(), filepath.Join("/x", name), src, opts)
	if err != nil {
		t.Fatalf("Outline(%s): %v", name, err)
	}
	return f
}

// Every position go/parser reports an error at is one syntax error, in
// position order: missing when a message there names a token in quotes,
// unexpected otherwise.
func TestGoSyntaxErrorsArePositionsAndKinds(t *testing.T) {
	type e = SyntaxError
	u, m := SyntaxErrorUnexpected, SyntaxErrorMissing
	var lines6to19 []e
	for l := 6; l <= 19; l++ {
		lines6to19 = append(lines6to19, e{l, 6, u})
	}
	for _, c := range []struct {
		name string
		src  []byte
		want []e // the whole list, or, with some, entries it must contain
		some bool
	}{
		{"unclosed function", []byte("package a\n\nfunc F() {\n\tx := 1\n"), []e{{4, 9, m}}, false},
		{"empty file", []byte{}, []e{{1, 1, m}}, false},
		{"errors on fourteen lines", manyErrors(14), lines6to19, false},
		{"two faults on one line", []byte("package a\n\nvar a, b = f(1 2), g(3 4)\n"), []e{{3, 16, u}, {3, 24, u}}, false},
		{"unterminated string", []byte("package a\nvar s = \"abc\n"), []e{{2, 9, u}}, true},
		{"a token or not", []byte("package a\n\nvar x = \n\nfunc G() { y := }\n"), []e{{5, 6, m}, {5, 8, u}}, true},
	} {
		f := outlineWith(t, "a.go", c.src, Options{MaxSyntaxErrors: 50})
		if !f.Parsed || f.Backend != BackendGoAST || f.NotParsed != "" || f.SyntaxErrorsTruncated {
			t.Errorf("%s: Parsed %v, Backend %s, NotParsed %q, truncated %v", c.name, f.Parsed, f.Backend, f.NotParsed, f.SyntaxErrorsTruncated)
		}
		for i := 1; i < len(f.SyntaxErrors); i++ {
			if a, b := f.SyntaxErrors[i-1], f.SyntaxErrors[i]; a.Line > b.Line || (a.Line == b.Line && a.Column >= b.Column) {
				t.Errorf("%s: %v is not in strict position order", c.name, f.SyntaxErrors)
			}
		}
		if c.some {
			for _, w := range c.want {
				if !slices.Contains(f.SyntaxErrors, w) {
					t.Errorf("%s: %v lacks %v", c.name, f.SyntaxErrors, w)
				}
			}
		} else if !slices.Equal(f.SyntaxErrors, c.want) {
			t.Errorf("%s: %v, want %v", c.name, f.SyntaxErrors, c.want)
		}
	}
	// An empty file on disk is the same.
	root := testutil.Tree(t, map[string]string{"empty.go": ""})
	f, err := Outline(context.Background(), filepath.Join(root, "empty.go"), nil, Options{})
	if err != nil || !f.Parsed || !slices.Equal(f.SyntaxErrors, []e{{1, 1, m}}) {
		t.Errorf("empty.go on disk: %+v, %v", f, err)
	}
}

// Errors on more than ten lines no longer stop the parser: the file keeps
// the declarations it recovers.
func TestAGoFileWithErrorsOnManyLinesKeepsItsDeclarations(t *testing.T) {
	f := outlineWith(t, "a.go", manyErrors(14), Options{})
	if want := []string{"func A L3-3", "func F L5-20", "func Z L22-22"}; !slices.Equal(shows(f.Decls), want) {
		t.Errorf("decls %q, want %q", shows(f.Decls), want)
	}
}

// At most MaxSyntaxErrors errors are returned, the first ones; 0 and a
// negative value mean 20, and SyntaxErrorsTruncated says the list was cut.
func TestGoSyntaxErrorsStopAtTheLimit(t *testing.T) {
	for _, c := range []struct {
		n, limit, wantLen int
		truncated         bool
		lastLine          int
	}{
		{20, 0, 20, false, 25}, {21, 0, 20, true, 25}, {21, 50, 21, false, 26}, {21, 3, 3, true, 8},
		{3, 3, 3, false, 8}, {21, -1, 20, true, 25}, {21, -100, 20, true, 25},
	} {
		f := outlineWith(t, "a.go", manyErrors(c.n), Options{MaxSyntaxErrors: c.limit})
		got := f.SyntaxErrors
		if len(got) != c.wantLen || f.SyntaxErrorsTruncated != c.truncated || len(got) == 0 || got[len(got)-1].Line != c.lastLine || got[0].Line != 6 {
			t.Errorf("%d errors, limit %d: %d errors %v, truncated %v; want %d, truncated %v, last on line %d",
				c.n, c.limit, len(got), got, f.SyntaxErrorsTruncated, c.wantLen, c.truncated, c.lastLine)
		}
	}
}
