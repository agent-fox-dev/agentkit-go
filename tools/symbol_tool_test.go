package tools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestFindSymbolRejectsBadArgumentsWithoutWalking(t *testing.T) {
	outside := t.TempDir()
	ws := newWS(t, map[string]string{"a.go": "package main\n\nfunc Hello() {}\n"})
	fs := newFT(ws, Options{}).findSymbolTool()
	walks := countWalks(t)
	for args, want := range map[string]string{
		`{bad json`:      "invalid_arguments",
		`{"name":""}`:    "invalid_arguments: name is required and must be non-empty",
		`{"name":"   "}`: "invalid_arguments: name is required and must be non-empty",
		`{"name":"` + strings.Repeat("a", 257) + `"}`: "invalid_arguments: name must be at most 256 bytes",
		`{"name":"Hello","kind":"function"}`:          "invalid_arguments: kind must be one of: func, method, type, class, interface, enum, trait, const, var, module, macro",
		`{"name":"Hello","path":"../"}`:               "path_not_allowed",
		`{"name":"Hello","path":"` + outside + `"}`:   "path_not_allowed",
		`{"name":"Hello","path":"nope"}`:              "read_failed",
		`{"name":"Hello","path":"a.go"}`:              "invalid_arguments: a.go is a file; use file_outline for a single file",
	} {
		r := call(fs, args)
		code, detail, _ := strings.Cut(want, ": ")
		if r.Error != code || (detail != "" && r.Detail != detail) {
			t.Errorf("%.60s: %q %q, want %q", args, r.Error, r.Detail, want)
		}
	}
	if n := walks.Load(); n != 0 {
		t.Errorf("%d walks for calls that were all rejected", n)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r := fs.Execute(ctx, []byte(`{"name":"Hello"}`)); r.Error != "aborted" || r.Detail != "Operation aborted" {
		t.Errorf("a cancelled call: %+v", r)
	}
	for _, args := range []string{`{"name":"` + strings.Repeat("a", 256) + `"}`, `{"name":"Hello","kind":"  FUNC "}`} {
		mustOK(t, call(fs, args))
	}
}

func TestFindSymbolResults(t *testing.T) {
	files := map[string]string{
		"main.go":       "package main\n\nfunc Run() {}\nfunc RunAll() {}\nfunc run() {}\n",
		"runner.go":     "package main\n\ntype Runner struct{}\n\nfunc (r *Runner) Run() {}\n",
		"pkg/a/a.go":    "package a\n\nfunc Sym() {}\n",
		"bad\x01n.go":   "package main\n\nfunc Hello() {}\n",
		"lib/x_test.go": "package lib\n\nfunc RunTest() {}\n",
	}
	for i := 0; i < 80; i++ {
		files[fmt.Sprintf("many/f%03d.go", i)] = fmt.Sprintf("package many\n\nfunc Sym%03d() {}\n", i)
	}
	ws := newWS(t, files)
	fs := newFT(ws, Options{}).findSymbolTool()

	r := mustOK(t, call(fs, `{"name":"Run"}`))
	want := `5 symbols matching "Run"  (index: 85 files; go/ast 85)` + "\n" +
		"main.go:3  func  func Run()\nrunner.go:5  method  func (r *Runner) Run()\nmain.go:4  func  func RunAll()\n" +
		"runner.go:3  type  type Runner struct\nlib/x_test.go:3  func  func RunTest()"
	if r.Text != want || r.Data["truncated"] != false || r.Data["files_indexed"] != 85 || r.Data["note"] != nil || r.Data["partial"] != nil {
		t.Errorf("Text:\n%s\nwant:\n%s\nData %v", r.Text, want, r.Data)
	}
	if b := r.Data["backends"].(map[string]int); b["go/ast"] != 85 || len(b) != 1 {
		t.Errorf("backends = %v", b)
	}
	if got := symbolNames(mustOK(t, call(fs, `{"name":"Runner.Run","exact":true}`))); !slices.Equal(got, []string{"Run"}) {
		t.Errorf("a qualified exact name: %v", got)
	}
	if r := mustOK(t, call(fs, `{"name":"Nothing"}`)); r.Text != `0 symbols matching "Nothing"  (index: 85 files; go/ast 85)`+"\nno symbols matched" || r.Data["symbols"] == nil {
		t.Errorf("no match: %q", r.Text)
	}
	// The text sanitizes a control character the Data keeps.
	r = mustOK(t, call(fs, `{"name":"Hello"}`))
	if !strings.Contains(r.Text, "\nbad?n.go:3  func  func Hello()") || r.Data["symbols"].([]SymbolMatch)[0].Path != "bad\x01n.go" {
		t.Errorf("Text %q Data %v", r.Text, r.Data["symbols"])
	}
	// A path scopes the matches and the counts.
	r = mustOK(t, callJSON(t, fs, map[string]any{"name": "S", "path": abs(ws, "pkg/a")}))
	if !slices.Equal(symbolNames(r), []string{"Sym"}) || r.Data["files_indexed"] != 1 || r.Data["backends"].(map[string]int)["go/ast"] != 1 {
		t.Errorf("a scoped call: %v %v", symbolNames(r), r.Data)
	}

	for _, c := range []struct {
		args   string
		n      int
		marker string
	}{
		{`{"name":"Sym"}`, 20, SymbolMarker(20)},
		{`{"name":"Sym","max_results":0}`, 20, SymbolMarker(20)},
		{`{"name":"Sym","max_results":-5}`, 20, SymbolMarker(20)},
		{`{"name":"Sym","max_results":30}`, 30, SymbolMarker(30)},
		{`{"name":"Sym","max_results":200}`, 50, SymbolMarker(50)},
	} {
		r := mustOK(t, call(fs, c.args))
		if n := len(symbolNames(r)); n != c.n || r.Data["note"] != c.marker || r.Data["truncated"] != true ||
			!strings.HasSuffix(r.Text, "\n"+c.marker) || r.Metadata == nil || r.Metadata.TruncatedBy != "lines" {
			t.Errorf("%s: %d matches, note %v", c.args, n, r.Data["note"])
		}
	}
}

func TestFindSymbolAsksTheIndexFirst(t *testing.T) {
	ws := newWS(t, map[string]string{"a/x.go": "package a\n\nfunc Local() {}\n"})
	answer := SymbolAnswer{FilesIndexed: 3, Backends: map[string]int{"go/ast": 2, "tree-sitter": 1}, Matches: []SymbolMatch{
		{Path: "a/z.py", Backend: "tree-sitter", Kind: "func", Name: "RunLonger", Signature: "def RunLonger()", StartLine: 4, EndLine: 9},
		{Path: "a/y.go", Backend: "go/ast", Kind: "func", Name: "Run", Signature: "func Run()", Exported: true, StartLine: 2},
	}}
	idx := &fakeIndex{ans: answer, ok: true}
	fs := newFT(ws, Options{Index: idx}).findSymbolTool()
	walks := countWalks(t)
	r := mustOK(t, call(fs, `{"name":"Run","path":"a","max_results":1}`))
	// Ranked and truncated by find_symbol's own code; the header names the index.
	want := `1 symbols matching "Run"  (codesearch index: 3 files)` + "\na/y.go:2  func  func Run()\n" + SymbolMarker(1)
	if r.Text != want || r.Data["files_indexed"] != 3 || r.Data["backends"].(map[string]int)["tree-sitter"] != 1 || walks.Load() != 0 {
		t.Errorf("Text:\n%s\nwant:\n%s\n%v, %d walks", r.Text, want, r.Data, walks.Load())
	}
	// An Index that reports no backends has them counted from the matches.
	idx.ans.Backends = nil
	if b := mustOK(t, call(fs, `{"name":"Run"}`)).Data["backends"].(map[string]int); len(b) != 2 || b["go/ast"] != 1 {
		t.Errorf("backends = %v", b)
	}
	idx.ans = SymbolAnswer{}
	if r := mustOK(t, call(fs, `{"name":"Nothing"}`)); !strings.HasSuffix(r.Text, "\nno symbols matched") {
		t.Errorf("an empty answer: %q", r.Text)
	}
	// ok=false falls back to the table; an error is an abort.
	idx.ok = false
	if got := symbolNames(mustOK(t, call(fs, `{"name":"Local"}`))); !slices.Equal(got, []string{"Local"}) || walks.Load() == 0 {
		t.Errorf("the fallback found %v", got)
	}
	idx.err = errors.New("index closed")
	if r := call(fs, `{"name":"Local"}`); r.Error != "aborted" {
		t.Errorf("an index error: %+v", r)
	}
}
