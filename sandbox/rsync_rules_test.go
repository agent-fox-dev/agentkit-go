//go:build !windows

package sandbox_test

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tarsafe"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/treegen"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// symlinkTree writes the TS-14-11 links (name -> target) and a.txt under
// root.
var symlinkTree = map[string]string{
	"abs": "/etc/hosts", "up": "../x", "sub/up2": "../../x", "sub/dd": "x/../../..",
	"sub/in": "../a.txt", "self": ".", "sub/in2": "y/../a",
}

func writeLinks(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range symlinkTree {
		p := filepath.Join(root, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
}

// rawPlan opens a raw Rsync stream on sb, sends start and returns the plan.
// On return it half-closes and reads until the agent ends the stream, so the
// agent has released its one stream slot before the caller's next transfer:
// returning earlier raced a following Sync into "another Rsync stream is
// running".
func rawPlan(t *testing.T, client sandboxpb.AgentClient, start *sandboxpb.RsyncStart) (*sandboxpb.RsyncPlan, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := client.Rsync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = st.CloseSend()
		for {
			if _, err := st.Recv(); err != nil {
				return
			}
		}
	}()
	if err := st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: start}}); err != nil {
		t.Fatal(err)
	}
	f, err := st.Recv()
	if err != nil {
		return nil, err
	}
	return f.GetPlan(), nil
}

// TS-14-11: every escaping symlink gets a rule on both sides, decided by
// tarsafe's symlink predicate and independent of IncludeIgnored.
// 14-REQ-3.3.
func TestEscapingSymlinkRules_TS14_11(t *testing.T) {
	root := t.TempDir()
	writeLinks(t, root)
	want := []string{"abs", "sub/dd", "sub/up2", "up"}
	for _, inc := range []bool{false, true} {
		rs, err := rsyncx.Compute(context.Background(), root, rsyncx.RuleOptions{IncludeIgnored: inc})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rs.Escaping, want) {
			t.Errorf("IncludeIgnored %v: escaping rules %v, want %v", inc, rs.Escaping, want)
		}
		for name, target := range symlinkTree {
			if slices.Contains(rs.Escaping, name) != tarsafe.SymlinkEscapes(name, target) {
				t.Errorf("%s -> %s: rule %v, predicate %v", name, target, slices.Contains(rs.Escaping, name), tarsafe.SymlinkEscapes(name, target))
			}
		}
	}

	isolate(t)
	sb := newLocal(t)
	writeLinks(t, sb.Agent().Workdir)
	plan, err := rawPlan(t, localtest.RawClient(sb), &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !slices.Contains(plan.Rules, w) {
			t.Errorf("the plan's rules %v lack %s", plan.Rules, w)
		}
	}
	for _, r := range plan.Rules {
		if strings.HasPrefix(r, "sub/in") || r == "self" {
			t.Errorf("the plan excludes the inside link %s", r)
		}
	}
}

