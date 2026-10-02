package tools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// TS-01-50: The walk applies the ignore engine, hidden switch, relative slash
// paths and nested ignore files, and calls back for directories.
func TestWalkIgnoreHiddenRelPathsAndDirs_TS0150(t *testing.T) {
	root := t.TempDir()

	// Build a tree:
	//   .gitignore          (ignores "ignored/")
	//   visible.txt
	//   .hidden_file
	//   .hidden_dir/
	//     inside.txt
	//   sub/
	//     .gitignore        (ignores "nested_ignored.txt")
	//     kept.txt
	//     nested_ignored.txt
	//   ignored/
	//     should_not_appear.txt
	//   .git/
	//     HEAD

	mkFile := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mkFile(".gitignore", "ignored/\n")
	mkFile("visible.txt", "hello")
	mkFile(".hidden_file", "secret")
	mkFile(".hidden_dir/inside.txt", "inner")
	mkFile("sub/.gitignore", "nested_ignored.txt\n")
	mkFile("sub/kept.txt", "ok")
	mkFile("sub/nested_ignored.txt", "nope")
	mkFile("ignored/should_not_appear.txt", "gone")
	mkFile(".git/HEAD", "ref: refs/heads/main\n")

	// --- hidden excluded ---
	ctx := context.Background()
	var relsNoHidden []string
	var dirsNoHidden []string
	err := walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		relsNoHidden = append(relsNoHidden, rel)
		if d.IsDir() {
			dirsNoHidden = append(dirsNoHidden, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk (hidden=false): %v", err)
	}
	sort.Strings(relsNoHidden)

	// Slash-separated, no root entry
	for _, r := range relsNoHidden {
		if r == "." || r == "" {
			t.Errorf("root entry should not appear: %q", r)
		}
		if filepath.ToSlash(r) != r {
			t.Errorf("rel not slash-separated: %q", r)
		}
	}

	// Ignored entries absent
	assertNotContains(t, relsNoHidden, "ignored")
	assertNotContains(t, relsNoHidden, "ignored/should_not_appear.txt")
	assertNotContains(t, relsNoHidden, "sub/nested_ignored.txt")

	// .git always ignored
	assertNotContains(t, relsNoHidden, ".git")
	assertNotContains(t, relsNoHidden, ".git/HEAD")

	// Hidden entries absent when hidden excluded
	assertNotContains(t, relsNoHidden, ".hidden_file")
	assertNotContains(t, relsNoHidden, ".hidden_dir")
	assertNotContains(t, relsNoHidden, ".hidden_dir/inside.txt")
	assertNotContains(t, relsNoHidden, ".gitignore")

	// Visible entries present
	assertContains(t, relsNoHidden, "visible.txt")
	assertContains(t, relsNoHidden, "sub")
	assertContains(t, relsNoHidden, "sub/kept.txt")

	// Directories are called back
	if len(dirsNoHidden) == 0 {
		t.Error("expected directories in callback, got none")
	}
	assertContains(t, dirsNoHidden, "sub")

	// --- hidden included ---
	var relsWithHidden []string
	err = walk(ctx, root, NoGlobalExcludes(), true, func(rel string, d fs.DirEntry) error {
		relsWithHidden = append(relsWithHidden, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk (hidden=true): %v", err)
	}
	sort.Strings(relsWithHidden)

	// .hidden_file and .hidden_dir present
	assertContains(t, relsWithHidden, ".hidden_file")
	assertContains(t, relsWithHidden, ".hidden_dir")
	assertContains(t, relsWithHidden, ".hidden_dir/inside.txt")
	assertContains(t, relsWithHidden, ".gitignore")

	// .git still absent (ignore engine matches it)
	assertNotContains(t, relsWithHidden, ".git")
	assertNotContains(t, relsWithHidden, ".git/HEAD")

	// Ignored entries still absent
	assertNotContains(t, relsWithHidden, "ignored")
	assertNotContains(t, relsWithHidden, "ignored/should_not_appear.txt")
	assertNotContains(t, relsWithHidden, "sub/nested_ignored.txt")
}

// TS-01-51: Missing roots and unreadable entries yield a nil error.
func TestWalkMissingRootAndUnreadable_TS0151(t *testing.T) {
	ctx := context.Background()

	// Missing root: zero callbacks, nil error
	missing := filepath.Join(t.TempDir(), "does_not_exist")
	var calls int
	err := walk(ctx, missing, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("missing root: want nil error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("missing root: want 0 calls, got %d", calls)
	}

	// Unreadable directory: skipped, other entries visited
	if runtime.GOOS == "windows" {
		t.Skip("permission-based skip not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root; permission test not meaningful")
	}

	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "a")
	unreadable := filepath.Join(root, "noperm")
	if err := os.Mkdir(unreadable, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "noperm/inside.txt", "x")
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(unreadable, 0o755) })

	var visited []string
	err = walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		visited = append(visited, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("unreadable dir: want nil error, got %v", err)
	}
	assertContains(t, visited, "a.txt")
	// The unreadable dir itself may or may not appear (it's listed by the parent's ReadDir),
	// but its children should not appear because WalkDir can't read it.
	assertNotContains(t, visited, "noperm/inside.txt")
}

// TS-01-52: A cancelled context stops the walk with ctx.Err().
func TestWalkCancelledContext_TS0152(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "a.txt", "a")
	writeTestFile(t, root, "b.txt", "b")
	writeTestFile(t, root, "c.txt", "c")

	// Cancel from inside the callback after the first entry
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls int
	err := walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		calls++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("want 1 call before cancel, got %d", calls)
	}

	// Already cancelled before the walk
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	calls = 0
	err = walk(ctx2, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		calls++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: want context.Canceled, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("pre-cancelled: want 0 calls, got %d", calls)
	}
}

