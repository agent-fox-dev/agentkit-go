package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// refsModule is a Go module whose Target is called once in its package, twice
// from main, and once from a file the workspace ignores.
var refsModule = map[string]string{
	"go.mod":             "module example.com/refs\n\ngo 1.27\n",
	".gitignore":         "ignored/\n",
	"lib/lib.go":         "package lib\n\n// Target is the declaration the references point at.\nfunc Target() int { return 1 }\n",
	"lib/other.go":       "package lib\n\nfunc Other() int { return Target() }\n",
	"main.go":            "package main\n\nimport \"example.com/refs/lib\"\n\nfunc main() {\n\t_ = lib.Target()\n\t_ = lib.Target()\n}\n",
	"ignored/skipped.go": "package ignored\n\nimport \"example.com/refs/lib\"\n\nfunc Skipped() int { return lib.Target() }\n",
}

func siteList(res ReferenceResult) []string {
	var out []string
	for _, s := range res.Sites {
		out = append(out, fmt.Sprintf("%s:%d:%d %s", s.Path, s.Line, s.Column, s.Confidence))
	}
	return out
}

func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, _ fs.DirEntry, err error) error { out = append(out, p); return err })
	return out
}

func TestReferencesFindsTheResolvedCallers(t *testing.T) {
	ws := newWS(t, refsModule)
	before := listing(t, ws.Root())
	res, err := References(context.Background(), ws, outline.Decl{Kind: outline.KindFunc, Name: "Target"}, ReferenceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	testutil.Golden(t, filepath.Join("testdata", "references_target.json"), append(b, '\n'))
	if want := []string{"lib/other.go:3:27 resolved", "main.go:6:10 resolved", "main.go:7:10 resolved"}; !slices.Equal(siteList(res), want) {
		t.Errorf("sites %v, want %v", siteList(res), want)
	}
	if after := listing(t, ws.Root()); !slices.Equal(before, after) {
		t.Errorf("References created files: %v", after)
	}
	// A path scopes the sites.
	res, err = References(context.Background(), ws, outline.Decl{Kind: outline.KindFunc, Name: "Target"}, ReferenceOptions{Path: "lib"})
	if err != nil || !slices.Equal(siteList(res), []string{"lib/other.go:3:27 resolved"}) {
		t.Errorf("Path lib: %v, %v", siteList(res), err)
	}
	if res, err := References(context.Background(), ws, outline.Decl{Name: "Target"}, ReferenceOptions{Path: "."}); err != nil || len(res.Sites) != 3 {
		t.Errorf("Path . is the whole workspace: %v, %v", siteList(res), err)
	}
}

func TestReferencesRefusals(t *testing.T) {
	ws := newWS(t, map[string]string{"main.go": "package main\n"})
	target := outline.Decl{Kind: outline.KindFunc, Name: "Run"}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, c := range map[string]struct {
		ctx  context.Context
		ws   *workspace.Workspace
		path string
		is   error
	}{
		"a nil workspace":          {context.Background(), nil, "", nil},
		"a path outside":           {context.Background(), ws, "../escape", workspace.ErrPathNotAllowed},
		"an absolute path outside": {context.Background(), ws, t.TempDir(), workspace.ErrPathNotAllowed},
		"a missing directory":      {context.Background(), ws, "no_such_dir", nil},
		"a file":                   {context.Background(), ws, "main.go", nil},
		"a cancelled context":      {cancelled, ws, "", context.Canceled},
	} {
		res, err := References(c.ctx, c.ws, target, ReferenceOptions{Path: c.path})
		if err == nil || (c.is != nil && !errors.Is(err, c.is)) || !reflect.DeepEqual(res, ReferenceResult{}) {
			t.Errorf("%s: %+v, %v; want a zero result and an error", name, res, err)
		}
	}
	if _, err := executeReferenceSearch(context.Background(), nil, target, "", "", ReferenceOptions{}, SymbolOptions{}, nil); err == nil {
		t.Error("a nil workspace inside the search")
	}
	// An empty workspace is an empty answer, not an error.
	if res, err := References(context.Background(), newWS(t, nil), target, ReferenceOptions{}); err != nil || res.Target != target || res.Backend != "lexical" {
		t.Errorf("an empty workspace: %+v, %v", res, err)
	}
}

func TestReferencesChoosesItsBackendFromTheTarget(t *testing.T) {
	ws := newWS(t, map[string]string{
		"go.mod":      "module example.com/c\n\ngo 1.22\n",
		"a/a.go":      "package a\n\nimport \"fmt\"\n\nfunc Hello() { fmt.Println(\"hi\") }\n",
		"b/b.go":      "package b\n\nimport \"example.com/c/a\"\n\nfunc Use() { a.Hello() }\n",
		"b/b_test.go": "package b\n\nimport \"example.com/c/a\"\n\nfunc TestUse() { a.Hello() }\n",
		"calc.py":     "def calc_total(x):\n    return calc_total(x)\n",
	})
	for _, c := range []struct {
		target  outline.Decl
		opts    ReferenceOptions
		backend string
		sites   []string
	}{
		{outline.Decl{Kind: outline.KindFunc, Name: "Hello", StartLine: 5}, ReferenceOptions{}, "go/types", []string{"b/b.go:5:16 resolved"}},
		{outline.Decl{Kind: outline.KindFunc, Name: "Hello", StartLine: 5}, ReferenceOptions{IncludeTests: true}, "go/types",
			[]string{"b/b.go:5:16 resolved", "b/b_test.go:5:20 resolved"}},
		// A name no Go package declares is searched lexically.
		{outline.Decl{Kind: outline.KindFunc, Name: "calc_total"}, ReferenceOptions{}, "lexical", nil},
		// An empty name finds nothing, as text.
		{outline.Decl{}, ReferenceOptions{}, "text", nil},
	} {
		res, err := References(context.Background(), ws, c.target, c.opts)
		if err != nil || res.Backend != c.backend || (c.sites != nil && !slices.Equal(siteList(res), c.sites)) {
			t.Errorf("%s %+v: %s %v, %v; want %s %v", c.target.Name, c.opts, res.Backend, siteList(res), err, c.backend, c.sites)
		}
		if c.backend == "go/types" && (res.PackagesChecked < 2 || res.Errors == 0) {
			t.Errorf("PackagesChecked %d, Errors %d: want both packages, and the error from the stubbed fmt", res.PackagesChecked, res.Errors)
		}
	}
}
