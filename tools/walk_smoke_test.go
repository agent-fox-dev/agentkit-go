package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentfox/agentkit-go/tools"
)

// TS-01-76 (smoke): A walk root outside the workspace is refused before any
// entry is visited.
func TestWalkOutsideWorkspaceRefused_TS_01_76(t *testing.T) {
	wsDir := t.TempDir()
	outsideDir := t.TempDir()

	ws, err := tools.NewWorkspace(wsDir)
	if err != nil {
		t.Fatal(err)
	}

	// Put a file in the outside dir so we can detect if the callback fires.
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	var calls int
	cb := func(rel string, d fs.DirEntry) error {
		calls++
		return nil
	}

	// Resolve outside through symlinks so it's a valid absolute path.
	outsideResolved, _ := filepath.EvalSymlinks(outsideDir)
	if outsideResolved == "" {
		outsideResolved = outsideDir
	}

	err = tools.Walk(ctx, ws, outsideResolved, tools.WalkOptions{}, cb)

	// The returned error satisfies errors.Is(err, ErrPathNotAllowed).
	if !errors.Is(err, tools.ErrPathNotAllowed) {
		t.Fatalf("want ErrPathNotAllowed, got %v", err)
	}

	// The callback is never called.
	if calls != 0 {
		t.Fatalf("callback should not be called, got %d calls", calls)
	}
}

// TS-01-77 (smoke): find_files lists files through the shared walk with nested
// ignores, hidden entries and no symlink following.
func TestFindFilesThroughSharedWalk_TS_01_77(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Build the tree.
	write(".gitignore", "ignored/\n")
	write(".github/ci.yml", "name: CI\n")
	write(".hidden_file", "secret")
	write("README.md", "hello\n")
	write("sub/.gitignore", "nested_ignored.txt\n")
	write("sub/kept.txt", "ok")
	write("sub/nested_ignored.txt", "nope")
	write("ignored/should_not_appear.txt", "gone")
	write("vendor/sub/.git/HEAD", "ref: refs/heads/main\n")
	write("vendor/sub/ok.go", "package sub\n")

	// Create a symlink to a directory.
	linkTarget := filepath.Join(root, "sub")
	link := filepath.Join(root, "link_to_sub")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ws, err := tools.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}

	var findTool func(ctx context.Context, in json.RawMessage) ([]string, bool, string)
	for _, tl := range all {
		if tl.Name == "find_files" {
			fn := tl.Execute
			findTool = func(ctx context.Context, in json.RawMessage) ([]string, bool, string) {
				res := fn(ctx, in)
				if !res.OK {
					return nil, false, res.Error
				}
				files, _ := res.Data["files"].([]string)
				trunc, _ := res.Data["truncated"].(bool)
				marker, _ := res.Data["marker"].(string)
				_ = marker
				return files, trunc, ""
			}
			break
		}
	}
	if findTool == nil {
		t.Fatal("find_files not found in tool set")
	}

	ctx := context.Background()

	// Run find_files with pattern ** and file_type file.
	args, _ := json.Marshal(map[string]any{"pattern": "**", "file_type": "file"})
	files, _, errStr := findTool(ctx, args)
	if errStr != "" {
		t.Fatalf("find_files failed: %s", errStr)
	}

	// Hidden files such as .github/ci.yml are returned.
	assertContainsStr(t, files, ".github/ci.yml")
	assertContainsStr(t, files, ".hidden_file")
	assertContainsStr(t, files, ".gitignore")

	// Files ignored by nested .gitignore files are absent.
	assertNotContainsStr(t, files, "ignored/should_not_appear.txt")
	assertNotContainsStr(t, files, "sub/nested_ignored.txt")

	// The symlinked directory's contents are not listed (no symlink following).
	for _, f := range files {
		if strings.HasPrefix(f, "link_to_sub/") {
			t.Errorf("symlinked directory contents should not be listed: %q", f)
		}
	}

	// The result is sorted.
	if !sort.StringsAreSorted(files) {
		t.Errorf("files not sorted: %v", files)
	}

	// Test with a limit to verify truncation marker.
	argsLimited, _ := json.Marshal(map[string]any{"pattern": "**", "file_type": "file", "limit": 2})
	filesLimited, trunc, errStr := findTool(ctx, argsLimited)
	if errStr != "" {
		t.Fatalf("find_files limited failed: %s", errStr)
	}
	if len(filesLimited) != 2 {
		t.Fatalf("expected 2 files with limit=2, got %d", len(filesLimited))
	}
	if !trunc {
		t.Error("expected truncated=true with limit=2")
	}
}

// TS-01-78 (smoke): The native search_files backend and CountCandidates share
// the walk and keep their result shape.
func TestNativeSearchAndCountCandidates_TS_01_78(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(".gitignore", "ignored/\n")
	write("visible.go", "needle in visible\n")
	write("visible.py", "needle in python\n")
	write("other.txt", "no match here\n")
	write(".hidden.go", "needle in hidden\n")
	write("ignored/skip.go", "needle in ignored\n")

	// Create a symlink to a file containing the pattern.
	write("target.go", "needle in target\n")
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(filepath.Join(root, "target.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Force native backend.
	restore := tools.SetRipgrepLookup(func() (string, bool) { return "", false })
	defer restore()

	ctx := context.Background()

	// Search with file_glob=*.go.
	res, backend, err := tools.SearchIn(ctx, root, tools.SearchParams{
		Pattern:  "needle",
		FileGlob: "*.go",
	}, tools.NoGlobalExcludes())
	if err != nil {
		t.Fatalf("SearchIn: %v", err)
	}
	if backend != tools.BackendNative {
		t.Fatalf("expected native backend, got %q", backend)
	}

	// Matches come only from non-ignored, non-hidden regular files matching file_glob.
	matchFiles := map[string]bool{}
	for _, m := range res.Matches {
		matchFiles[m.File] = true
	}

	if matchFiles[".hidden.go"] {
		t.Error("hidden file should not be searched")
	}
	if matchFiles["ignored/skip.go"] {
		t.Error("ignored file should not be searched")
	}
	if matchFiles["link.go"] {
		t.Error("symlink should not be searched")
	}
	if matchFiles["visible.py"] {
		t.Error("visible.py should not match file_glob=*.go")
	}
	if !matchFiles["visible.go"] {
		t.Error("visible.go should be searched and matched")
	}
	if !matchFiles["target.go"] {
		t.Error("target.go should be searched and matched")
	}

	// The result shape has matches, truncated, files_searched.
	if res.Matches == nil {
		t.Error("Matches should not be nil")
	}
	// files_searched should be a positive number.
	if res.FilesSearched == 0 {
		t.Error("FilesSearched should be > 0")
	}

	// CountCandidates with file_glob=*.go.
	count, err := tools.CountCandidates(ctx, root, tools.SearchParams{FileGlob: "*.go"})
	if err != nil {
		t.Fatalf("CountCandidates: %v", err)
	}

	// files_searched equals the CountCandidates value.
	if res.FilesSearched != count {
		t.Fatalf("FilesSearched=%d != CountCandidates=%d", res.FilesSearched, count)
	}
}

// --- helpers ---

func assertContainsStr(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("expected %q in %v", want, slice)
}

func assertNotContainsStr(t *testing.T, slice []string, unwanted string) {
	t.Helper()
	for _, s := range slice {
		if s == unwanted {
			t.Errorf("did not expect %q in %v", unwanted, slice)
			return
		}
	}
}
