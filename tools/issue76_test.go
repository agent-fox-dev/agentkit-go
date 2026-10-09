package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/outline"
)

// Issue #76 §1: a nested repository's .git directory is as private as the
// root's — find_files and Walk must not list its config, HEAD or objects.
func TestNestedGitDirectoriesAreNeverListed(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "vendor/dep/.git/HEAD", "ref: refs/heads/main\n")
	writeTestFile(t, dir, "vendor/dep/.git/config", "[remote \"origin\"]\n\turl = https://token@example.com/x\n")
	writeTestFile(t, dir, "vendor/dep/.git/objects/ab/cdef", "blob")
	writeTestFile(t, dir, "vendor/dep/ok.go", "package dep\n")
	writeTestFile(t, dir, "sub/mod/.git", "gitdir: ../../.git/modules/mod\n") // a submodule's .git file
	writeTestFile(t, dir, "sub/mod/main.go", "package mod\n")

	for _, got := range [][]string{runFindFilesAny(t, dir, "**"), walkWorkspace(t, dir)} {
		for _, p := range got {
			if p == ".git" || strings.HasPrefix(p, ".git/") || strings.Contains(p, "/.git/") || strings.HasSuffix(p, "/.git") {
				t.Errorf("listed %q: a .git entry at any depth must be excluded", p)
			}
		}
		if !sliceHas(got, "vendor/dep/ok.go") || !sliceHas(got, "sub/mod/main.go") {
			t.Errorf("the nested repositories' own files must still be listed: %v", got)
		}
	}
}

func walkWorkspace(t *testing.T, dir string) []string {
	t.Helper()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	err = Walk(context.Background(), ws, ws.Root, WalkOptions{Ignore: NoGlobalExcludes(), IncludeHidden: true},
		func(rel string, _ fs.DirEntry) error { out = append(out, rel); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sliceHas(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

// Issue #76 §2: file_outline decides "too large" from the size it already
// stat'ed, before reading. The oversized file here is unreadable, so any
// read of it fails the call.
func TestFileOutlineDoesNotReadAnOversizedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not make a file unreadable on Windows")
	}
	root := t.TempDir()
	p := filepath.Join(root, "bundle.js")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, 2<<20); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	if f, err := os.Open(p); err == nil {
		f.Close()
		t.Skip("running with privileges that read a mode-000 file")
	}

	r := makeOutlineTool(t, root)(context.Background(), json.RawMessage(`{"path":"bundle.js"}`))
	if !r.OK || !strings.Contains(r.Text, "file too large") {
		t.Fatalf("file_outline = ok=%v error=%s detail=%s text=%q; want a none outline saying "+
			"\"file too large\" without reading the file", r.OK, r.Error, r.Detail, r.Text)
	}

	f, err := outline.Outline(context.Background(), p, nil, outline.Options{})
	if err != nil || f.Backend != outline.BackendNone {
		t.Fatalf("outline.Outline(src=nil) = %+v, %v; want none without reading", f, err)
	}
	fs, err := outline.OutlineMany(context.Background(), []outline.Source{{Abs: p}}, outline.Options{})
	if err != nil || fs[0].Backend != outline.BackendNone {
		t.Fatalf("OutlineMany = %+v, %v; want none without reading", fs, err)
	}
}

// Issue #76 §5: in the fold path, an edit that was found exactly keeps its
// exact semantics — it is not re-matched by line and applied when on its own
// it would be rejected as not unique.
func TestFoldedBatchKeepsExactEditsExact(t *testing.T) {
	content := "a := “quoted”\nreturn nil\nif x {\n\treturn nil // again\n}\n"
	_, _, err := ApplyEdits(content, []Edit{
		{OldString: `a := "quoted"`, NewString: `a := "fixed"`}, // needs the fold
		{OldString: "return nil", NewString: "return err"},      // found twice exactly
	})
	var ee *EditError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v; a batch whose exact edit is not unique must be rejected", err)
	}

	// An exact edit that is unique still applies alongside a folded one, at
	// its exact position and nothing more.
	out, n, err := ApplyEdits(content, []Edit{
		{OldString: `a := "quoted"`, NewString: `a := "fixed"`},
		{OldString: "nil // again", NewString: "err // again"},
	})
	if err != nil || n != 2 {
		t.Fatalf("ApplyEdits = %d, %v", n, err)
	}
	want := "a := \"fixed\"\nreturn nil\nif x {\n\treturn err // again\n}\n"
	if out != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
}