// TS-01-53: SkipDir, SkipAll and callback errors have fs.WalkDirFunc semantics.
func TestWalkSkipDirSkipAllError_TS0153(t *testing.T) {
	root := t.TempDir()
	// a/x.txt, b/y.txt
	writeTestFile(t, root, "a/x.txt", "x")
	writeTestFile(t, root, "b/y.txt", "y")

	ctx := context.Background()

	// SkipDir on directory "a": a's children skipped, b visited
	var rels []string
	err := walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		rels = append(rels, rel)
		if rel == "a" && d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SkipDir: want nil, got %v", err)
	}
	assertContains(t, rels, "a")
	assertNotContains(t, rels, "a/x.txt")
	assertContains(t, rels, "b")
	assertContains(t, rels, "b/y.txt")

	// SkipAll at first file: walk ends with nil error
	rels = nil
	err = walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		rels = append(rels, rel)
		if !d.IsDir() {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SkipAll: want nil, got %v", err)
	}
	// At most one file should have been seen (the first one encountered)
	fileCount := 0
	for _, r := range rels {
		if r == "a/x.txt" || r == "b/y.txt" {
			fileCount++
		}
	}
	if fileCount > 1 {
		t.Fatalf("SkipAll: expected at most 1 file, got %d in %v", fileCount, rels)
	}

	// Sentinel error: returned unchanged
	sentinel := errors.New("test sentinel")
	err = walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("sentinel: want exact sentinel, got %v", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("sentinel: errors.Is failed")
	}
}

// TS-01-54: Walk refuses a nil workspace, a relative root or a root outside
// the workspace with ErrPathNotAllowed before any callback.
func TestWalkRefusesInvalidRoots_TS0154(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Use the resolved root so symlinks (e.g. /var -> /private/var on macOS)
	// don't cause a mismatch.
	resolvedDir := ws.Root

	outside := t.TempDir() // a different temp dir, outside ws
	ctx := context.Background()
	var calls int
	cb := func(rel string, d fs.DirEntry) error {
		calls++
		return nil
	}

	// nil workspace
	calls = 0
	err = Walk(ctx, nil, resolvedDir, WalkOptions{}, cb)
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("nil ws: want ErrPathNotAllowed, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("nil ws: callback should not be called, got %d calls", calls)
	}

	// relative root
	calls = 0
	err = Walk(ctx, ws, "relative/path", WalkOptions{}, cb)
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("relative root: want ErrPathNotAllowed, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("relative root: callback should not be called, got %d calls", calls)
	}

	// root outside workspace
	calls = 0
	// Resolve outside through symlinks too, so it's a valid absolute path
	outsideResolved, _ := filepath.EvalSymlinks(outside)
	if outsideResolved == "" {
		outsideResolved = outside
	}
	err = Walk(ctx, ws, outsideResolved, WalkOptions{}, cb)
	if !errors.Is(err, ErrPathNotAllowed) {
		t.Fatalf("outside root: want ErrPathNotAllowed, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("outside root: callback should not be called, got %d calls", calls)
	}

	// Valid root inside workspace: should succeed
	writeTestFile(t, resolvedDir, "hello.txt", "hi")
	calls = 0
	err = Walk(ctx, ws, resolvedDir, WalkOptions{Ignore: NoGlobalExcludes()}, cb)
	if err != nil {
		t.Fatalf("valid root: want nil, got %v", err)
	}
	if calls == 0 {
		t.Fatal("valid root: expected at least one callback")
	}
}

// TS-01-55: The walk does not follow symlinks.
func TestWalkDoesNotFollowSymlinks_TS0155(t *testing.T) {
	root := t.TempDir()
	// Create a target directory with a file
	writeTestFile(t, root, "target/inner.txt", "inside")
	// Create a symlink to the target directory
	link := filepath.Join(root, "link")
	if err := os.Symlink(filepath.Join(root, "target"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// Also create a symlink to a file
	writeTestFile(t, root, "real.txt", "real")
	fileLink := filepath.Join(root, "file_link.txt")
	if err := os.Symlink(filepath.Join(root, "real.txt"), fileLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	ctx := context.Background()
	var rels []string
	err := walk(ctx, root, NoGlobalExcludes(), false, func(rel string, d fs.DirEntry) error {
		rels = append(rels, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// The symlink entries themselves are called back
	assertContains(t, rels, "link")
	assertContains(t, rels, "file_link.txt")

	// But the target's children are NOT visited through the symlink
	assertNotContains(t, rels, "link/inner.txt")

	// The real target directory IS visited
	assertContains(t, rels, "target")
	assertContains(t, rels, "target/inner.txt")
}

// --- helpers ---

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, slice []string, want string) {
	t.Helper()
	for _, s := range slice {
		if s == want {
			return
		}
	}
	t.Errorf("expected %q in %v", want, slice)
}

func assertNotContains(t *testing.T, slice []string, unwanted string) {
	t.Helper()
	for _, s := range slice {
		if s == unwanted {
			t.Errorf("did not expect %q in %v", unwanted, slice)
			return
		}
	}
}
