package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
