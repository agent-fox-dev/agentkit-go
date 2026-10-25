package outline

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

func TestOutlinePathIsRelativeToRootOrAbsolute(t *testing.T) {
	root := testutil.Tree(t, map[string]string{"sub/dir/a.go": "package a\n"})
	abs := filepath.Join(root, "sub", "dir", "a.go")
	for opts, want := range map[Options]string{{Root: root}: "sub/dir/a.go", {}: filepath.ToSlash(abs)} {
		f, err := Outline(context.Background(), abs, nil, opts)
		if err != nil || f.Path != want {
			t.Errorf("Outline(Root %q).Path = %q, %v; want %q", opts.Root, f.Path, err, want)
		}
	}
}

func TestOutlineOfAnUnknownExtensionReadsNothing(t *testing.T) {
	f, err := Outline(context.Background(), "/nonexistent/foo.xyz", nil, Options{})
	if err != nil || f.Lang != "" || !isNone(f) {
		t.Fatalf("Outline(foo.xyz) = %+v, %v; want no language, none, no error", f, err)
	}
}

func TestOutlineReadsTheFileOnlyWhenNoSourceIsGiven(t *testing.T) {
	abs := filepath.Join(testutil.Tree(t, map[string]string{"a.go": "package a\n\nfunc OnDisk() {}\n"}), "a.go")
	for src, want := range map[string]string{"": "OnDisk", "package a\n\nfunc InMemory() {}\n": "InMemory"} {
		var b []byte
		if src != "" {
			b = []byte(src)
		}
		f, err := Outline(context.Background(), abs, b, Options{})
		if err != nil || len(f.Decls) != 1 || f.Decls[0].Name != want {
			t.Errorf("Outline with src %q = %v, %v; want only %s", src, shows(f.Decls), err, want)
		}
	}
}

// A file over the limit, on disk or given, is none; MaxFileBytes 0 means
// 1 MiB, and a file of exactly the limit is outlined.
func TestOutlineOfAFileOverTheSizeLimitIsNone(t *testing.T) {
	sized := func(n int) string {
		s := "package a\n\nfunc F() {}\n//"
		return s + strings.Repeat("x", n-len(s))
	}
	root := testutil.Tree(t, map[string]string{"exact.go": sized(500), "over.go": sized(501), "mib.go": sized(1<<20 + 1)})
	for _, c := range []struct {
		name  string
		max   int64
		given bool
		none  bool
	}{
		{"exact.go", 500, false, false}, {"over.go", 500, false, true}, {"over.go", 500, true, true},
		{"mib.go", 0, false, true}, {"over.go", 0, false, false},
	} {
		abs := filepath.Join(root, c.name)
		var src []byte
		if c.given {
			src, _ = os.ReadFile(abs)
		}
		f, err := Outline(context.Background(), abs, src, Options{MaxFileBytes: c.max})
		if err != nil || isNone(f) != c.none || f.Lang != LangGo {
			t.Errorf("%s with limit %d (given %v) = %s %v, %v; want none %v", c.name, c.max, c.given, f.Backend, shows(f.Decls), err, c.none)
		}
	}
	// Over the limit the file is not read at all: a directory would fail to.
	dir := filepath.Join(t.TempDir(), "big.py")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if f, err := Outline(context.Background(), dir, nil, Options{MaxFileBytes: 1}); err != nil || !isNone(f) {
		t.Errorf("Outline of an over-limit entry = %+v, %v; want none without reading it", f, err)
	}
}

func TestOutlineOfABinaryFileIsNone(t *testing.T) {
	early := []byte("package a\n\nfunc F() {}\n")
	early = append(early, 0)
	late := []byte("package a\n\nfunc F() {}\n//" + strings.Repeat("x", binarySniffSize) + "\x00\n")
	if f := outlineSrc(t, "early.go", string(early)); !isNone(f) || f.Lang != LangGo {
		t.Errorf("a NUL in the first 8 KiB: %s %v, want none", f.Backend, shows(f.Decls))
	}
	if f := outlineSrc(t, "late.go", string(late)); f.Backend != BackendGoAST {
		t.Errorf("a NUL after the first 8 KiB: %s, want it outlined", f.Backend)
	}
}

func TestOutlineOfAnUnreadableFileIsAnError(t *testing.T) {
	for _, abs := range []string{"/nonexistent/missing.py", t.TempDir() + "/missing.go"} {
		if f, err := Outline(context.Background(), abs, nil, Options{}); err == nil || f.Decls != nil || f.Path != "" {
			t.Errorf("Outline(%s) = %+v, %v; want the zero File and an error", abs, f, err)
		}
	}
}

func TestOutlineOfACancelledContextIsItsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Outline(ctx, "/fake/a.go", []byte("package a\n"), Options{}); err != context.Canceled {
		t.Errorf("Outline: err = %v, want context.Canceled", err)
	}
	if fs, err := OutlineMany(ctx, []Source{{Abs: "/fake/a.go", Src: []byte("package a\n")}}, Options{}); err != context.Canceled || fs != nil {
		t.Errorf("OutlineMany = %v, %v; want nil, context.Canceled", fs, err)
	}
}

// A Go file the parser returns no syntax tree for is none.
func TestOutlineOfAGoFileWithNoSyntaxTreeIsNone(t *testing.T) {
	saved := parseGo
	t.Cleanup(func() { parseGo = saved })
	parseGo = func(*token.FileSet, string, any, parser.Mode) (*ast.File, error) { return nil, os.ErrInvalid }
	if f := outlineSrc(t, "a.go", "package a\n\nfunc F() {}\n"); !isNone(f) || f.Lang != LangGo {
		t.Errorf("Outline = %+v, want Go, none", f)
	}
}

// OutlineMany returns one File per input, in order, an unreadable one as
// none.
func TestOutlineManyKeepsTheInputOrder(t *testing.T) {
	dir := testutil.Tree(t, map[string]string{
		"a.go": "package a\nfunc A() {}\n", "c.unknown": "x", "d.go": "package d\nfunc D() {}\n",
	})
	var srcs []Source
	for _, name := range []string{"a.go", "missing.py", "c.unknown", "d.go", "given.go"} {
		srcs = append(srcs, Source{Abs: filepath.Join(dir, name)})
	}
	srcs[4].Src = []byte("package g\nfunc G() {}\n")
	files, err := OutlineMany(context.Background(), srcs, Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		got = append(got, f.Path+" "+f.Lang+" "+string(f.Backend)+" "+strings.Join(shows(f.Decls), ","))
	}
	want := []string{
		"a.go Go go/ast func A L2-2", "missing.py Python none ", "c.unknown  none ",
		"d.go Go go/ast func D L2-2", "given.go Go go/ast func G L2-2",
	}
	if !slices.Equal(got, want) {
		t.Errorf("OutlineMany =\n%q\nwant\n%q", got, want)
	}
}