// TS-14-9: each side's ignore rules are exactly the entries tools.Walk does
// not visit, at their highest ignored level and never under .git; the host
// uses LocalDir.Ignore and the agent NoGlobalExcludes. 14-REQ-3.1.
func TestIgnoreRulesMatchWalk_TS14_9(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".gitignore": "build/\n*.log\n", "build/sub/deep.txt": "d", "x.log": "x",
		"nested/.git/HEAD": "ref: refs/heads/main\n", "nested/.gitignore": "tmp/\n", "nested/tmp/t.txt": "t",
		".git/info/exclude": "secret.txt\n", "secret.txt": "s", ".git/objects/ab/cd": "o", "keep.txt": "k"})
	rs, err := rsyncx.Compute(ctx, root, rsyncx.RuleOptions{Ignore: hermetic})
	if err != nil {
		t.Fatal(err)
	}
	oracle := slices.Sorted(maps.Keys(treegen.Ignored(t, root)))
	if !slices.Equal(rs.Ignored, oracle) {
		t.Errorf("ignore rules %q, oracle %q", rs.Ignored, oracle)
	}
	for _, w := range []string{"build", "nested/tmp", "secret.txt", "x.log"} {
		if !slices.Contains(rs.Ignored, w) {
			t.Errorf("rules %q lack %s", rs.Ignored, w)
		}
	}
	for _, r := range rs.Ignored {
		if r == "build/sub" || slices.Contains(strings.Split(r, "/"), ".git") {
			t.Errorf("rule %s is below its highest ignored level or under .git", r)
		}
	}

	g := t.TempDir()
	writeFiles(t, g, map[string]string{"git/ignore": "*.global\n"})
	withG := tools.IgnoreOptions{
		Getenv:    func(k string) string { return map[string]string{"XDG_CONFIG_HOME": g}[k] },
		Home:      func() (string, error) { return "", os.ErrNotExist },
		GitConfig: func() string { return "" },
	}
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"a.global": "g", "b.txt": "b"})
	if rs, _ := rsyncx.Compute(ctx, src, rsyncx.RuleOptions{Ignore: withG}); !slices.Contains(rs.Ignored, "a.global") {
		t.Errorf("with G the host rules %q lack a.global", rs.Ignored)
	}
	isolate(t)
	for _, c := range []struct {
		ig   tools.IgnoreOptions
		want bool
	}{{withG, false}, {hermetic, true}} {
		sb := newLocal(t)
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: c.ig}); err != nil {
			t.Fatal(err)
		}
		if got := lexists(filepath.Join(sb.Agent().Workdir, "a.global")); got != c.want {
			t.Errorf("a.global in the workspace: %v, want %v", got, c.want)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", g)
	sb := newLocal(t)
	writeFiles(t, sb.Agent().Workdir, map[string]string{"w.global": "w"})
	plan, err := rawPlan(t, localtest.RawClient(sb), &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(plan.Rules, "w.global") {
		t.Errorf("the agent's plan %q honours the global excludes", plan.Rules)
	}
}

// TS-14-10: IncludeIgnored and a single-file export path produce no ignore
// rules, while escaping-symlink and .git rules remain. 14-REQ-3.2.
func TestNoIgnoreRules_TS14_10(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{".gitignore": "build/\n", "build/out.o": "o", "esc": "->/etc", ".git/HEAD": "h"})
	rs, err := rsyncx.Compute(ctx, src, rsyncx.RuleOptions{Ignore: hermetic, IncludeIgnored: true, ExcludeGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.List(true); !slices.Equal(got, []string{".git", "esc"}) {
		t.Errorf("rules %q, want [.git esc]", got)
	}
	isolate(t)
	sb := newLocal(t)
	ws := sb.Agent().Workdir
	writeFiles(t, ws, map[string]string{".gitignore": "*.log\n", "logs/app.log": "L", "cache.log": "c"})
	plan, err := rawPlan(t, localtest.RawClient(sb), &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "logs/app.log"})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.File || slices.ContainsFunc(plan.Rules, func(r string) bool { return r != ".git" }) {
		t.Errorf("the file plan %+v has ignore rules or lacks the file flag", plan)
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic, IncludeIgnored: true, Delete: true, ExcludeGit: true}); err != nil {
		t.Fatal(err)
	}
	if !lexists(filepath.Join(ws, "build/out.o")) || lexists(filepath.Join(ws, "esc")) || lexists(filepath.Join(ws, ".git")) {
		t.Error("the workspace lacks build/out.o or holds esc or .git")
	}
	if lexists(filepath.Join(ws, "cache.log")) {
		t.Error("cache.log survived Delete although IncludeIgnored left the agent no ignore rule")
	}
}

// TS-14-12: a Sync with ExcludeGit and every Export add one unanchored .git
// rule, and a Sync without ExcludeGit transfers .git whole. Under D-3 the
// rule travels in the args frame's rules, never as an --exclude.
// 14-REQ-3.4.
func TestGitRule_TS14_12(t *testing.T) {
	ctx := context.Background()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n",
		"sub/.git": "gitdir: ../.git/modules/sub\n", "nested/.git/HEAD": "n", "nested/n.txt": "n", "a.txt": "a"})
	count := func(rules []string) int {
		n := 0
		for _, r := range rules {
			if r == ".git" {
				n++
			}
		}
		return n
	}
	hasGit := func(rules []string) bool {
		return slices.ContainsFunc(rules, func(r string) bool { return slices.Contains(strings.Split(r, "/"), ".git") })
	}
	a := relaytest.New(t)
	if _, err := a.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic, ExcludeGit: true}); err != nil {
		t.Fatal(err)
	}
	if len(gitPaths(a.Workspace)) != 0 || !lexists(filepath.Join(a.Workspace, "a.txt")) || !lexists(filepath.Join(a.Workspace, "nested/n.txt")) {
		t.Errorf("with ExcludeGit the workspace is %v", treeOf(t, a.Workspace))
	}
	if n := count(a.Relay.Last().Args.Rules); n != 1 {
		t.Errorf("the args carry the .git rule %d times", n)
	}
	b := relaytest.New(t)
	if _, err := b.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/HEAD", ".git/config", "sub/.git", "nested/.git/HEAD"} {
		if readFile(t, filepath.Join(b.Workspace, p)) != readFile(t, filepath.Join(src, p)) {
			t.Errorf("%s differs from src", p)
		}
	}
	if hasGit(b.Relay.Last().Args.Rules) {
		t.Errorf("a Sync without ExcludeGit has rules %q", b.Relay.Last().Args.Rules)
	}
	for _, eg := range []bool{false, true} {
		d := filepath.Join(t.TempDir(), "dst")
		if err := b.Sandbox.Export(ctx, ".", sandbox.LocalDir{Path: d, Ignore: hermetic, ExcludeGit: eg}); err != nil {
			t.Fatal(err)
		}
		if g := gitPaths(d); len(g) != 0 {
			t.Errorf("ExcludeGit %v: the export wrote %v", eg, g)
		}
		if n := count(b.Relay.Last().Args.Rules); n != 1 {
			t.Errorf("ExcludeGit %v: the export's args carry the .git rule %d times", eg, n)
		}
	}
}
