package outline

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
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

func TestABrokenGoFileStillYieldsWhatParses(t *testing.T) {
	f := outlineSrc(t, "broken.go", "package a\n\nfunc A() {}\n\nfunc broken( {\n}\n\nfunc B() {}\n")
	if f.Backend != BackendGoAST || len(f.Decls) == 0 || f.Decls[0].Name != "A" {
		t.Errorf("Outline(broken.go) = %s %v, want go/ast starting with A", f.Backend, shows(f.Decls))
	}
}
