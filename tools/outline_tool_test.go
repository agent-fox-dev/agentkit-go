package tools

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

const outlineSrc = "package x\n\ntype Observer interface {\n\tOnStart()\n}\n\nfunc (r *Runner) Run() error {\n\treturn nil\n}\n\nconst MaxRetries = 3\n\nfunc internal() {}\n"

func TestFileOutlineListsDeclarationsWithLineRanges(t *testing.T) {
	ws := newWS(t, map[string]string{
		"x.go": outlineSrc, "empty.go": "package empty\n", "one.go": "package one\n\nfunc One() {}\n",
		".gitignore": "gen/\n", "gen/g.go": "package gen\n\nfunc G() {}\n", ".hidden/h.go": "package h\n\nfunc H() {}\n",
	})
	// Neither building the tool set nor outlining a file walks the workspace.
	walks := countWalks(t)
	fo := toolSet(t, ws, Options{})["file_outline"]
	defer func() {
		if n := walks.Load(); n != 0 {
			t.Errorf("%d walks", n)
		}
	}()
	for args, want := range map[string]string{
		`{"path":"x.go"}`: "x.go  (go/ast, 4 declarations, 1 unexported not listed)\n" +
			"  L3-5  type Observer interface\n  L7-9  func (r *Runner) Run() error\n  L11  const MaxRetries",
		`{"path":"x.go","include_private":true}`: "x.go  (go/ast, 4 declarations)\n" +
			"  L3-5  type Observer interface\n  L7-9  func (r *Runner) Run() error\n  L11  const MaxRetries\n  L13  func internal()",
		`{"path":"one.go"}`:   "one.go  (go/ast, 1 declaration)\n  L3  func One()",
		`{"path":"empty.go"}`: "empty.go  (go/ast, 0 declarations)\n  No declarations found",
		// Ignored and hidden files are outlined anyway: the caller named them.
		`{"path":"gen/g.go"}`:     "gen/g.go  (go/ast, 1 declaration)\n  L3  func G()",
		`{"path":".hidden/h.go"}`: ".hidden/h.go  (go/ast, 1 declaration)\n  L3  func H()",
	} {
		if r := mustOK(t, call(fo, args)); r.Text != want {
			t.Errorf("%s:\n%s\nwant:\n%s", args, r.Text, want)
		}
	}
	r := mustOK(t, call(fo, `{"path":"x.go"}`))
	if f, ok := r.Data["file"].(outline.File); !ok || len(f.Decls) != 4 || r.Data["declarations"] != 4 || r.Data["listed"] != 3 || r.Data["backend"] != "go/ast" {
		t.Errorf("Data = %+v", r.Data)
	}
	// Read from disk on every call, never from the symbol table.
	writeAt(t, ws.Root(), "one.go", "package one\n\nfunc Two() {}\n")
	if r := mustOK(t, call(fo, `{"path":"one.go"}`)); !strings.Contains(r.Text, "Two") || strings.Contains(r.Text, "One()") {
		t.Errorf("a stale outline: %s", r.Text)
	}
}

