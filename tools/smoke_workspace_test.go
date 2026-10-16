package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/tools"
	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// TS-20-64 (smoke, 20-PATH-1): an embedder opens a workspace and builds the
// tools for it, and a model-style call to find_files lists the matching
// non-ignored files and is refused a path outside the root. Every component is
// real: workspace.Open, Resolve and Walk, tools.For, the find_files tool and
// the filesystem.
func TestSmokeOpenForAndFindFiles_TS20_64(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":   "ignored/\n*.log\n",
		"a.go":         "package a\n",
		"sub/b.go":     "package sub\n",
		"ignored/c.go": "package ignored\n",
		"x.log":        "log\n",
		"notes.txt":    "notes\n",
	})
	writeTree(t, outside, map[string]string{"secret.go": "package secret\n"})

	ws, err := workspace.Open(root, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	ts, err := tools.For(ws, tools.Options{})
	if err != nil {
		t.Fatal(err)
	}

	res := callTool(t, ts, "find_files", `{"pattern":"**/*.go"}`)
	got, _ := res.Data["files"].([]string)
	if want := []string{"a.go", "sub/b.go"}; !slices.Equal(got, want) {
		t.Errorf("find_files **/*.go = %v, want the non-ignored files %v", got, want)
	}

	quoted, _ := json.Marshal(outside)
	bad := toolIn(t, ts, "find_files").Execute(context.Background(),
		json.RawMessage(fmt.Sprintf(`{"path":%s,"pattern":"*"}`, quoted)))
	if bad.OK || bad.Error != "path_not_allowed" {
		t.Errorf("find_files on a path outside the root: OK=%v Error=%q, want an error result with code path_not_allowed", bad.OK, bad.Error)
	}
}
