package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/workspace"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFindFilesHonoursANestedGitignore is the end-to-end check: the engine is
// only useful if the walk actually feeds it every directory it enters.
func TestFindFilesHonoursANestedGitignore(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "src/keep.go", "")
	writeFile(t, dir, "src/gen/.gitignore", "*.go\n")
	writeFile(t, dir, "src/gen/generated.go", "")

	files := runFindFiles(t, dir, "**/*.go")
	for _, f := range files {
		if filepath.ToSlash(f) == "src/gen/generated.go" {
			t.Fatalf("find_files returned %v; src/gen/.gitignore excludes *.go and the "+
				"walk must load it on the way down", files)
		}
	}
	var sawKeep bool
	for _, f := range files {
		if filepath.ToSlash(f) == "src/keep.go" {
			sawKeep = true
		}
	}
	if !sawKeep {
		t.Fatalf("find_files returned %v, want src/keep.go", files)
	}
}

// runFindFiles drives the real find_files tool, through the real resolver, so
// the test exercises the shipped path rather than a fixture (NFR-TEST-08.3).
func runFindFiles(t *testing.T, dir, pattern string) []string {
	t.Helper()
	ws, err := workspace.Open(dir, workspace.Options{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	all, err := For(ws, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name != "find_files" {
			continue
		}
		args, _ := json.Marshal(map[string]any{"pattern": pattern})
		res := tl.Execute(context.Background(), args)
		if !res.OK {
			t.Fatalf("find_files failed: %s", res.Error)
		}
		raw, _ := res.Data["files"].([]string)
		return raw
	}
	t.Fatal("find_files is not in the default tool set")
	return nil
}