func TestFileOutlineRejections(t *testing.T) {
	outsideDir := t.TempDir()
	writeAt(t, outsideDir, "o.go", "package o\n")
	ws := newWS(t, map[string]string{"a.go": "package a\n", "pkg/p.go": "package p\n"})
	mustSymlink(t, outsideDir+"/o.go", abs(ws, "escape.go"))
	fo := newFT(ws, Options{}).fileOutlineTool()
	for args, code := range map[string]string{
		`{`: "invalid_arguments", `{"path":""}`: "invalid_arguments", `{"path":3}`: "invalid_arguments",
		`{"path":"escape.go"}`: "path_not_allowed", `{"path":"` + outsideDir + `/o.go"}`: "path_not_allowed",
		`{"path":"missing.go"}`: "read_failed", `{"path":"pkg"}`: "not_a_file",
	} {
		if r := call(fo, args); r.Error != code {
			t.Errorf("%s: %q %q, want %q", args, r.Error, r.Detail, code)
		}
	}
	if r := call(fo, `{"path":"pkg"}`); !strings.Contains(r.Detail, "list_files") {
		t.Errorf("a directory names list_files: %q", r.Detail)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := fo.Execute(ctx, []byte(`{"path":"a.go"}`)); r.Error != "aborted" || r.Detail != "Operation aborted" {
		t.Errorf("a cancelled call: %+v", r)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	writeAt(t, ws.Root(), "secret.go", "package secret\n")
	_ = os.Chmod(abs(ws, "secret.go"), 0)
	if r := call(fo, `{"path":"secret.go"}`); r.Error != "outline_failed" {
		t.Errorf("an unreadable file: %+v", r)
	}
	// Over outline's size limit the file is not even read: mode 0 proves it.
	f, err := os.Create(abs(ws, "bundle.js"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(2 << 20)
	f.Close()
	_ = os.Chmod(abs(ws, "bundle.js"), 0)
	if r := call(fo, `{"path":"bundle.js"}`); !r.OK || r.Text != "bundle.js  (none, 0 declarations)\n  No declarations found (file too large); use read_file or search_files" {
		t.Errorf("an oversized file: %+v", r)
	}
}

// TestANoneOutlineSaysWhyAndWhatToUseInstead renders outlines with no
// backend, which in a build without cgo is every language but Go.
func TestANoneOutlineSaysWhyAndWhatToUseInstead(t *testing.T) {
	ws := newWS(t, map[string]string{"data.xyz": "some data\n", "bin.go": "package x\n\x00\x00"})
	fo := newFT(ws, Options{}).fileOutlineTool()
	for path, reason := range map[string]string{"data.xyz": "language not recognised", "bin.go": "binary"} {
		want := path + "  (none, 0 declarations)\n  No declarations found (" + reason + "); use read_file or search_files"
		if r := mustOK(t, call(fo, `{"path":"`+path+`"}`)); r.Text != want {
			t.Errorf("%s:\n%s\nwant:\n%s", path, r.Text, want)
		}
	}
	src := []byte("<?php\nfunction hello() {}\n")
	r := renderOutlineResult(ws, abs(ws, "a.php"), outline.File{Lang: outline.LangPHP, Backend: outline.BackendNone}, false, src, int64(len(src)))
	if want := "a.php  (none, 0 declarations)\n  No declarations found (no outline backend for PHP in this build (built without cgo)); use read_file or search_files"; r.Text != want {
		t.Errorf("got:\n%s\nwant:\n%s", r.Text, want)
	}
}

func TestAMemberWhoseSignatureLacksItsContainerIsIndented(t *testing.T) {
	ws := newWS(t, nil)
	f := outline.File{Lang: "Python", Backend: outline.BackendTreeSitter, Decls: []outline.Decl{
		{Kind: outline.KindClass, Name: "MyClass", Signature: "class MyClass", Exported: true, StartLine: 1, EndLine: 6},
		{Kind: outline.KindMethod, Name: "my_method", Container: "MyClass", Signature: "def my_method(self)", Exported: true, StartLine: 2, EndLine: 3},
		{Kind: outline.KindMethod, Name: "Run", Container: "Runner", Signature: "func (r *Runner) Run()", Exported: true, StartLine: 7},
		{Kind: outline.KindFunc, Name: "top", Signature: "def top()", Exported: true, StartLine: 5, EndLine: 5},
	}}
	want := "mod.py  (tree-sitter, 4 declarations)\n  L1-6  class MyClass\n    L2-3  def my_method(self)\n  L7  func (r *Runner) Run()\n  L5  def top()"
	if r := renderOutlineResult(ws, abs(ws, "mod.py"), f, false, nil, 0); r.Text != want {
		t.Errorf("got:\n%s\nwant:\n%s", r.Text, want)
	}
	for n, want := range map[int]string{0: "0", 7: "7", 1234: "1234", -42: "-42"} {
		if got := itoa(n); got != want {
			t.Errorf("itoa(%d) = %q", n, got)
		}
	}
}
