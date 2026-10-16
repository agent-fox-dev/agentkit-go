package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// referencesModule is a small Go module with a declaration, Target, and three
// references to it.
func referencesModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":       "module example.com/refs\n\ngo 1.27\n",
		"lib/lib.go":   "package lib\n\n// Target is the declaration the references point at.\nfunc Target() int { return 1 }\n",
		"lib/other.go": "package lib\n\nfunc Other() int { return Target() }\n",
		"main.go":      "package main\n\nimport \"example.com/refs/lib\"\n\nfunc main() {\n\t_ = lib.Target()\n\t_ = lib.Target()\n}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var targetDecl = outline.Decl{Kind: outline.KindFunc, Name: "Target"}

// listing is every path under dir, to show nothing was created.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err == nil {
			out = append(out, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TS-20-41: tools.References returns the same sites as the method it replaces
// did, with default bounds, no cache and no index. The expected result is
// testdata/references_target.json, recorded from (*Workspace).References
// before this change.
func TestReferencesMatchesTheMethodItReplaces_TS20_41(t *testing.T) {
	dir := referencesModule(t)
	ws := openForDir(t, dir)
	before := listing(t, ws.Root())

	res, err := tools.References(context.Background(), ws, targetDecl, tools.ReferenceOptions{})
	if err != nil {
		t.Fatalf("References: %v", err)
	}
	got, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "references_target.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got)+"\n" != string(want) {
		t.Errorf("the result differs from the recorded one:\n got  %s\n want %s", got, want)
	}
	if after := listing(t, ws.Root()); !reflect.DeepEqual(before, after) {
		t.Errorf("References created files: before %v, after %v", before, after)
	}
}

// TS-20-42: tools.References with a nil workspace returns an empty result and
// an error.
func TestReferencesRefusesANilWorkspace_TS20_42(t *testing.T) {
	res, err := tools.References(context.Background(), nil, targetDecl, tools.ReferenceOptions{})
	if !reflect.DeepEqual(res, tools.ReferenceResult{}) {
		t.Errorf("result = %+v, want the zero value", res)
	}
	if err == nil {
		t.Error("a nil workspace returned no error")
	}
}

// TS-20-43: tools.References with a path outside the workspace returns
// ErrPathNotAllowed and an empty result.
func TestReferencesRefusesAnOutsidePath_TS20_43(t *testing.T) {
	ws := openForDir(t, referencesModule(t))
	outside := t.TempDir()
	for _, p := range []string{outside, "../x"} {
		res, err := tools.References(context.Background(), ws, targetDecl, tools.ReferenceOptions{Path: p})
		if !errors.Is(err, workspace.ErrPathNotAllowed) {
			t.Errorf("Path %q: err = %v, want workspace.ErrPathNotAllowed", p, err)
		}
		if !reflect.DeepEqual(res, tools.ReferenceResult{}) {
			t.Errorf("Path %q: result = %+v, want the zero value", p, res)
		}
	}
}

// TS-20-44: tools.References stops with the context's error when cancelled.
func TestReferencesStopsWhenCancelled_TS20_44(t *testing.T) {
	ws := openForDir(t, referencesModule(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tools.References(ctx, ws, targetDecl, tools.ReferenceOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !reflect.DeepEqual(res, tools.ReferenceResult{}) {
		t.Errorf("result = %+v, want the zero value", res)
	}
}
