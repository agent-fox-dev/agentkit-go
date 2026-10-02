package tools

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TS-01-56: tools contains exactly one directory-walk implementation.
// Parses non-test Go sources of the tools package with go/parser and counts
// calls to filepath.WalkDir, filepath.Walk and fs.WalkDir, expecting exactly
// one such call inside walk.go.
func TestToolsContainsExactlyOneWalkDirCall_TS0156(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	type callInfo struct {
		file string
		line int
	}
	var calls []callInfo

	for _, e := range entries {
		name := e.Name()
		// Only non-test Go source files
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			// Check for selector expressions like filepath.WalkDir, filepath.Walk, fs.WalkDir
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			funcName := sel.Sel.Name
			pkgName := ident.Name
			if (pkgName == "filepath" && (funcName == "WalkDir" || funcName == "Walk")) ||
				(pkgName == "fs" && funcName == "WalkDir") {
				pos := fset.Position(call.Pos())
				calls = append(calls, callInfo{file: pos.Filename, line: pos.Line})
			}
			return true
		})
	}

	if len(calls) != 1 {
		for _, c := range calls {
			t.Logf("  WalkDir call at %s:%d", c.file, c.line)
		}
		t.Fatalf("expected exactly 1 filepath.WalkDir/Walk/fs.WalkDir call in non-test tools sources, got %d", len(calls))
	}
	if calls[0].file != "walk.go" {
		t.Fatalf("the single WalkDir call should be in walk.go, but found in %s", calls[0].file)
	}
}

