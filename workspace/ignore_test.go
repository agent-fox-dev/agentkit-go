package workspace

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// openIgnoring opens a workspace over dir with no global excludes.
func openIgnoring(t *testing.T, dir string) *Workspace {
	t.Helper()
	ws, err := Open(dir, Options{Ignore: NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// countingGit is an IgnoreOptions whose every hook counts its calls.
type countingGit struct {
	getenv, home, gitConfig atomic.Int32
	path                    string
}

func (c *countingGit) options() IgnoreOptions {
	return IgnoreOptions{
		Getenv:    func(string) string { c.getenv.Add(1); return "" },
		Home:      func() (string, error) { c.home.Add(1); return "", os.ErrNotExist },
		GitConfig: func() string { c.gitConfig.Add(1); return c.path },
	}
}

// TS-20-5: Open reads no ignore file and starts no subprocess.
func TestOpenReadsNoIgnoreFileAndStartsNoProcess_TS20_5(t *testing.T) {
	root := t.TempDir()
	// A .gitignore that blocks forever when opened for reading, where the
	// platform has named pipes.
	trapped := mkfifo(filepath.Join(root, ".gitignore")) == nil

	bin := t.TempDir()
	log := filepath.Join(bin, "git.log")
	script := "#!/bin/sh\necho called >> " + log + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	counting := &countingGit{}
	for name, opts := range map[string]IgnoreOptions{"hooks": counting.options(), "zero": {}} {
		done := make(chan error, 1)
		go func() {
			_, err := Open(root, Options{Ignore: opts})
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s: Open: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			if !trapped {
				t.Fatalf("%s: Open did not return", name)
			}
			t.Fatalf("%s: Open opened the .gitignore", name)
		}
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("Open started the git subprocess")
	}
	if n := counting.getenv.Load() + counting.home.Load() + counting.gitConfig.Load(); n != 0 {
		t.Errorf("Open called its ignore hooks %d times, want 0", n)
	}
}

// TS-20-15: IgnoreOptions has the documented fields and NoGlobalExcludes
// empties the global layer.
func TestIgnoreOptionsAndNoGlobalExcludes_TS20_15(t *testing.T) {
	typ := reflect.TypeFor[IgnoreOptions]()
	for name, want := range map[string]reflect.Type{
		"Getenv":    reflect.TypeFor[func(string) string](),
		"Home":      reflect.TypeFor[func() (string, error)](),
		"GitConfig": reflect.TypeFor[func() string](),
	} {
		f, ok := typ.FieldByName(name)
		if !ok || f.Type != want {
			t.Errorf("IgnoreOptions.%s = %v, want %v", name, f.Type, want)
		}
	}
	if typ.NumField() != 3 {
		t.Errorf("IgnoreOptions has %d fields, want 3", typ.NumField())
	}

	no := NoGlobalExcludes()
	if no.GitConfig == nil || no.GitConfig() != "" {
		t.Fatalf("NoGlobalExcludes().GitConfig() is not the empty string")
	}

	// A global excludes file present in the environment: the XDG location is
	// the one git itself reads by default.
	xdg := t.TempDir()
	writeFile(t, xdg, "git/ignore", "*.zz\n")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	root := t.TempDir()

	control, err := Open(root, Options{Ignore: IgnoreOptions{GitConfig: func() string { return "" }}})
	if err != nil {
		t.Fatal(err)
	}
	if !control.Ignored("a.zz", false) {
		t.Fatal("test setup: the global excludes file in XDG_CONFIG_HOME is not read by the zero options")
	}
	if openIgnoring(t, root).Ignored("a.zz", false) {
		t.Error("a workspace opened with NoGlobalExcludes() honoured a global excludes file")
	}
}

// TS-20-17: Ignore() returns options whose GitConfig is memoized and reusable
// without another lookup.
func TestIgnoreReturnsMemoizedOptions_TS20_17(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global-ignore")
	writeFile(t, dir, "global-ignore", "*.glob\n")
	root := filepath.Join(dir, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	counting := &countingGit{path: global}
	ws, err := Open(root, Options{Ignore: counting.options()})
	if err != nil {
		t.Fatal(err)
	}
	opts := ws.Ignore()
	for i := 0; i < 3; i++ {
		if got := opts.GitConfig(); got != global {
			t.Fatalf("GitConfig() = %q, want %q", got, global)
		}
	}
	if n := counting.gitConfig.Load(); n > 1 {
		t.Fatalf("the lookup ran %d times, want at most 1", n)
	}

	ws2, err := Open(root, Options{Ignore: opts})
	if err != nil {
		t.Fatal(err)
	}
	if !ws2.Ignored("x.glob", false) {
		t.Error("the second workspace did not use the global excludes file")
	}
	if n := counting.gitConfig.Load(); n > 1 {
		t.Fatalf("the lookup ran %d times in total, want at most 1", n)
	}
	if ws.Ignore().Getenv == nil || ws.Ignore().Home == nil {
		t.Error("Ignore() dropped the Getenv or Home hook")
	}
}

// tree writes the entries under dir; a name ending in "/" is a directory.
func tree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(rel)), 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		writeFile(t, dir, rel, body)
	}
}

// TS-20-27: Ignored matches rel and every ancestor using the ancestors' ignore
// files from the root down.
func TestIgnoredUsesAncestorsIgnoreFiles_TS20_27(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{
		".gitignore":      "vendor/\n",
		"a/.gitignore":    "*.o\n!keep.o\n",
		"a/repo/.git/":    "",
		"a/repo/z.o":      "",
		"vendor/x/y.go":   "",
		"a/b.o":           "",
		"a/keep.o":        "",
		"a/repo/.gitkeep": "",
	})
	ws := openIgnoring(t, root)

	for _, c := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"vendor/x/y.go", false, true},
		{"vendor", true, true},
		{"a/b.o", false, true},
		{"a/keep.o", false, false},
		{`a\b.o`, false, true},
		{`a\keep.o`, false, false},
		{"a/repo/z.o", false, false},
		{"a", true, false},
	} {
		if got := ws.Ignored(c.rel, c.isDir); got != c.want {
			t.Errorf("Ignored(%q, %v) = %v, want %v", c.rel, c.isDir, got, c.want)
		}
	}
}

