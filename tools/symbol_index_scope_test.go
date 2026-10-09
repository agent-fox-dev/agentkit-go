package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/core"
)

// answerIndex is an Index whose Symbols answers with a fixed SymbolAnswer.
type answerIndex struct {
	fakeIndex
	ans SymbolAnswer
}

func (a *answerIndex) Symbols(_ context.Context, _ SymbolQuery) (SymbolAnswer, bool, error) {
	return a.ans, true, nil
}

func findSymbolWith(t *testing.T, idx Index, args map[string]any) core.ToolResult {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{
		Workspace: ws,
		Env:       os.Environ(),
		Ignore:    NoGlobalExcludes(),
		Symbols:   SymbolOptions{},
		Index:     idx,
	}.withDefaults())
	in, _ := json.Marshal(args)
	return ft.findSymbolTool().Execute(context.Background(), in)
}

// 03-REQ-7.2 and 02-REQ-4.5: through an Index, Data.backends and
// Data.files_indexed describe the queried scope, as they do for the table, not
// the matches returned and not the whole index.
func TestFindSymbolThroughTheIndexReportsTheScopesBackends(t *testing.T) {
	idx := &answerIndex{ans: SymbolAnswer{
		// One match, from a tree-sitter file, in a scope of 3 files.
		Matches:      []SymbolMatch{{Path: "a/x.py", Backend: "tree-sitter", Kind: "func", Name: "Run", Signature: "def Run()"}},
		FilesIndexed: 3,
		Backends:     map[string]int{"go/ast": 2, "tree-sitter": 1},
	}}
	r := findSymbolWith(t, idx, map[string]any{"name": "Run", "path": "a"})
	if !r.OK {
		t.Fatalf("find_symbol: %s: %s", r.Error, r.Detail)
	}
	if got := r.Data["files_indexed"]; got != 3 {
		t.Errorf("files_indexed = %v, want 3 (the scope)", got)
	}
	got, _ := r.Data["backends"].(map[string]int)
	if len(got) != 2 || got["go/ast"] != 2 || got["tree-sitter"] != 1 {
		t.Errorf("backends = %v, want the scope's {go/ast:2 tree-sitter:1}, not the matches' backends", r.Data["backends"])
	}
}

// An Index that does not report backends keeps the earlier behaviour: they are
// counted from the matches.
func TestFindSymbolCountsBackendsFromMatchesWhenTheIndexReportsNone(t *testing.T) {
	idx := &answerIndex{ans: SymbolAnswer{
		Matches: []SymbolMatch{
			{Path: "a.go", Backend: "go/ast", Kind: "func", Name: "Run", Signature: "func Run()"},
			{Path: "b.go", Backend: "go/ast", Kind: "func", Name: "Run2", Signature: "func Run2()"},
		},
		FilesIndexed: 9,
	}}
	r := findSymbolWith(t, idx, map[string]any{"name": "Run"})
	if !r.OK {
		t.Fatalf("find_symbol: %s: %s", r.Error, r.Detail)
	}
	got, _ := r.Data["backends"].(map[string]int)
	if len(got) != 1 || got["go/ast"] != 2 {
		t.Errorf("backends = %v, want {go/ast:2} counted from the matches", r.Data["backends"])
	}
}
