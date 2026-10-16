package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-45: Search, SearchIn and CountCandidates keep their root-path
// signatures and results, and SearchIn takes a workspace.IgnoreOptions. The
// expected results, testdata/search_recorded.json, were recorded from the code
// before this change.
func TestSearchEntryPointsKeepTheirResults_TS20_45(t *testing.T) {
	hermeticEnv(t)
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore": "*.log\n", "a.go": "package a\nfunc needle() {}\n", "b.glob": "needle\n",
		"ignored.log": "needle\n", "sub/.gitignore": "c.txt\n", "sub/c.txt": "needle\n", "sub/d.txt": "needle\n", ".hidden": "needle\n",
	})
	global := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(global, []byte("*.glob\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	type searched struct {
		Files         []string `json:"files"`
		FilesSearched int      `json:"files_searched"`
		Truncated     bool     `json:"truncated"`
	}
	var want struct {
		Hermetic searched `json:"search_in_hermetic"`
		Global   searched `json:"search_in_global"`
		Real     searched `json:"search_real_env"`
		Count    int      `json:"count_candidates_real_env"`
		CountGo  int      `json:"count_candidates_go"`
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "search_recorded.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	p := tools.SearchParams{Pattern: "needle"}
	rec := func(res tools.SearchResult, err error) searched {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var files []string
		for _, m := range res.Matches {
			if !slices.Contains(files, m.File) {
				files = append(files, m.File)
			}
		}
		slices.Sort(files)
		return searched{files, res.FilesSearched, res.Truncated}
	}
	withGlobal := workspace.NoGlobalExcludes()
	withGlobal.GitConfig = func() string { return global }

	if got := rec(tools.SearchIn(ctx, root, p, workspace.NoGlobalExcludes())); !reflect.DeepEqual(got, want.Hermetic) {
		t.Errorf("SearchIn with NoGlobalExcludes: %+v, recorded %+v", got, want.Hermetic)
	}
	if got := rec(tools.SearchIn(ctx, root, p, withGlobal)); !reflect.DeepEqual(got, want.Global) {
		t.Errorf("SearchIn with a global excludes file given through the options: %+v, recorded %+v", got, want.Global)
	}
	if got := rec(tools.Search(ctx, root, p)); !reflect.DeepEqual(got, want.Real) {
		t.Errorf("Search: %+v, recorded %+v", got, want.Real)
	}
	if n, err := tools.CountCandidates(ctx, root, tools.SearchParams{}); err != nil || n != want.Count {
		t.Errorf("CountCandidates = %d, %v, recorded %d", n, err, want.Count)
	}
	if n, err := tools.CountCandidates(ctx, root, tools.SearchParams{FileGlob: "*.go"}); err != nil || n != want.CountGo {
		t.Errorf("CountCandidates with a glob = %d, %v, recorded %d", n, err, want.CountGo)
	}
}
