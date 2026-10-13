//go:build !windows

package sandbox_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// writeFiles writes path -> content under root; "->target" makes a symlink.
func writeFiles(t *testing.T, root string, files map[string]string) {
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

func lexists(p string) bool { _, err := os.Lstat(p); return err == nil }

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var hermetic = tools.NoGlobalExcludes()

// TS-14-14: the combined rules are the deduplicated, sorted union of both
// sides' rules, they travel in the args frame (never as --exclude options;
// D-3), and wildcard names match only their own path. 14-REQ-3.6.
func TestCombinedRules_TS14_14(t *testing.T) {
	got := rsyncx.Combine([]string{"build", "a*b", "dup", "sp ace"}, []string{"dup", "q?x", "br[c]k", ".git"})
	want := []string{".git", "a*b", "br[c]k", "build", "dup", "q?x", "sp ace"}
	if !slices.Equal(got, want) {
		t.Errorf("Combine = %q, want %q", got, want)
	}
	e := relaytest.New(t)
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"axxb": "1", "qzx": "2", "brck": "3",
		"a*b": "->/etc", "q?x": "->../out", "br[c]k": "->/tmp"})
	if _, err := e.Sandbox.Sync(context.Background(), sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	args := e.Relay.Last().Args
	for _, r := range []string{"a*b", "q?x", "br[c]k"} {
		if !slices.Contains(args.Rules, r) {
			t.Errorf("the args frame's rules %q lack %s", args.Rules, r)
		}
	}
	if !slices.IsSorted(args.Rules) || len(slices.Compact(slices.Clone(args.Rules))) != len(args.Rules) {
		t.Errorf("the args frame's rules %q are not sorted and unique", args.Rules)
	}
	for _, a := range args.Args {
		if strings.HasPrefix(a, "--exclude") || strings.HasPrefix(a, "--filter") {
			t.Errorf("the args carry %q; rules never reach the library", a)
		}
	}
	for _, f := range []string{"axxb", "qzx", "brck"} {
		if !lexists(filepath.Join(e.Workspace, f)) {
			t.Errorf("%s is missing: a wildcard rule matched more than its own path", f)
		}
	}
	for _, l := range []string{"a*b", "q?x", "br[c]k"} {
		if lexists(filepath.Join(e.Workspace, l)) {
			t.Errorf("the escaping symlink %s was transferred", l)
		}
	}
}

// TS-14-18: a LocalDir Sync sends a SYNC start with an empty path and the
// flags, describes the agent's side in its args, and maps Path's contents
// onto the workspace root. 14-REQ-4.2.
func TestSyncHandshake_TS14_18(t *testing.T) {
	e := relaytest.New(t)
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"a/b.txt": "b", "c.txt": "c"})
	ctx := context.Background()
	if _, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Delete: true, ExcludeGit: true}); err != nil {
		t.Fatal(err)
	}
	tr := e.Relay.Last()
	s := tr.Start
	if s.Direction.String() != "SYNC" || s.Path != "" || s.IncludeIgnored || !s.ExcludeGit || !s.Delete {
		t.Errorf("start %+v", s)
	}
	p, err := rsyncx.ParseArgs(tr.Args.Args)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Recursive || !p.Links || !p.Times || !p.Perms || !p.Delete || p.Sender || len(p.Excludes) != 0 {
		t.Errorf("args %q parsed as %+v", tr.Args.Args, p)
	}
	for _, o := range p.Options {
		if o == "--server" || o == "--delete" {
			continue
		}
		if strings.HasPrefix(o, "--") || strings.Trim(o[1:], "rltp") != "" {
			t.Errorf("unexpected option %s: only --server, --delete and -r -l -t -p are used", o)
		}
	}
	if !slices.Contains(tr.Args.Rules, ".git") {
		t.Errorf("rules %q lack the .git rule of ExcludeGit", tr.Args.Rules)
	}
	if tr.Status != nil || tr.Done == nil {
		t.Errorf("transfer ended %v with done %v", tr.Status, tr.Done)
	}
	if _, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src}); err != nil {
		t.Fatal(err)
	}
	if p, _ := rsyncx.ParseArgs(e.Relay.Last().Args.Args); p.Delete {
		t.Error("a Sync without Delete declares --delete")
	}
	if readFile(t, filepath.Join(e.Workspace, "a/b.txt")) != "b" || lexists(filepath.Join(e.Workspace, filepath.Base(src))) {
		t.Error("Path's contents did not land at the workspace root")
	}
}

