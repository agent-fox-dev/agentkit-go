//go:build !windows

package codesearch

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-65 (smoke, 20-PATH-2): an embedder builds a code-search index for a
// workspace, builds the tools with the index, and search_files returns matches
// only from non-ignored files. Every component is real: workspace, the index
// (zoekt), tools.For, the search_files and code_search tools, the filesystem.
func TestSmokeIndexAndSearchFiles_TS20_65(t *testing.T) {
	root := t.TempDir()
	const token = "needleToken"
	body := "package p\n\n// " + token + "\nfunc F() {}\n"
	mkFile(t, root, ".gitignore", "ignored/\n")
	mkFile(t, root, "nested/.gitignore", "inner.go\n")
	for _, rel := range []string{"kept.go", "nested/ok.go", "ignored/hidden.go", "nested/inner.go"} {
		mkFile(t, root, rel, body)
	}

	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := New(ws, Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Close() })
	ts, err := tools.For(ws, tools.Options{Index: idx})
	if err != nil {
		t.Fatal(err)
	}

	builtins := []string{"read_file", "write_file", "edit_file", "list_files", "find_files", "search_files",
		"file_outline", "find_symbol", "find_references", "execute", "run_command"}
	var names []string
	for _, tl := range ts {
		names = append(names, tl.Name)
	}
	if len(names) < 12 || !slices.Equal(names[:11], builtins) || names[11] != "code_search" {
		t.Fatalf("the tool list is %v, want the eleven built-ins and then the index's tools", names)
	}

	searchTool, codeSearch := ts[5], ts[11]
	res := searchTool.Execute(context.Background(), json.RawMessage(`{"pattern":"`+token+`"}`))
	if !res.OK {
		t.Fatalf("search_files: %s: %s", res.Error, res.Detail)
	}
	var found []string
	for _, m := range res.Data["matches"].([]tools.SearchMatch) {
		if !slices.Contains(found, m.File) {
			found = append(found, m.File)
		}
	}
	slices.Sort(found)
	if want := []string{"kept.go", "nested/ok.go"}; !slices.Equal(found, want) {
		t.Errorf("search_files found %v, want only the non-ignored files %v", found, want)
	}

	cs := codeSearch.Execute(context.Background(), json.RawMessage(`{"query":"`+token+`"}`))
	if !cs.OK {
		t.Fatalf("code_search: %s: %s", cs.Error, cs.Detail)
	}
	if got, want := getResultFilePaths(cs), []string{"kept.go", "nested/ok.go"}; !slices.Equal(sortedCopy(got), want) {
		t.Errorf("code_search returned %v, want only the non-ignored files %v", got, want)
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}
