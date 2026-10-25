package workspace

import (
	"context"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
)

type ignoreCase struct {
	rel   string
	isDir bool
	want  bool
}

func checkIgnored(t *testing.T, ws *Workspace, cases []ignoreCase) {
	t.Helper()
	for _, c := range cases {
		if got := ws.Ignored(c.rel, c.isDir); got != c.want {
			t.Errorf("Ignored(%q, dir=%v) = %v, want %v", c.rel, c.isDir, got, c.want)
		}
	}
}

func TestIgnoredFollowsTheGitignoreDialect(t *testing.T) {
	ws := openTree(t, map[string]string{".gitignore": "# a comment\n\n" +
		"*.[!o]\n" + // a negated class
		"a/**/b/c\n" + // ** backtracks past the first b
		"\\#notes\n\\!bang\n" + // escaped, not a comment or a negation
		"trail   \n" + "space\\ \n" + // trailing spaces trimmed unless escaped
		"/rooted\n" + "build/\n" + "node_modules\r\n" + "/\n"})
	checkIgnored(t, ws, []ignoreCase{
		{"x.c", false, true}, {"x.o", false, false},
		{"a/b/x/b/c", false, true},
		{"#notes", false, true}, {"!bang", false, true}, {"# a comment", false, false},
		{"trail", false, true}, {"space ", false, true}, {"space", false, false},
		{"rooted", false, true}, {"rooted/below", false, true}, {"sub/rooted", false, false},
		{"build", true, true}, {"build", false, false}, {"sub/build/out", false, true},
		{"deep/node_modules/x/y", false, true},
		{"plain", false, false},
	})
}

// Sources apply lowest first (global, .git/info/exclude, then each
// .gitignore from the root down) and the last match wins.
func TestIgnoreSourcesApplyInPrecedenceOrder(t *testing.T) {
	root := testutil.Tree(t, map[string]string{
		".git/info/exclude": "!keep.log\n*.excl\n",
		".gitignore":        "build/\n*.tmp\n!x.tmp\n",
		"sub/.gitignore":    "!important.log\n*.o\n",
	})
	ws, err := Open(root, Options{Ignore: withGlobalExcludes(t, "*.log\n")})
	if err != nil {
		t.Fatal(err)
	}
	checkIgnored(t, ws, []ignoreCase{
		{"a.log", false, true},              // the global layer
		{"keep.log", false, false},          // re-included by the repository exclude file
		{"a.excl", false, true},             // the repository exclude file
		{"build", true, true},               // the root .gitignore
		{"a.tmp", false, true},              // the root .gitignore
		{"x.tmp", false, false},             // a later line of the same file
		{"sub/important.log", false, false}, // re-included by the deeper .gitignore
		{"sub/noise.log", false, true},
		{"sub/x.o", false, true},
		{"x.o", false, false}, // a deeper file says nothing about its parent
		{"plain.txt", false, false},
	})
}

// A directory holding .git, a directory or a worktree's pointer file, is its
// own ignore root: its own exclude file applies, the outer .gitignore files
// stop at it, and its rules do not leak out.
func TestANestedRepositoryIsItsOwnIgnoreRoot(t *testing.T) {
	ws := openTree(t, map[string]string{
		".gitignore":                   "dist/\n*.dat\n!.git\n",
		".ignore":                      "*.txt\n",
		"dist/outer.js":                "",
		"other/g.dat":                  "",
		"vendor/lib/.git/HEAD":         "ref: refs/heads/main\n",
		"vendor/lib/.git/info/exclude": "*.mine\n",
		"vendor/lib/.gitignore":        "tmp/\nzbuild/\n",
		"vendor/lib/dist/bundle.js":    "",
		"vendor/lib/f.dat":             "",
		"vendor/lib/a.mine":            "",
		"vendor/lib/tmp/x":             "",
		"vendor/lib/zbuild/y":          "",
		"wt/.git":                      "gitdir: elsewhere\n",
		"wt/h.dat":                     "",
		"zbuild/app":                   "", // outside the nested repository, and walked after it
		"a.txt":                        "",
	})
	checkIgnored(t, ws, []ignoreCase{
		{"dist", true, true},
		{"other/g.dat", false, true},
		{"vendor/lib/dist", true, false},
		{"vendor/lib/f.dat", false, false},
		{"wt/h.dat", false, false},
		{"vendor/lib/a.mine", false, true},
		{"vendor/lib/tmp", true, true},
		{"vendor/lib/zbuild", true, true},
		{"zbuild", true, false},
		{"a.txt", false, false}, // .ignore is not a source
		{"vendor/lib/.git", true, true},
		{"wt/.git", false, true},
		{".git/config", false, true}, // even under a !.git pattern
	})
	got := walked(t, ws, "", WalkOptions{IncludeHidden: true})
	if !slices.Contains(got, "zbuild/app") || slices.Contains(got, "vendor/lib/zbuild") {
		t.Errorf("the nested repository's zbuild/ rule leaked out, or did not apply within it: %v", got)
	}
	if !slices.Equal(got, notIgnored(t, ws)) {
		t.Errorf("Walk = %v, want what Ignored allows %v", got, notIgnored(t, ws))
	}
}

