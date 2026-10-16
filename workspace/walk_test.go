package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// withGlobalExcludes is NoGlobalExcludes() with a global excludes file naming
// patterns, found through the GitConfig hook.
func withGlobalExcludes(t *testing.T, patterns string) IgnoreOptions {
	t.Helper()
	file := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(file, []byte(patterns), 0o644); err != nil {
		t.Fatal(err)
	}
	o := NoGlobalExcludes()
	o.GitConfig = func() string { return file }
	return o
}

// walked is every rel path ws.Walk visits from sub, sorted.
func walked(t *testing.T, ws *Workspace, sub string, opts WalkOptions) []string {
	t.Helper()
	got := []string{}
	err := ws.Walk(context.Background(), sub, opts, func(rel string, _ fs.DirEntry) error {
		got = append(got, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%q): %v", sub, err)
	}
	slices.Sort(got)
	return got
}

// TS-20-20: Walk visits the non-ignored entries under sub with rel relative to
// sub and the engine rooted at sub.
//
// The sets are what tools.Walk visited for this tree before the walk moved.
// They follow the engine: a repository's .git/info/exclude is read where the
// scan root is itself a repository root, so walking "sub" of a root that is a
// repository applies the global layer and sub's own ignore files, and not the
// root's .gitignore or .git/info/exclude.
func TestWalkRootsTheEngineAtSub_TS20_20(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{
		".git/info/exclude": "*.excl\n",
		".gitignore":        "*.gen\n",
		"a.txt":             "",
		"a.gen":             "",
		"b.glob":            "",
		"b.excl":            "",
		"sub/k.txt":         "",
		"sub/x.gen":         "",
		"sub/y.glob":        "",
		"sub/z.excl":        "",
		"other/deep/f.txt":  "",
	})
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.glob\n")})
	if err != nil {
		t.Fatal(err)
	}

	wantRoot := []string{"a.txt", "other", "other/deep", "other/deep/f.txt", "sub", "sub/k.txt"}
	for _, sub := range []string{"", "."} {
		if got := walked(t, ws, sub, WalkOptions{}); !slices.Equal(got, wantRoot) {
			t.Errorf("Walk(%q) = %v, want %v", sub, got, wantRoot)
		}
	}

	wantSub := []string{"k.txt", "x.gen", "z.excl"}
	for _, sub := range []string{"sub", filepath.Join(ws.Root(), "sub")} {
		if got := walked(t, ws, sub, WalkOptions{}); !slices.Equal(got, wantSub) {
			t.Errorf("Walk(%q) = %v, want %v: the root .gitignore must not apply, the global layer must", sub, got, wantSub)
		}
	}
}