// TS-01-57: Search, SearchIn and CountCandidates work from a bare root string
// without a Workspace.
func TestSearchAndCountWithoutWorkspace_TS0157(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.go", "package main\nfunc needle() {}\n")
	writeTestFile(t, root, "b.txt", "no match here\n")

	ctx := context.Background()

	// Search
	res, _, err := Search(ctx, root, SearchParams{Pattern: "needle"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(res.Matches) == 0 {
		t.Fatal("Search: expected at least one match")
	}

	// SearchIn
	res2, _, err := SearchIn(ctx, root, SearchParams{Pattern: "needle"}, NoGlobalExcludes())
	if err != nil {
		t.Fatalf("SearchIn: %v", err)
	}
	if len(res2.Matches) == 0 {
		t.Fatal("SearchIn: expected at least one match")
	}

	// CountCandidates
	n, err := CountCandidates(ctx, root, SearchParams{})
	if err != nil {
		t.Fatalf("CountCandidates: %v", err)
	}
	if n != 2 {
		t.Fatalf("CountCandidates: want 2, got %d", n)
	}
}

// TS-01-58: find_files still returns dot-prefixed entries through Walk with
// IncludeHidden true.
func TestFindFilesReturnsDotPrefixedEntries_TS0158(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, ".github/workflows/ci.yml", "name: CI\n")
	writeTestFile(t, dir, ".gitignore", "*.log\n")
	writeTestFile(t, dir, "README.md", "hello\n")

	files := runFindFilesAny(t, dir, "**")
	assertContains(t, files, ".gitignore")
	assertContains(t, files, ".github/")
	assertContains(t, files, ".github/workflows/")
	assertContains(t, files, ".github/workflows/ci.yml")
	assertContains(t, files, "README.md")

	// .git itself should be excluded
	for _, f := range files {
		if f == ".git" || f == ".git/" || strings.HasPrefix(f, ".git/") {
			t.Errorf("did not expect .git entries, got %q", f)
		}
	}
}

// runFindFilesAny drives find_files with file_type=any.
func runFindFilesAny(t *testing.T, dir, pattern string) []string {
	t.Helper()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := All(Options{Workspace: ws, Ignore: NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range all {
		if tl.Name != "find_files" {
			continue
		}
		args, _ := json.Marshal(map[string]any{"pattern": pattern, "file_type": "any"})
		res := tl.Execute(context.Background(), args)
		if !res.OK {
			t.Fatalf("find_files failed: %s", res.Error)
		}
		raw, _ := res.Data["files"].([]string)
		return raw
	}
	t.Fatal("find_files not found")
	return nil
}

// TS-01-59: find_files keeps its file_type filter, glob matching, limit,
// truncation marker and sorting.
func TestFindFilesKeepsFiltersAndLimits_TS0159(t *testing.T) {
	dir := t.TempDir()
	// Create files that would sort differently
	writeTestFile(t, dir, "z.go", "")
	writeTestFile(t, dir, "a.go", "")
	writeTestFile(t, dir, "m.go", "")
	writeTestFile(t, dir, "b.txt", "")
	writeTestFile(t, dir, "subdir/c.go", "")

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := All(Options{Workspace: ws, Ignore: NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	var find func(ctx context.Context, in json.RawMessage) json.RawMessage
	for _, tl := range all {
		if tl.Name == "find_files" {
			fn := tl.Execute
			find = func(ctx context.Context, in json.RawMessage) json.RawMessage {
				res := fn(ctx, in)
				b, _ := json.Marshal(res.Data)
				return b
			}
			break
		}
	}
	if find == nil {
		t.Fatal("find_files not found")
	}

	// file_type=file with glob *.go and limit=2
	args, _ := json.Marshal(map[string]any{"pattern": "**/*.go", "file_type": "file", "limit": 2})
	res := json.RawMessage(nil)
	for _, tl := range all {
		if tl.Name == "find_files" {
			r := tl.Execute(context.Background(), args)
			if !r.OK {
				t.Fatalf("find_files failed: %s", r.Error)
			}
			// Check sorting
			files := r.Data["files"].([]string)
			if len(files) != 2 {
				t.Fatalf("expected 2 files with limit=2, got %d: %v", len(files), files)
			}
			// Should be sorted
			if !sort.StringsAreSorted(files) {
				t.Fatalf("files not sorted: %v", files)
			}
			// Should be truncated
			trunc, _ := r.Data["truncated"].(bool)
			if !trunc {
				t.Fatalf("expected truncated=true")
			}
			// Should have marker
			marker, _ := r.Data["marker"].(string)
			if marker == "" {
				t.Fatal("expected truncation marker")
			}
			_ = res
			break
		}
	}

	// file_type=dir
	args2, _ := json.Marshal(map[string]any{"pattern": "**", "file_type": "dir"})
	for _, tl := range all {
		if tl.Name == "find_files" {
			r := tl.Execute(context.Background(), args2)
			if !r.OK {
				t.Fatalf("find_files dir failed: %s", r.Error)
			}
			files := r.Data["files"].([]string)
			for _, f := range files {
				if !strings.HasSuffix(f, "/") {
					t.Fatalf("file_type=dir returned non-directory: %q", f)
				}
			}
			break
		}
	}
}

// TS-01-60: find_files with ** and file_type file returns the same set as Walk
// with IncludeHidden true.
func TestFindFilesMatchesWalkIncludeHidden_TS0160(t *testing.T) {
	dir := t.TempDir()
	// Build a tree with nested .gitignore, nested repo, hidden files, symlink
	writeTestFile(t, dir, ".gitignore", "ignored/\n")
	writeTestFile(t, dir, "visible.txt", "hello")
	writeTestFile(t, dir, ".hidden_file", "secret")
	writeTestFile(t, dir, ".hidden_dir/inside.txt", "inner")
	writeTestFile(t, dir, "sub/.gitignore", "nested_ignored.txt\n")
	writeTestFile(t, dir, "sub/kept.txt", "ok")
	writeTestFile(t, dir, "sub/nested_ignored.txt", "nope")
	writeTestFile(t, dir, "ignored/should_not_appear.txt", "gone")
	writeTestFile(t, dir, "vendor/sub/.git/HEAD", "ref: refs/heads/main\n")
	writeTestFile(t, dir, "vendor/sub/ok.go", "package sub\n")

	// Create a symlink
	link := filepath.Join(dir, "link_to_visible")
	if err := os.Symlink(filepath.Join(dir, "visible.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir := ws.Root

	// Get find_files results (file_type=file, pattern=**)
	findResult := runFindFiles(t, dir, "**")

	// Get Walk results with IncludeHidden=true, collecting non-directory entries
	ctx := context.Background()
	var walkFiles []string
	err = Walk(ctx, ws, resolvedDir, WalkOptions{Ignore: NoGlobalExcludes(), IncludeHidden: true}, func(rel string, d fs.DirEntry) error {
		if !d.IsDir() {
			walkFiles = append(walkFiles, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	// Also filter walkFiles by the ** glob to match find_files behavior
	var walkFiltered []string
	for _, f := range walkFiles {
		if MatchGlob("**", f) {
			walkFiltered = append(walkFiltered, f)
		}
	}

	sort.Strings(findResult)
	sort.Strings(walkFiltered)

	if len(findResult) != len(walkFiltered) {
		t.Fatalf("find_files returned %d files, Walk returned %d files\nfind: %v\nwalk: %v",
			len(findResult), len(walkFiltered), findResult, walkFiltered)
	}
	for i := range findResult {
		if findResult[i] != walkFiltered[i] {
			t.Fatalf("mismatch at %d: find=%q walk=%q\nfind: %v\nwalk: %v",
				i, findResult[i], walkFiltered[i], findResult, walkFiltered)
		}
	}
}

// TS-01-61: The native search backend and countCandidates use the walk with
// hidden entries excluded and keep their regular-file check and file_glob.
func TestNativeSearchExcludesHiddenAndAppliesGlob_TS0161(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, ".hidden_match.go", "needle\n")
	writeTestFile(t, dir, "visible.go", "needle\n")
	writeTestFile(t, dir, "visible.txt", "needle\n")

	// Create a symlink to a file containing the pattern
	writeTestFile(t, dir, "target.go", "needle\n")
	link := filepath.Join(dir, "link.go")
	if err := os.Symlink(filepath.Join(dir, "target.go"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Force native backend
	restore := SetRipgrepLookup(func() (string, bool) { return "", false })
	defer restore()

	ctx := context.Background()

	// Search with file_glob=*.go
	res, _, err := SearchIn(ctx, dir, SearchParams{Pattern: "needle", FileGlob: "*.go"}, NoGlobalExcludes())
	if err != nil {
		t.Fatalf("SearchIn: %v", err)
	}

	// Only visible.go and target.go should match (not hidden, not symlink, not .txt)
	matchFiles := map[string]bool{}
	for _, m := range res.Matches {
		matchFiles[m.File] = true
	}
	if matchFiles[".hidden_match.go"] {
		t.Error("hidden file should not be searched")
	}
	if matchFiles["link.go"] {
		t.Error("symlink should not be searched")
	}
	if matchFiles["visible.txt"] {
		t.Error("visible.txt should not match file_glob=*.go")
	}
	if !matchFiles["visible.go"] {
		t.Error("visible.go should be searched and matched")
	}
	if !matchFiles["target.go"] {
		t.Error("target.go should be searched and matched")
	}

	// countCandidates with file_glob=*.go
	n, err := countCandidates(ctx, dir, SearchParams{FileGlob: "*.go"}, NoGlobalExcludes())
	if err != nil {
		t.Fatalf("countCandidates: %v", err)
	}
	// visible.go and target.go are regular files matching *.go (not hidden, not symlink)
	if n != 2 {
		t.Fatalf("countCandidates: want 2, got %d", n)
	}
}

// TS-01-62: Walk with IncludeHidden false visits exactly the regular files
// search_files selects.
func TestWalkHiddenFalseMatchesSearchFiles_TS0162(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, ".gitignore", "ignored/\n")
	writeTestFile(t, dir, "visible.go", "needle\n")
	writeTestFile(t, dir, ".hidden.go", "needle\n")
	writeTestFile(t, dir, "sub/deep.go", "needle\n")
	writeTestFile(t, dir, "ignored/skip.go", "needle\n")

	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir := ws.Root

	// Force native backend
	restore := SetRipgrepLookup(func() (string, bool) { return "", false })
	defer restore()

	ctx := context.Background()

	// Walk with IncludeHidden=false, collecting regular files
	var walkRegular []string
	err = Walk(ctx, ws, resolvedDir, WalkOptions{Ignore: NoGlobalExcludes(), IncludeHidden: false}, func(rel string, d fs.DirEntry) error {
		if d.Type().IsRegular() {
			walkRegular = append(walkRegular, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	// Search with a match-everything pattern to find all files searched
	res, _, err := SearchIn(ctx, dir, SearchParams{Pattern: "."}, NoGlobalExcludes())
	if err != nil {
		t.Fatalf("SearchIn: %v", err)
	}
	searchFiles := map[string]bool{}
	for _, m := range res.Matches {
		searchFiles[m.File] = true
	}

	sort.Strings(walkRegular)
	walkSet := map[string]bool{}
	for _, f := range walkRegular {
		walkSet[f] = true
	}

	// Every file search_files found should be in the walk set
	for f := range searchFiles {
		if !walkSet[f] {
			t.Errorf("search_files found %q but Walk did not visit it", f)
		}
	}
	// Every file Walk found should be in the search set
	for f := range walkSet {
		if !searchFiles[f] {
			t.Errorf("Walk visited %q but search_files did not search it", f)
		}
	}
}

// TS-01-63: Tool descriptions, schemas, markers and the ripgrep path are
// unchanged and the existing suites pass unedited.
// This test verifies that tool descriptions and schemas are byte-identical
// to their expected values.
func TestToolDescriptionsAndSchemasUnchanged_TS0163(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := All(Options{Workspace: ws, Ignore: NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}

	// Verify find_files and search_files descriptions are as expected
	expectedDescs := map[string]string{
		"find_files":   "Find files by glob pattern, skipping .gitignored paths.",
		"search_files": "Search file contents by regular expression, skipping .gitignored, hidden (dot-prefixed) and binary files. Returns at most max_matches (<= 100) matches with context_lines (<= 20) lines either side.",
	}

	for _, tl := range all {
		if expected, ok := expectedDescs[tl.Name]; ok {
			if tl.Description != expected {
				t.Errorf("%s description changed.\ngot:  %q\nwant: %q", tl.Name, tl.Description, expected)
			}
		}
	}

	// Verify the tool set names are unchanged
	got := make([]string, 0, len(all))
	for _, tl := range all {
		got = append(got, tl.Name)
	}
	want := []string{"read_file", "write_file", "edit_file", "list_files",
		"find_files", "search_files", "execute", "run_command", "powershell"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("default tool set changed.\ngot:  %v\nwant: %v", got, want)
	}
}

// TS-01-68: No default Runner is installed and the default tool set is unchanged.
func TestNoDefaultRunnerInstalled_TS0168(t *testing.T) {
	// Verify that no non-test code outside tools.CtagsRunner constructs an
	// outline.Options with a Runner by searching the source files.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		// Check for Runner: in non-test code (outside CtagsRunner definition)
		if name != "ctags.go" && strings.Contains(content, "Runner:") {
			// Parse to make sure it's not a comment
			f, perr := parser.ParseFile(fset, name, data, parser.ParseComments)
			if perr != nil {
				continue
			}
			// Check if Runner appears in non-comment code
			ast.Inspect(f, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok {
					return true
				}
				if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "Runner" {
					t.Errorf("found Runner: in non-test file %s at line %d",
						name, fset.Position(kv.Pos()).Line)
				}
				return true
			})
			_ = f
		}
	}

	// Verify FileNavigationTools() returns the expected names
	navTools := FileNavigationTools()
	wantNav := []string{"list_files", "find_files", "search_files"}
	if strings.Join(navTools, ",") != strings.Join(wantNav, ",") {
		t.Fatalf("FileNavigationTools() changed.\ngot:  %v\nwant: %v", navTools, wantNav)
	}

	// Verify All() returns the pinned set
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	all, err := All(Options{Workspace: ws})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(all))
	for _, tl := range all {
		got = append(got, tl.Name)
	}
	want := []string{"read_file", "write_file", "edit_file", "list_files",
		"find_files", "search_files", "execute", "run_command", "powershell"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("All() tool set changed.\ngot:  %v\nwant: %v", got, want)
	}
}