func TestGlobalExcludesComeFromGitConfigThenXDGThenHome(t *testing.T) {
	dir := testutil.Tree(t, map[string]string{
		"cfg/explicit": "secret*\n", "xdg/git/ignore": "xdgonly*\n", "home/.config/git/ignore": "homeonly*\n",
	})
	explicit, xdg, home := filepath.Join(dir, "cfg/explicit"), filepath.Join(dir, "xdg"), filepath.Join(dir, "home")
	getenv := func(v string) func(string) string {
		return func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return v
			}
			return ""
		}
	}
	homeAt := func() (string, error) { return home, nil }
	for _, c := range []struct {
		name    string
		opts    IgnoreOptions
		ignored string
	}{
		{"core.excludesFile", IgnoreOptions{GitConfig: func() string { return explicit }, Getenv: getenv(xdg), Home: homeAt}, "secret.txt"},
		{"XDG_CONFIG_HOME", IgnoreOptions{GitConfig: func() string { return "" }, Getenv: getenv(xdg), Home: homeAt}, "xdgonly.txt"},
		{"home", IgnoreOptions{GitConfig: func() string { return "" }, Getenv: getenv(""), Home: homeAt}, "homeonly.txt"},
		{"none", NoGlobalExcludes(), ""},
	} {
		ws, err := Open(t.TempDir(), Options{Ignore: c.opts})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"secret.txt", "xdgonly.txt", "homeonly.txt"} {
			if got := ws.Ignored(f, false); got != (f == c.ignored) {
				t.Errorf("%s: Ignored(%s) = %v", c.name, f, got)
			}
		}
	}
}