// TS-20-21: Walk refuses a sub outside the root, and a nil receiver, before
// calling fn.
func TestWalkRefusesAnOutsideSub_TS20_21(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "ws")
	tree(t, root, map[string]string{"a/f.txt": "", "b/g.txt": ""})
	if err := os.Mkdir(root+"-evil", 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := Open(root, Options{Ignore: NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	count := func(string, fs.DirEntry) error { calls++; return nil }
	for _, sub := range []string{"../x", filepath.Join(base, "x"), ws.Root() + "-evil", "a/../../x"} {
		calls = 0
		if err := ws.Walk(context.Background(), sub, WalkOptions{}, count); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("Walk(%q) err = %v, want ErrPathNotAllowed", sub, err)
		}
		if calls != 0 {
			t.Errorf("Walk(%q) called fn %d times before refusing", sub, calls)
		}
	}

	var nilWS *Workspace
	if err := nilWS.Walk(context.Background(), "", WalkOptions{}, count); !errors.Is(err, ErrPathNotAllowed) {
		t.Errorf("Walk on a nil workspace: err = %v, want ErrPathNotAllowed", err)
	}
	if calls != 0 {
		t.Errorf("a nil workspace called fn %d times", calls)
	}

	for _, sub := range []string{filepath.Join(ws.Root(), "a"), "a/../b"} {
		calls = 0
		if err := ws.Walk(context.Background(), sub, WalkOptions{}, count); err != nil {
			t.Errorf("Walk(%q): %v", sub, err)
		}
		if calls != 1 {
			t.Errorf("Walk(%q) made %d calls, want 1 (the file in it)", sub, calls)
		}
	}
}

// TS-20-22: Walk skips hidden entries by default, does not follow symlinks,
// skips unreadable entries and checks sub lexically.
func TestWalkHiddenSymlinksUnreadableAndLexicalSub_TS20_22(t *testing.T) {
	outside := t.TempDir()
	tree(t, outside, map[string]string{"secret.txt": "x"})
	ws := openIgnoring(t, t.TempDir())
	r := ws.Root()
	tree(t, r, map[string]string{"a.txt": "", ".hidden/f": "", "locked/inside.txt": ""})
	mustSymlink(t, outside, filepath.Join(r, "linkout"))

	locked := filepath.Join(r, "locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	enforced := true
	if _, err := os.ReadDir(locked); err == nil {
		enforced = false // running as root, or a filesystem that ignores modes
	}

	plain := walked(t, ws, "", WalkOptions{})
	for _, hidden := range []string{".hidden", ".hidden/f"} {
		if slices.Contains(plain, hidden) {
			t.Errorf("%q visited without IncludeHidden", hidden)
		}
	}
	all := walked(t, ws, "", WalkOptions{IncludeHidden: true})
	for _, hidden := range []string{".hidden", ".hidden/f"} {
		if !slices.Contains(all, hidden) {
			t.Errorf("%q not visited with IncludeHidden", hidden)
		}
	}
	for _, got := range [][]string{plain, all} {
		if !slices.Contains(got, "linkout") {
			t.Errorf("the symlink entry itself was not visited: %v", got)
		}
		if slices.Contains(got, "linkout/secret.txt") {
			t.Errorf("a symlinked directory's contents were visited: %v", got)
		}
		if enforced && slices.Contains(got, "locked/inside.txt") {
			t.Errorf("an unreadable directory's contents were visited: %v", got)
		}
	}

	// A sub that is an in-root symlink to an outside directory is accepted
	// lexically, not resolved, and not refused: it names a link, which is not
	// followed, so nothing under it is visited.
	if got := walked(t, ws, "linkout", WalkOptions{IncludeHidden: true}); len(got) != 0 {
		t.Errorf("Walk of a symlinked sub visited %v", got)
	}
}

// TS-20-23: Walk of a missing sub or a regular file makes no calls and returns
// nil.
func TestWalkOfAMissingSubOrAFile_TS20_23(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{"a.txt": "x"})
	ws := openIgnoring(t, root)
	for _, sub := range []string{"missing", "a.txt"} {
		calls := 0
		err := ws.Walk(context.Background(), sub, WalkOptions{}, func(string, fs.DirEntry) error { calls++; return nil })
		if err != nil {
			t.Errorf("Walk(%q) = %v, want nil", sub, err)
		}
		if calls != 0 {
			t.Errorf("Walk(%q) made %d calls, want 0", sub, calls)
		}
	}
}

// TS-20-24: Walk returns the context error at the next entry when cancelled.
func TestWalkReturnsTheContextError_TS20_24(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{}
	for i := 0; i < 100; i++ {
		files[fmt.Sprintf("f%03d.txt", i)] = ""
	}
	tree(t, root, files)
	ws := openIgnoring(t, root)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	err := ws.Walk(ctx, "", WalkOptions{}, func(string, fs.DirEntry) error {
		calls++
		if calls == 3 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 3 {
		t.Errorf("cancelled on the 3rd call: err = %v after %d calls, want context.Canceled after 3", err, calls)
	}

	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	calls = 0
	err = ws.Walk(expired, "", WalkOptions{}, func(string, fs.DirEntry) error { calls++; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Errorf("expired deadline: err = %v after %d calls, want context.DeadlineExceeded after 0", err, calls)
	}
}

// TS-20-25: Walk honours SkipDir and SkipAll and returns any other fn error.
func TestWalkHonoursSkipsAndErrors_TS20_25(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{"d1/f1": "", "d1/f2": "", "d2/g1": "", "d2/g2": ""})
	ws := openIgnoring(t, root)

	var got []string
	err := ws.Walk(context.Background(), "", WalkOptions{}, func(rel string, d fs.DirEntry) error {
		got = append(got, rel)
		if rel == "d1" {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Errorf("SkipDir: err = %v, want nil", err)
	}
	if slices.Contains(got, "d1/f1") || !slices.Contains(got, "d2/g1") {
		t.Errorf("SkipDir at d1 visited %v, want d2's files and none of d1's", got)
	}

	got = nil
	err = ws.Walk(context.Background(), "", WalkOptions{}, func(rel string, d fs.DirEntry) error {
		got = append(got, rel)
		if !d.IsDir() {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Errorf("SkipAll: err = %v, want nil", err)
	}
	if n := len(got); n != 2 || got[0] != "d1" || got[1] != "d1/f1" {
		t.Errorf("SkipAll at the first file visited %v, want [d1 d1/f1]", got)
	}

	sentinel := errors.New("stop here")
	got = nil
	err = ws.Walk(context.Background(), "", WalkOptions{}, func(rel string, _ fs.DirEntry) error {
		got = append(got, rel)
		if rel == "d1/f2" {
			return sentinel
		}
		return nil
	})
	if err != sentinel {
		t.Errorf("an fn error: err = %v, want exactly the error fn returned", err)
	}
	if slices.Contains(got, "d2") {
		t.Errorf("the walk went on after fn's error: %v", got)
	}
}

// TS-20-26: WalkOptions has one field, IncludeHidden, and Walk always applies
// the workspace's own rules.
func TestWalkAppliesTheWorkspacesOwnRules_TS20_26(t *testing.T) {
	typ := reflect.TypeFor[WalkOptions]()
	if typ.NumField() != 1 || typ.Field(0).Name != "IncludeHidden" || typ.Field(0).Type.Kind() != reflect.Bool {
		t.Fatalf("WalkOptions = %v, want exactly IncludeHidden bool", typ)
	}

	root := t.TempDir()
	tree(t, root, map[string]string{"x.secret": "", "ok.txt": "", ".hidden.secret": ""})
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.secret\n")})
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []bool{false, true} {
		got := walked(t, ws, "", WalkOptions{IncludeHidden: hidden})
		for _, rel := range got {
			if rel == "x.secret" || rel == ".hidden.secret" {
				t.Errorf("IncludeHidden=%v visited %q, which the workspace's global excludes name", hidden, rel)
			}
		}
		if !slices.Contains(got, "ok.txt") {
			t.Errorf("IncludeHidden=%v did not visit ok.txt: %v", hidden, got)
		}
	}
}
