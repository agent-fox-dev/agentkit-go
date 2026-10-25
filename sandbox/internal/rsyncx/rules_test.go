package rsyncx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/workspace"
)

// Ignored is what workspace.Walk does not visit, at its highest level and
// never under .git; Escaping is the symlinks that leave the root.
func TestComputeListsTheIgnoredAndEscapingEntries(t *testing.T) {
	root := ruleTree(t)
	rs, err := Compute(context.Background(), root, RuleOptions{Ignore: workspace.NoGlobalExcludes()})
	if err != nil {
		t.Fatal(err)
	}
	if want := walkOracle(t, root, workspace.NoGlobalExcludes()); !slices.Equal(rs.Ignored, want) {
		t.Errorf("Ignored %v, want the Walk oracle %v", rs.Ignored, want)
	}
	if !slices.Contains(rs.Ignored, "build") || slices.Contains(rs.Ignored, "build/sub") || !slices.Contains(rs.Ignored, "secret.txt") {
		t.Errorf("Ignored %v: want build once, at its highest level, and secret.txt from .git/info/exclude", rs.Ignored)
	}
	if want := []string{"esc", "up"}; !slices.Equal(rs.Escaping, want) {
		t.Errorf("Escaping %v, want %v", rs.Escaping, want)
	}
	if rs.Git || len(rs.Unreadable) != 0 {
		t.Errorf("Git = %v, Unreadable = %v; want neither", rs.Git, rs.Unreadable)
	}
}

func TestComputeOptions(t *testing.T) {
	root := ruleTree(t)
	ctx := context.Background()
	rs, _ := Compute(ctx, root, RuleOptions{Ignore: workspace.NoGlobalExcludes(), ExcludeGit: true})
	if !rs.Git || !slices.Contains(rs.List(true), ".git") {
		t.Error("ExcludeGit gave no .git rule")
	}
	for _, l := range rs.List(true) {
		if l != ".git" && slices.Contains(strings.Split(l, "/"), ".git") {
			t.Errorf("rule %q has a .git component", l)
		}
	}
	rs, _ = Compute(ctx, root, RuleOptions{IncludeIgnored: true})
	if len(rs.Ignored) != 0 || len(rs.Escaping) != 2 {
		t.Errorf("IncludeIgnored: %+v, want no ignore rules and both escaping links", rs)
	}
	rs, _ = Compute(ctx, filepath.Join(root, "keep.txt"), RuleOptions{File: true, ExcludeGit: true})
	if l := rs.List(true); !slices.Equal(l, []string{".git"}) {
		t.Errorf("a file transfer's rules %v, want only .git", l)
	}
	if _, err := Compute(ctx, filepath.Join(root, "missing"), RuleOptions{}); err == nil {
		t.Error("Compute of a missing root succeeded")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Compute(cancelled, root, RuleOptions{IncludeIgnored: true}); err == nil {
		t.Error("Compute under a cancelled context succeeded")
	}
}

// An export of a subdirectory honours the workspace's ignore files; a root
// the workspace ignores takes only its own.
func TestComputeWithABaseAppliesTheBasesIgnoreFiles(t *testing.T) {
	ws := t.TempDir()
	writeTree(t, ws, map[string]string{".gitignore": "*.tmp\nskipped/\n", "sub/a.tmp": "x", "sub/b.txt": "b",
		"skipped/.gitignore": "*.o\n", "skipped/c.tmp": "c", "skipped/d.o": "d"})
	ig := workspace.NoGlobalExcludes()
	rs, err := Compute(context.Background(), filepath.Join(ws, "sub"), RuleOptions{Ignore: ig, Base: ws})
	if err != nil || !slices.Equal(rs.Ignored, []string{"a.tmp"}) {
		t.Errorf("sub: Ignored %v, %v; want [a.tmp]", rs.Ignored, err)
	}
	rs, err = Compute(context.Background(), filepath.Join(ws, "skipped"), RuleOptions{Ignore: ig, Base: ws})
	if err != nil || !slices.Equal(rs.Ignored, []string{"d.o"}) {
		t.Errorf("skipped: Ignored %v, %v; want only its own [d.o]", rs.Ignored, err)
	}
	if _, err := Compute(context.Background(), ws, RuleOptions{Ignore: ig, Base: filepath.Join(ws, "missing")}); err == nil {
		t.Error("a missing Base was accepted")
	}
}

// The ignore environment given is the one the walk uses: a global excludes
// file it names applies, and with no global excludes it does not.
func TestComputeUsesTheGivenIgnoreEnvironment(t *testing.T) {
	root := ruleTree(t)
	writeTree(t, root, map[string]string{"a.glob": "g", "nested/b.glob": "g", "c.txt": "c"})
	global := filepath.Join(t.TempDir(), "global-ignore")
	if err := os.WriteFile(global, []byte("*.glob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	custom := workspace.NoGlobalExcludes()
	custom.GitConfig = func() string { return global }
	rs, err := Compute(context.Background(), root, RuleOptions{Ignore: custom})
	if err != nil {
		t.Fatal(err)
	}
	if want := walkOracle(t, root, custom); !slices.Equal(rs.Ignored, want) {
		t.Errorf("Ignored %v, want what workspace.Walk excludes with those options: %v", rs.Ignored, want)
	}
	if !slices.Contains(rs.Ignored, "a.glob") || slices.Contains(rs.Ignored, "c.txt") {
		t.Errorf("Ignored %v: want a.glob and not c.txt", rs.Ignored)
	}
	plain, _ := Compute(context.Background(), root, RuleOptions{Ignore: workspace.NoGlobalExcludes()})
	if slices.Contains(plain.Ignored, "a.glob") {
		t.Errorf("NoGlobalExcludes() excluded a.glob: %v", plain.Ignored)
	}
}

func TestComputeMakesAnUnreadableEntryARule(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}
	root := t.TempDir()
	writeTree(t, root, map[string]string{"locked/f": "x", "secret": "s", "ok": "o"})
	for _, p := range []string{"locked", "secret"} {
		if err := os.Chmod(filepath.Join(root, p), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, p), 0o755) })
	}
	rs, err := Compute(context.Background(), root, RuleOptions{IncludeIgnored: true})
	if err != nil || !slices.Equal(rs.Unreadable, []string{"locked", "secret"}) {
		t.Errorf("Unreadable %v, %v; want [locked secret]", rs.Unreadable, err)
	}
}