// The zero options read the real environment: git's core.excludesFile, then
// $XDG_CONFIG_HOME/git/ignore, then ~/.config/git/ignore.
func TestZeroIgnoreOptionsReadTheRealEnvironment(t *testing.T) {
	testutil.Hermetic(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git binary")
	}
	t.Chdir(t.TempDir()) // outside any repository, whose config git would read too
	home, xdg := os.Getenv("HOME"), os.Getenv("XDG_CONFIG_HOME")
	explicit := filepath.Join(t.TempDir(), "explicit")
	testutil.WriteTree(t, "/", map[string]string{
		explicit: "*.git-config\n", xdg + "/git/ignore": "*.xdg\n", home + "/.config/git/ignore": "*.home\n",
	})
	try := func(want string) {
		t.Helper()
		ws, err := Open(t.TempDir(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		for _, ext := range []string{"git-config", "xdg", "home"} {
			if got := ws.Ignored("f."+ext, false); got != (ext == want) {
				t.Errorf("with %s the source: Ignored(f.%s) = %v", want, ext, got)
			}
		}
	}
	try("xdg")
	t.Setenv("XDG_CONFIG_HOME", "")
	try("home")
	if err := os.WriteFile(os.Getenv("GIT_CONFIG_GLOBAL"), []byte("[core]\n\texcludesFile = "+explicit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	try("git-config")
	// NoGlobalExcludes pins an empty global layer whatever the environment holds.
	if open(t, t.TempDir()).Ignored("f.git-config", false) {
		t.Error("NoGlobalExcludes applied the configured global excludes file")
	}
}

func TestIgnoredAppliesTheAncestorsIgnoreFiles(t *testing.T) {
	ws := openTree(t, map[string]string{
		".gitignore": "vendor/\n", "a/.gitignore": "*.o\n!keep.o\n", "a/repo/.git/HEAD": "",
	})
	checkIgnored(t, ws, []ignoreCase{
		{"vendor/x/y.go", false, true}, // an ignored ancestor
		{"vendor", true, true},
		{"a/b.o", false, true},
		{"a/keep.o", false, false},
		{`a\b.o`, false, true}, // either separator
		{`a\keep.o`, false, false},
		{"a/./b.o", false, true},
		{"a/repo/z.o", false, false},
		{"a", true, false},
	})
}

// Ignored answers the root as kept, and a path that is absolute or escapes
// the root, or that holds a .git segment, as ignored; a leading dot is not a
// rule.
func TestIgnoredOfTheRootGitAndOutsidePaths(t *testing.T) {
	ws := open(t, t.TempDir())
	checkIgnored(t, ws, []ignoreCase{
		{"", true, false}, {".", true, false}, {"a/..", true, false},
		{".git", true, true}, {"a/.git/config", false, true}, {"x/.git", true, true},
		{filepath.Join(ws.Root(), "a"), false, true}, {"/etc/passwd", false, true},
		{"../x", false, true}, {"a/../../x", false, true}, {"..", true, true},
		{".env", false, false}, {".config/app.toml", false, false}, {".github", true, false},
	})
}

func TestIgnoredRereadsTheIgnoreFilesOnEveryCall(t *testing.T) {
	root := testutil.Tree(t, map[string]string{"a/.gitignore": "*.x\n", ".git/HEAD": ""})
	ws := open(t, root)
	for _, step := range []struct {
		file, body string
		want       bool
	}{
		{"", "", true},
		{"a/.gitignore", "", false},
		{".git/info/exclude", "*.x\n", true},
	} {
		if step.file != "" {
			testutil.WriteTree(t, root, map[string]string{step.file: step.body})
		}
		if got := ws.Ignored("a/f.x", false); got != step.want {
			t.Fatalf("after writing %q to %q: Ignored(a/f.x) = %v, want %v", step.body, step.file, got, step.want)
		}
	}
}

func TestTheGlobalExcludesLookupRunsOncePerWorkspace(t *testing.T) {
	c := &counting{}
	ws, err := Open(testutil.Tree(t, map[string]string{"a/f.txt": "", "b.txt": ""}), Options{Ignore: c.options()})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			if i%2 == 0 {
				ws.Ignored("a/f.txt", false)
				return
			}
			_ = ws.Walk(context.Background(), "", WalkOptions{}, func(string, fs.DirEntry) error { return nil })
		})
	}
	wg.Wait()
	ws.Ignored("b.txt", false)
	_ = ws.Walk(context.Background(), "a", WalkOptions{}, func(string, fs.DirEntry) error { return nil })
	if n := c.lookups.Load(); n != 1 {
		t.Fatalf("the lookup ran %d times, want 1", n)
	}
}

// randomTree writes a tree with nested .gitignore files and negations,
// repository roots (now and then a .git file) with and without an exclude
// file, and hidden entries: everything the ignore rules have a case for.
func randomTree(t *testing.T, r *rand.Rand) string {
	names := []string{"a", "b", "c", "build", "tmp", ".hid", "keep.log", "x.log", "top", "ab", "d.txt"}
	pats := []string{"*.log", "build/", "!keep.log", "/top", "tmp", "a*", "!ab", "b/c", "**/x.log", "!*.txt", "*.txt", ".hid"}
	pat := func() string { return pats[r.IntN(len(pats))] + "\n" }
	files := map[string]string{}
	var mk func(dir string, depth int)
	mk = func(dir string, depth int) {
		files[dir+".keep"] = ""
		switch r.IntN(8) {
		case 0:
			files[dir+".git"] = "gitdir: elsewhere\n"
		case 1:
			files[dir+".git/HEAD"] = ""
		case 2:
			files[dir+".git/info/exclude"] = pat() + pat()
		}
		if r.IntN(2) == 0 {
			files[dir+".gitignore"] = pat() + pat()
		}
		for _, n := range names {
			switch {
			case r.IntN(3) != 0:
			case depth < 3 && r.IntN(2) == 0:
				mk(dir+n+"/", depth+1)
			default:
				files[dir+n] = "x"
			}
		}
	}
	mk("", 0)
	return testutil.Tree(t, files)
}

func TestWalkAndIgnoredAgreeOnAnyTree(t *testing.T) {
	trees := 150
	if testing.Short() {
		trees = 20
	}
	for seed := range uint64(trees) {
		ws := open(t, randomTree(t, rand.New(rand.NewPCG(seed, 32))))
		if got, want := walked(t, ws, "", WalkOptions{IncludeHidden: true}), notIgnored(t, ws); !slices.Equal(got, want) {
			t.Fatalf("seed %d: Walk = %v\nIgnored allows %v", seed, got, want)
		}
	}
}
