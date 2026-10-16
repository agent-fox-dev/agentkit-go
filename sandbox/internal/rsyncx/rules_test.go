package rsyncx

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// writeTree writes files (path -> content); a content starting with "->"
// makes a symlink to the rest.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(body, "->"); ok {
			if err := os.Symlink(target, p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// walkOracle is every entry filepath.WalkDir finds that workspace.Walk does
// not visit, whose parent Walk visits (or is the root), and that has no .git
// component, with no global excludes.
func walkOracle(t *testing.T, root string) []string {
	t.Helper()
	return walkOracleWith(t, root, workspace.NoGlobalExcludes())
}

// walkOracleWith is walkOracle with ig as the workspace's ignore environment.
func walkOracleWith(t *testing.T, root string, ig workspace.IgnoreOptions) []string {
	t.Helper()
	root, _ = filepath.EvalSymlinks(root)
	ws, err := workspace.Open(root, workspace.Options{Ignore: ig})
	if err != nil {
		t.Fatal(err)
	}
	visited := map[string]bool{".": true}
	if err := ws.Walk(context.Background(), "", workspace.WalkOptions{IncludeHidden: true},
		func(rel string, _ fs.DirEntry) error { visited[rel] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if slices.Contains(strings.Split(rel, "/"), ".git") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !visited[rel] && visited[path.Dir(rel)] {
			out = append(out, rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	return out
}

func ruleTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore": "build/\n*.log\n", "build/sub/deep.txt": "d", "x.log": "x", "keep.txt": "k",
		".git/HEAD": "ref", ".git/info/exclude": "secret.txt\n", "secret.txt": "s",
		"nested/.git/HEAD": "ref", "nested/.gitignore": "tmp/\n", "nested/tmp/a": "a", "nested/n.txt": "n",
		".hidden": "h", "esc": "->/etc", "up": "->../outside", "in": "->keep.txt", "sub/in2": "->../keep.txt",
	})
	return root
}

func TestComputeRules(t *testing.T) {
	root := ruleTree(t)
	ctx := context.Background()
	rs, err := Compute(ctx, root, RuleOptions{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if want := walkOracle(t, root); !slices.Equal(rs.Ignored, want) {
		t.Errorf("Ignored %v, want the Walk oracle %v", rs.Ignored, want)
	}
	if !slices.Contains(rs.Ignored, "build") || slices.Contains(rs.Ignored, "build/sub") {
		t.Errorf("Ignored %v: want build once, at its highest level", rs.Ignored)
	}
	if want := []string{"esc", "up"}; !slices.Equal(rs.Escaping, want) {
		t.Errorf("Escaping %v, want %v", rs.Escaping, want)
	}
	if rs.Git || slices.Contains(rs.List(true), ".git") {
		t.Error("a .git rule without ExcludeGit")
	}
	rs, _ = Compute(ctx, root, RuleOptions{Ignore: workspace.NoGlobalExcludes(), ExcludeGit: true})
	if !rs.Git || !slices.Contains(rs.List(true), ".git") {
		t.Error("no .git rule with ExcludeGit")
	}
	rs, _ = Compute(ctx, root, RuleOptions{IncludeIgnored: true})
	if len(rs.Ignored) != 0 || len(rs.Escaping) != 2 {
		t.Errorf("IncludeIgnored: %+v, want no ignore rules and both escaping links", rs)
	}
	rs, _ = Compute(ctx, filepath.Join(root, "keep.txt"), RuleOptions{File: true, ExcludeGit: true})
	if l := rs.List(true); !slices.Equal(l, []string{".git"}) {
		t.Errorf("a file transfer's rules %v, want only .git", l)
	}
	for _, l := range rs.List(true) {
		if l != ".git" && slices.Contains(strings.Split(l, "/"), ".git") {
			t.Errorf("rule %q has a .git component", l)
		}
	}
}

func TestCombineAndSize(t *testing.T) {
	got := Combine([]string{"build", "a*b", "dup", "sp ace"}, []string{"dup", "q?x", "br[c]k", ".git"})
	if want := []string{".git", "a*b", "br[c]k", "build", "dup", "q?x", "sp ace"}; !slices.Equal(got, want) {
		t.Errorf("Combine = %q, want %q", got, want)
	}
	rules := func(n int) []string { return []string{strings.Repeat("x", n)} }
	if err := CheckRuleSize(rules(MaxRuleBytes)); err != nil {
		t.Errorf("8 MiB of rules refused: %v", err)
	}
	err := CheckRuleSize(rules(MaxRuleBytes + 1))
	if err == nil || !strings.Contains(err.Error(), "8388609") || !strings.Contains(err.Error(), "8 MiB") {
		t.Errorf("8 MiB + 1 byte: %v, want an error naming the size and 8 MiB", err)
	}
}

func TestMatcherAndFilteredFS(t *testing.T) {
	m := NewMatcher([]string{"build", "a*b", ".git", "sub/x"})
	for p, want := range map[string]bool{"build": true, "build/o": true, "a*b": true, "axxb": false,
		".git": true, "n/.git/HEAD": true, "sub": false, "sub/x": true, "sub/x/y": true, "sub/y": false} {
		if m.Excluded(p) != want {
			t.Errorf("Excluded(%q) = %v, want %v", p, !want, want)
		}
	}
	root := ruleTree(t)
	r, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fsys := Filtered(r, ".", NewMatcher([]string{"build", "esc", ".git", "nested/tmp"}))
	var seen []string
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		seen = append(seen, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range seen {
		if NewMatcher([]string{"build", "esc", ".git", "nested/tmp"}).Excluded(p) {
			t.Errorf("the filtered tree shows %s", p)
		}
	}
	if !slices.Contains(seen, "keep.txt") || !slices.Contains(seen, "in") {
		t.Errorf("the filtered tree lacks included entries: %v", seen)
	}
	if _, err := fsys.Open("build/sub/deep.txt"); err == nil {
		t.Error("an excluded file opens")
	}
	if target, err := fsys.(fs.ReadLinkFS).ReadLink("in"); err != nil || target != "keep.txt" {
		t.Errorf("ReadLink(in) = %q, %v", target, err)
	}
	sub := Filtered(r, "nested", NewMatcher([]string{"tmp"}))
	if _, err := fs.Stat(sub, "nested/tmp/a"); err == nil {
		t.Error("a rule relative to the transfer root nested did not apply")
	}
	if _, err := fs.Stat(sub, "nested/n.txt"); err != nil {
		t.Errorf("nested/n.txt: %v", err)
	}
	set, err := PresentSet(fsys, ".")
	if err != nil || slices.Contains(set, "build") || !slices.Contains(set, "sub/in2") || slices.Contains(set, ".") {
		t.Errorf("PresentSet = %v, %v", set, err)
	}
}

func TestDeleteAbsent(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "recv"), filepath.Join(base, "outside")
	writeTree(t, outside, map[string]string{"keep": "k"})
	writeTree(t, root, map[string]string{
		"a.txt": "a", "stale.txt": "s", "out/x.o": "o", "gone/deep/f": "f", ".env": "e",
		".git/HEAD": "ref", "link-out": "->" + outside, "dir/stale": "s", "dir/kept": "k",
	})
	present := []string{"a.txt", "dir", "dir/kept"}
	n, err := DeleteAbsent(root, NewMatcher([]string{"out/x.o", ".env", ".git", "link-out"}), present)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a.txt", "out/x.o", ".env", ".git/HEAD", "link-out", "dir/kept"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
			t.Errorf("%s was deleted: %v", p, err)
		}
	}
	for _, p := range []string{"stale.txt", "gone", "dir/stale"} {
		if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	if n != 5 {
		t.Errorf("deleted %d entries, want 5 (stale.txt, gone, gone/deep, gone/deep/f, dir/stale)", n)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Error("deletion reached through a symlink")
	}

	// A symlink absent from the sender is removed, never followed.
	writeTree(t, root, map[string]string{"link2": "->" + outside})
	if _, err := DeleteAbsent(root, NewMatcher(nil), []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "link2")); err == nil {
		t.Error("an absent symlink survived")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Error("deleting a symlink removed its target")
	}
}

// TS-20-55: RuleOptions.Ignore is a workspace.IgnoreOptions and rsyncx visits
// the tree through workspace: the rules built with ws.Ignore() exclude exactly
// what workspace.Walk excludes with those options, a global excludes file only
// the options name included.
func TestRuleOptionsTakeTheWorkspacesIgnore_TS20_55(t *testing.T) {
	if got, want := reflect.TypeFor[RuleOptions]().Field(0), reflect.TypeFor[workspace.IgnoreOptions](); got.Name != "Ignore" || got.Type != want {
		t.Fatalf("RuleOptions' first field is %s %v, want Ignore %v", got.Name, got.Type, want)
	}

	root := ruleTree(t)
	writeTree(t, root, map[string]string{"a.glob": "g", "nested/b.glob": "g", "c.txt": "c"})
	global := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(global, []byte("*.glob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	custom := workspace.NoGlobalExcludes()
	custom.GitConfig = func() string { return global }
	ws, err := workspace.Open(root, workspace.Options{Ignore: custom})
	if err != nil {
		t.Fatal(err)
	}

	rs, err := Compute(context.Background(), root, RuleOptions{Ignore: ws.Ignore()})
	if err != nil {
		t.Fatal(err)
	}
	if want := walkOracleWith(t, root, ws.Ignore()); !slices.Equal(rs.Ignored, want) {
		t.Errorf("Ignored %v, want what workspace.Walk excludes with those options: %v", rs.Ignored, want)
	}
	if !slices.Contains(rs.Ignored, "a.glob") || slices.Contains(rs.Ignored, "c.txt") {
		t.Errorf("Ignored %v: the options' global excludes file should name a.glob and not c.txt", rs.Ignored)
	}
	// Without the options the global layer is empty and a.glob is not excluded.
	plain, err := Compute(context.Background(), root, RuleOptions{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plain.Ignored, "a.glob") {
		t.Errorf("NoGlobalExcludes() excluded a.glob: %v", plain.Ignored)
	}
}
