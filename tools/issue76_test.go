package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/agentfox/agentkit-go/outline"
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
	fs, _, err := outline.OutlineMany(context.Background(), []outline.Source{{Abs: p}}, outline.Options{})
	if err != nil || fs[0].Backend != outline.BackendNone {
		t.Fatalf("OutlineMany = %+v, %v; want none without reading", fs, err)
	}
}

// Issue #76 §3: the `ctags --version` probe has a deadline. A wedged ctags
// is unavailable, not a hang under the symbol table's lock.
func TestCtagsProbeHasADeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell-script ctags is not portable to Windows")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "ctags"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if p, err := exec.LookPath("ctags"); err != nil || filepath.Dir(p) != dir {
		t.Skip("the fake ctags is not first on PATH")
	}
	old := ctagsProbeTimeout
	ctagsProbeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ctagsProbeTimeout = old })

	run := CtagsRunner(nil)

	// A caller whose own context ends first gets its context error, and the
	// result is not cached as "unavailable".
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := run(ctx, []string{"x"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the probe outlived the caller's context")
	}

	start = time.Now()
	_, err := run(context.Background(), []string{"x"})
	if !errors.Is(err, ErrCtagsUnavailable) {
		t.Fatalf("err = %v, want ErrCtagsUnavailable for a ctags that never answers --version", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the probe took %v; it must give up at its deadline", time.Since(start))
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
		Symbols: SymbolOptions{DisableCtags: true}}.withDefaults())
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

// Issue #76 §5: neither search backend gives up on a long line. Native
// search stopped reading a file at its first line over 1 MiB; the ripgrep
// backend failed the whole search on a match event over 4 MiB. Both must
// find a match on the long line and one after it, as ripgrep itself does.
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
	if rg, lerr := exec.LookPath("rg"); lerr == nil {
		res, err = searchRipgrep(context.Background(), rg, dir, p, NoGlobalExcludes())
		check("ripgrep", res, err)
	}
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
		Symbols: SymbolOptions{DisableCtags: true}}.withDefaults())

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

// The ripgrep backend's reader, without a ripgrep install: a stand-in that
// replays ripgrep's JSON event stream, whose first match event carries a
// 5 MiB line.
func TestRipgrepJSONReaderTakesALongEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell-script stand-in is not portable to Windows")
	}
	dir := t.TempDir()
	long := "var x = \"" + strings.Repeat("a", 5<<20) + "\"; NEEDLE_ONE"
	event := func(typ string, line int, text string) string {
		b, _ := json.Marshal(map[string]any{"type": typ, "data": map[string]any{
			"path": map[string]any{"text": "min.js"}, "lines": map[string]any{"text": text + "\n"},
			"line_number": line}})
		return string(b)
	}
	stream := strings.Join([]string{
		`{"type":"begin","data":{"path":{"text":"min.js"}}}`,
		event("match", 1, long),
		event("match", 3, "NEEDLE_TWO"),
		`{"type":"end","data":{"path":{"text":"min.js"}}}`,
	}, "\n") + "\n"
	events := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(events, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	rg := filepath.Join(dir, "rg")
	if err := os.WriteFile(rg, []byte("#!/bin/sh\ncat '"+events+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := searchRipgrep(context.Background(), rg, dir, SearchParams{Pattern: "NEEDLE_", MaxMatches: 10}, NoGlobalExcludes())
	if err != nil {
		t.Fatalf("searchRipgrep: %v", err)
	}
	if len(res.Matches) != 2 || res.Matches[0].Line != 1 || res.Matches[1].Line != 3 {
		t.Fatalf("matches %+v, want lines 1 and 3", res.Matches)
	}
}