// TS-20-28: Ignored reports .git segments as ignored and the root as not
// ignored.
func TestIgnoredGitSegmentsAndRoot_TS20_28(t *testing.T) {
	ws := openIgnoring(t, t.TempDir())
	for _, c := range []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{".git", true, true},
		{"a/.git/config", false, true},
		{"x/.git", true, true},
		{"", true, false},
		{".", true, false},
	} {
		if got := ws.Ignored(c.rel, c.isDir); got != c.want {
			t.Errorf("Ignored(%q, %v) = %v, want %v", c.rel, c.isDir, got, c.want)
		}
	}
}

// TS-20-29: Ignored fails closed for absolute and escaping paths.
func TestIgnoredFailsClosed_TS20_29(t *testing.T) {
	ws := openIgnoring(t, t.TempDir())
	for _, p := range []string{filepath.Join(ws.Root(), "a"), "/etc/passwd", "../x", "a/../../x", ".."} {
		if !ws.Ignored(p, false) {
			t.Errorf("Ignored(%q) = false, want true (fail closed)", p)
		}
	}
}

// TS-20-30: A dot-prefixed name is not reported ignored on account of its
// leading dot.
func TestIgnoredDotNamesAreNotRules_TS20_30(t *testing.T) {
	ws := openIgnoring(t, t.TempDir())
	for _, c := range []struct {
		rel   string
		isDir bool
	}{{".env", false}, {".config/app.toml", false}, {".github", true}} {
		if ws.Ignored(c.rel, c.isDir) {
			t.Errorf("Ignored(%q, %v) = true, want false", c.rel, c.isDir)
		}
	}
}

// TS-20-31: Ignored keeps no cache: an edited .gitignore changes the next
// answer.
func TestIgnoredKeepsNoCache_TS20_31(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{"a/.gitignore": "*.x\n", "a/f.x": "", ".git/info/": ""})
	ws := openIgnoring(t, root)

	if !ws.Ignored("a/f.x", false) {
		t.Fatal("first call: a/f.x is not ignored by a/.gitignore")
	}
	writeFile(t, root, "a/.gitignore", "")
	if ws.Ignored("a/f.x", false) {
		t.Fatal("second call: still ignored after a/.gitignore was emptied")
	}
	writeFile(t, root, ".git/info/exclude", "*.x\n")
	if !ws.Ignored("a/f.x", false) {
		t.Fatal("third call: not ignored after .git/info/exclude gained *.x")
	}
}