// TS-14-20: the done statistics count new and changed files, their bytes and
// deletions, Sent equals the host's data bytes, and the tool set is rebuilt.
// 14-REQ-4.4, 14-REQ-1.3.
func TestSyncStatistics_TS14_20(t *testing.T) {
	e := relaytest.New(t)
	ctx := context.Background()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"keep.txt": strings.Repeat("k", 100), "change.txt": strings.Repeat("c", 100), "gone.txt": "g"})
	old := time.Now().Add(-time.Hour)
	for _, f := range []string{"keep.txt", "change.txt", "gone.txt"} {
		_ = os.Chtimes(filepath.Join(src, f), old, old)
	}
	if _, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	proxies, err := e.Sandbox.Tools(ctx, sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	newsym := "package x\n\nfunc NewlyAdded() {}\n"
	_ = os.Remove(filepath.Join(src, "gone.txt"))
	writeFiles(t, src, map[string]string{"change.txt": strings.Repeat("C", 250), "new.txt": strings.Repeat("n", 40), "newsym.go": newsym})
	r, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Relay.Last().Done
	if r.Files != 3 || r.Bytes != int64(250+40+len(newsym)) || r.Deleted != 1 ||
		int64(r.Files) != d.Files || r.Bytes != d.Bytes || int64(r.Deleted) != d.Deleted {
		t.Errorf("result %+v, done %v; want 3 files, %d bytes, 1 deleted", r, d, 250+40+len(newsym))
	}
	if r.Sent <= 0 || r.Sent != e.Relay.Last().HostData() {
		t.Errorf("Sent %d, the host's data frames %d", r.Sent, e.Relay.Last().HostData())
	}
	for _, p := range proxies {
		if p.Name == "find_symbol" {
			res := p.Execute(ctx, []byte(`{"name":"NewlyAdded"}`))
			if !strings.Contains(res.Text+fmt.Sprint(res.Data), "newsym.go") {
				t.Errorf("find_symbol after the sync: %+v", res)
			}
		}
	}
}

// TS-14-21: re-syncing after one file of a 100-file 16 KiB tree changes
// transfers only that file and sends under a tenth of the first sync's
// bytes. 14-REQ-4.5.
func TestIncrementalResync_TS14_21(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	src := t.TempDir()
	for i := range 100 {
		b := make([]byte, 16384)
		_, _ = rand.Read(b)
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("f%02d", i)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ld := sandbox.LocalDir{Path: src, Ignore: hermetic}
	r1, err := sb.Sync(context.Background(), ld)
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16384)
	_, _ = rand.Read(b)
	f42 := filepath.Join(src, "f42")
	if err := os.WriteFile(f42, b, 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(f42, later, later)
	r2, err := sb.Sync(context.Background(), ld)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Files != 1 || r2.Bytes != 16384 || r2.Sent*10 >= r1.Sent {
		t.Errorf("first %+v, second %+v; want the second to move one file in under a tenth of the bytes", r1, r2)
	}
	if !bytes.Equal([]byte(readFile(t, filepath.Join(sb.Agent().Workdir, "f42"))), b) {
		t.Error("the workspace copy is not the new content")
	}
}

// TS-14-22: Delete removes a file dropped from src, no Delete keeps it, and
// a workspace entry the workspace's own .gitignore ignores survives.
// 14-REQ-4.6.
func TestSyncDelete_TS14_22(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ctx := context.Background()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{".gitignore": "*.o\n", "a.txt": "a", "b.txt": "b"})
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if r := tool(t, sb, "write_file").Execute(ctx, []byte(`{"path":"out/x.o","content":"object"}`)); !r.OK {
		t.Fatal(r)
	}
	ws := sb.Agent().Workdir
	_ = os.Remove(filepath.Join(src, "b.txt"))
	r1, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic})
	if err != nil || !lexists(filepath.Join(ws, "b.txt")) || r1.Deleted != 0 || !lexists(filepath.Join(ws, "out/x.o")) {
		t.Errorf("without Delete: %+v, %v; b.txt %v, out/x.o %v", r1, err, lexists(filepath.Join(ws, "b.txt")), lexists(filepath.Join(ws, "out/x.o")))
	}
	r2, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic, Delete: true})
	if err != nil || lexists(filepath.Join(ws, "b.txt")) || r2.Deleted < 1 {
		t.Errorf("with Delete: %+v, %v; b.txt %v", r2, err, lexists(filepath.Join(ws, "b.txt")))
	}
	if readFile(t, filepath.Join(ws, "out/x.o")) != "object" {
		t.Error("the workspace-ignored out/x.o did not survive")
	}
}

// TS-14-23: LocalDir Sync can be called repeatedly before and after Tools,
// and the proxies see the latest content. 14-REQ-4.7.
func TestRepeatedSync_TS14_23(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ctx := context.Background()
	src := t.TempDir()
	ld := sandbox.LocalDir{Path: src, Ignore: hermetic}
	writeFiles(t, src, map[string]string{"a.txt": "a"})
	for range 2 {
		if _, err := sb.Sync(ctx, ld); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sb.Tools(ctx, sandbox.ToolOptions{}); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, src, map[string]string{"b.txt": "has needle-xyz inside\n"})
	if _, err := sb.Sync(ctx, ld); err != nil {
		t.Fatal(err)
	}
	if r := tool(t, sb, "read_file").Execute(ctx, []byte(`{"path":"b.txt"}`)); !strings.Contains(r.Text, "needle-xyz") {
		t.Errorf("read_file b.txt: %+v", r)
	}
	if r := tool(t, sb, "search_files").Execute(ctx, []byte(`{"pattern":"needle-xyz"}`)); !strings.Contains(r.Text+fmt.Sprint(r.Data), "b.txt") {
		t.Errorf("search_files: %+v", r)
	}
	writeFiles(t, src, map[string]string{"c.txt": "c"})
	if _, err := sb.Sync(ctx, ld); err != nil {
		t.Fatal(err)
	}
	if r := tool(t, sb, "read_file").Execute(ctx, []byte(`{"path":"c.txt"}`)); !r.OK || !strings.Contains(fmt.Sprint(r.Data["content"]), "c") {
		t.Errorf("read_file c.txt: %+v", r)
	}
}