// Issue #76 §5: edit_file on a path that does not exist changed nothing, so
// it marks nothing dirty.
func TestEditFileOnAMissingPathMarksNothing(t *testing.T) {
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{Workspace: ws, Ignore: NoGlobalExcludes(),
		Symbols: SymbolOptions{}}.withDefaults())
	ft.table = &symbolTable{}
	args, _ := json.Marshal(map[string]any{"path": "nope.go",
		"edits": []map[string]string{{"old_string": "a", "new_string": "b"}}})
	if r := ft.editFile().Execute(context.Background(), args); r.OK {
		t.Fatal("edit_file on a missing file succeeded")
	}
	if dirty, all, _ := ft.table.snapshotMarks(); len(dirty) != 0 || all {
		t.Fatalf("a failed edit of a missing file marked %v (all=%v) dirty", dirty, all)
	}
}

// Issue #76 §5: search does not give up on a long line. It stopped reading a
// file at its first line over 1 MiB; it must find a match on the long line and
// one after it.
func TestSearchReadsPastALongLine(t *testing.T) {
	dir := t.TempDir()
	long := "var x = \"" + strings.Repeat("a", 5<<20) + "\"; NEEDLE_ONE\n"
	writeTestFile(t, dir, "min.js", long+"after\nNEEDLE_TWO\n")
	p := SearchParams{Pattern: "NEEDLE_", MaxMatches: 10}

	check := func(backend string, res SearchResult, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", backend, err)
		}
		var lines []int
		for _, m := range res.Matches {
			lines = append(lines, m.Line)
		}
		if len(lines) != 2 || lines[0] != 1 || lines[1] != 3 {
			t.Fatalf("%s: matches on lines %v, want [1 3]", backend, lines)
		}
	}
	res, err := searchNative(context.Background(), dir, p, NoGlobalExcludes())
	check("native", res, err)
}

// Issue #76 §5: write_file and edit_file replace a file atomically (a new
// file renamed over the old), keep its mode, and still write through a
// symlink to its target inside the workspace.
func TestWritesAreAtomicAndKeepModeAndLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits and symlinks differ on Windows")
	}
	root := t.TempDir()
	ws, err := NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTools(Options{Workspace: ws, Ignore: NoGlobalExcludes(),
		Symbols: SymbolOptions{}}.withDefaults())

	script := filepath.Join(ws.Root, "run.sh")
	if err := os.WriteFile(script, []byte("echo old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(script)

	args, _ := json.Marshal(map[string]any{"path": "run.sh",
		"edits": []map[string]string{{"old_string": "old", "new_string": "new"}}})
	if r := ft.editFile().Execute(context.Background(), args); !r.OK {
		t.Fatalf("edit_file: %s %s", r.Error, r.Detail)
	}
	after, _ := os.Stat(script)
	if os.SameFile(before, after) {
		t.Error("edit_file rewrote the file in place; a crash mid-write would truncate it")
	}
	if after.Mode().Perm() != 0o755 {
		t.Errorf("mode %v after edit_file, want 0755 kept", after.Mode().Perm())
	}

	target := filepath.Join(ws.Root, "real.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws.Root, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	args, _ = json.Marshal(map[string]any{"path": "link.txt", "content": "through the link"})
	if r := ft.writeFile().Execute(context.Background(), args); !r.OK {
		t.Fatalf("write_file: %s %s", r.Error, r.Detail)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("write_file replaced the symlink with a regular file")
	}
	if b, _ := os.ReadFile(target); string(b) != "through the link" {
		t.Errorf("target holds %q; the write must land on the link's target", b)
	}
	if fi, _ := os.Stat(target); fi.Mode().Perm() != 0o600 {
		t.Errorf("target mode %v, want 0600 kept", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(ws.Root)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".agentkit-") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
}