func TestRuleSetListIsSortedAndDeduplicated(t *testing.T) {
	rs := RuleSet{Ignored: []string{"b", "a"}, Unreadable: []string{"c", "a"}, Escaping: []string{"d"}, Git: true}
	if got := rs.List(true); !slices.Equal(got, []string{".git", "a", "b", "c", "d"}) {
		t.Errorf("List(true) = %v", got)
	}
	if got := rs.List(false); !slices.Equal(got, []string{".git", "a", "c", "d"}) {
		t.Errorf("List(false) = %v", got)
	}
}

func TestCombineIsTheSortedUnion(t *testing.T) {
	got := Combine([]string{"build", "a*b", "dup", "sp ace"}, []string{"dup", "q?x", "br[c]k", ".git"})
	if want := []string{".git", "a*b", "br[c]k", "build", "dup", "q?x", "sp ace"}; !slices.Equal(got, want) {
		t.Errorf("Combine = %q, want %q", got, want)
	}
}

func TestCheckRuleSizeRefusesOverEightMiB(t *testing.T) {
	rules := func(n int) []string { return []string{strings.Repeat("x", n)} }
	if err := CheckRuleSize(rules(MaxRuleBytes)); err != nil {
		t.Errorf("8 MiB of rules refused: %v", err)
	}
	err := CheckRuleSize(rules(MaxRuleBytes + 1))
	if err == nil || !strings.Contains(err.Error(), "8388609") || !strings.Contains(err.Error(), "8 MiB") {
		t.Errorf("8 MiB + 1 byte: %v, want an error naming the size and 8 MiB", err)
	}
}

// A rule matches its path and everything under it, wildcards literally, and
// .git matches a .git component at any depth.
func TestMatcherExcludesExactPathsAndGitAtAnyDepth(t *testing.T) {
	m := NewMatcher([]string{"build", "a*b", ".git", "sub/x/"})
	for p, want := range map[string]bool{"build": true, "build/o": true, "a*b": true, "axxb": false,
		".git": true, "n/.git/HEAD": true, "sub": false, "sub/x": true, "sub/x/y": true, "sub/y": false,
		".": false, "builder": false} {
		if m.Excluded(p) != want {
			t.Errorf("Excluded(%q) = %v, want %v", p, !want, want)
		}
	}
}