// TS-20-16: At most one core.excludesFile lookup per workspace, none before
// first use, including under concurrent first use.
func TestOneLookupPerWorkspace_TS20_16(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{"a/f.txt": "", "b.txt": ""})
	counting := &countingGit{}
	ws, err := Open(root, Options{Ignore: counting.options()})
	if err != nil {
		t.Fatal(err)
	}
	if n := counting.gitConfig.Load(); n != 0 {
		t.Fatalf("the lookup ran %d times by the end of Open, want 0", n)
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				ws.Ignored("a/f.txt", false)
				return
			}
			_ = ws.Walk(context.Background(), "", WalkOptions{}, func(string, fs.DirEntry) error { return nil })
		}()
	}
	wg.Wait()
	if n := counting.gitConfig.Load(); n != 1 {
		t.Fatalf("the lookup ran %d times after 32 goroutines first used the rules at once, want exactly 1", n)
	}

	ws.Ignored("b.txt", false)
	_ = ws.Walk(context.Background(), "a", WalkOptions{}, func(string, fs.DirEntry) error { return nil })
	if n := counting.gitConfig.Load(); n != 1 {
		t.Fatalf("the lookup ran %d times in total after further use, want 1", n)
	}
}

// TS-20-18: Ignore sources apply in precedence order and the last match wins.
func TestIgnoreSourcesApplyInPrecedenceOrder_TS20_18(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{
		".git/info/exclude": "!keep.log\n",
		".gitignore":        "build/\n",
		"sub/.gitignore":    "!important.log\n*.tmp\n",
		"a.log":             "",
		"keep.log":          "",
		"sub/important.log": "",
		"build/out.o":       "",
		"sub/x.tmp":         "",
		"plain.txt":         "",
	})
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.log\n")})
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		rel   string
		isDir bool
		want  bool
		why   string
	}{
		{"a.log", false, true, "ignored by the global layer"},
		{"keep.log", false, false, "re-included by the repository exclude file"},
		{"sub/important.log", false, false, "re-included by the later .gitignore"},
		{"build", true, true, "ignored by the root .gitignore"},
		{"sub/x.tmp", false, true, "ignored by sub/.gitignore"},
		{"plain.txt", false, false, "matched by nothing"},
	} {
		if got := ws.Ignored(c.rel, c.isDir); got != c.want {
			t.Errorf("Ignored(%q) = %v, want %v (%s)", c.rel, got, c.want, c.why)
		}
	}
	assertWalkMatchesIgnored(t, ws)
}

// TS-20-19: Nested repositories start a new ignore root, .git is always
// ignored and .ignore is not a source.
func TestNestedRepositoriesAndGitAndDotIgnore_TS20_19(t *testing.T) {
	root := t.TempDir()
	tree(t, root, map[string]string{
		".gitignore":      "*.dat\n!.git\n",
		".ignore":         "*.txt\n",
		"sub/repo/.git/":  "",
		"sub/repo/f.dat":  "",
		"sub/other/g.dat": "",
		"a.txt":           "",
	})
	ws := openIgnoring(t, root)

	for _, c := range []struct {
		rel   string
		isDir bool
		want  bool
		why   string
	}{
		{"sub/repo/f.dat", false, false, "the root .gitignore stops applying at the repository boundary"},
		{"sub/other/g.dat", false, true, "outside the nested repository the root .gitignore applies"},
		{"sub/repo/.git", true, true, "a .git directory is always ignored"},
		{".git/config", false, true, "any .git segment is ignored, even with a !.git pattern"},
		{"a.txt", false, false, ".ignore is not a source"},
	} {
		if got := ws.Ignored(c.rel, c.isDir); got != c.want {
			t.Errorf("Ignored(%q) = %v, want %v (%s)", c.rel, got, c.want, c.why)
		}
	}

	err := ws.Walk(context.Background(), "", WalkOptions{IncludeHidden: true}, func(rel string, _ fs.DirEntry) error {
		if hasGitComponent(rel) {
			t.Errorf("Walk called fn for %q", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertWalkMatchesIgnored(t, ws)
}

// assertWalkMatchesIgnored checks that Walk with IncludeHidden visits exactly
// the entries Ignored reports as not ignored.
func assertWalkMatchesIgnored(t *testing.T, ws *Workspace) {
	t.Helper()
	visited := map[string]bool{}
	err := ws.Walk(context.Background(), "", WalkOptions{IncludeHidden: true}, func(rel string, _ fs.DirEntry) error {
		visited[rel] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(ws.Root(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == ws.Root() {
			return err
		}
		rel, _ := filepath.Rel(ws.Root(), p)
		rel = filepath.ToSlash(rel)
		if allowed := !ws.Ignored(rel, d.IsDir()); allowed != visited[rel] {
			t.Errorf("%q: Walk visited it = %v, Ignored allows it = %v", rel, visited[rel], allowed)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
