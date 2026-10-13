//go:build !windows

package sandbox_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
)

// proxyCall runs one proxy tool and fails the test when it fails.
func proxyCall(t *testing.T, sb sandbox.Sandbox, name, input string) string {
	t.Helper()
	r := tool(t, sb, name).Execute(context.Background(), []byte(input))
	if r.Error != "" {
		t.Fatalf("%s %s: %s %s", name, input, r.Error, r.Detail)
	}
	return r.Text
}

// stamp is an entry's type, content or target, and modification time.
func stamps(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return nil
		}
		var body string
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			body = "-> " + l
		case fi.IsDir():
			body = "dir"
		default:
			b, _ := os.ReadFile(p)
			body = fmt.Sprintf("%x", sha256.Sum256(b))
		}
		out[rel] = fmt.Sprintf("%s %v %s", body, fi.Mode(), fi.ModTime().Format(time.RFC3339Nano))
		return nil
	})
	return out
}

// hashTree hashes the paths, types, modes and contents under root.
func hashTree(t *testing.T, root string) string {
	t.Helper()
	m := stamps(t, root)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\x00", k, m[k])
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// TS-14-2: a relative or symlinked LocalDir path resolves against the
// working directory at call time and the transfer uses the resolved
// directory without changing the working directory. 14-REQ-1.2.
func TestLocalDirPathResolution_TS14_2(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ws := sb.Spec().Workdir
	ctx := context.Background()
	a, b := t.TempDir(), t.TempDir()
	writeFiles(t, a, map[string]string{"src/a.txt": "a", "srclink": "->" + filepath.Join(a, "src"),
		"dstlink": "->" + filepath.Join(a, "realdst")})
	if err := os.Mkdir(filepath.Join(a, "realdst"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, b, map[string]string{"src/b.txt": "b"})

	sameWd := func(f func()) {
		t.Helper()
		before, _ := os.Getwd()
		f()
		if after, _ := os.Getwd(); after != before {
			t.Errorf("the working directory moved from %s to %s", before, after)
		}
	}
	t.Chdir(a)
	sameWd(func() {
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: "src", Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
	})
	if !lexists(filepath.Join(ws, "a.txt")) {
		t.Error("the first Sync did not use A/src")
	}
	t.Chdir(b)
	sameWd(func() {
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: "src", Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
	})
	if !lexists(filepath.Join(ws, "b.txt")) {
		t.Error("the second Sync did not follow the working directory to B/src")
	}
	sameWd(func() {
		if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: filepath.Join(a, "srclink"), Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
	})
	if lexists(filepath.Join(ws, "srclink")) || !lexists(filepath.Join(ws, "a.txt")) {
		t.Error("the Sync from A/srclink did not transfer A/src's contents")
	}
	sameWd(func() {
		if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: "out", Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
	})
	if readFile(t, filepath.Join(b, "out", "b.txt")) != "b" {
		t.Error("the Export to out did not create B/out with the workspace files")
	}
	sameWd(func() {
		if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: filepath.Join(a, "dstlink"), Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
	})
	if readFile(t, filepath.Join(a, "realdst", "b.txt")) != "b" {
		t.Error("the Export to A/dstlink did not write into A/realdst")
	}
	if l, err := os.Readlink(filepath.Join(a, "dstlink")); err != nil || l != filepath.Join(a, "realdst") {
		t.Errorf("A/dstlink is now %q, %v", l, err)
	}
}

// TS-14-26: exporting a workspace directory creates a missing destination,
// maps the directory's contents onto it, and runs the client as receiver
// against a read-only server. 14-REQ-5.3.
func TestExportDirectory_TS14_26(t *testing.T) {
	e := relaytest.New(t)
	ctx := context.Background()
	proxyCall(t, e.Sandbox, "write_file", `{"path":"out/result.txt","content":"hello"}`)
	snap := stamps(t, e.Workspace)
	p := t.TempDir()
	if err := e.Sandbox.Export(ctx, ".", sandbox.LocalDir{Path: filepath.Join(p, "dst"), Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(p, "dst", "out", "result.txt")) != "hello" {
		t.Error("dst lacks out/result.txt")
	}
	first := e.Relay.Last()
	if err := e.Sandbox.Export(ctx, "out", sandbox.LocalDir{Path: filepath.Join(p, "dst2"), Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(p, "dst2", "result.txt")) != "hello" {
		t.Error("dst2 lacks result.txt")
	}
	for i, tr := range []*relaytest.Transfer{first, e.Relay.Last()} {
		if s := tr.Start; s.Direction.String() != "EXPORT" || s.Path != []string{".", "out"}[i] {
			t.Errorf("start %d: %+v", i, s)
		}
		o, err := rsyncx.ParseArgs(tr.Args.Args)
		if err != nil {
			t.Fatal(err)
		}
		if !o.Sender || !o.Recursive || !o.Links || !o.Times || !o.Perms || o.Delete {
			t.Errorf("args %q parsed as %+v", tr.Args.Args, o)
		}
		if !slices.Contains(tr.Args.Rules, ".git") {
			t.Errorf("rules %q lack .git", tr.Args.Rules)
		}
	}
	if got := stamps(t, e.Workspace); !mapsEqual(got, snap) {
		t.Errorf("the workspace changed: %v, was %v", got, snap)
	}
}

// TS-14-27: exporting a regular file writes it under its base name even
// when the workspace's ignore rules match it; a directory export applies
// them. 14-REQ-5.4.
func TestExportFile_TS14_27(t *testing.T) {
	e := relaytest.New(t)
	ctx := context.Background()
	writeFiles(t, e.Workspace, map[string]string{".gitignore": "*.log\n", "logs/app.log": "L1"})
	dst := t.TempDir()
	if err := e.Sandbox.Export(ctx, "logs/app.log", sandbox.LocalDir{Path: dst, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dst, "app.log")) != "L1" || lexists(filepath.Join(dst, "logs")) {
		t.Error("the file did not land as dst/app.log")
	}
	if p := e.Relay.Last().Plan; p == nil || !p.File {
		t.Errorf("plan %v lacks the regular-file flag", p)
	}
	dst3 := filepath.Join(t.TempDir(), "dst3")
	if err := e.Sandbox.Export(ctx, "logs", sandbox.LocalDir{Path: dst3, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if lexists(filepath.Join(dst3, "app.log")) {
		t.Error("a directory export sent the ignored app.log")
	}
}

// TS-14-28: Export with Delete removes destination files the workspace lacks
// but keeps dst's ignored files, its .git and its escaping symlinks.
// 14-REQ-5.5.
func TestExportDelete_TS14_28(t *testing.T) {
	e := relaytest.New(t)
	ctx := context.Background()
	outside := t.TempDir()
	dst := t.TempDir()
	writeFiles(t, dst, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n",
		".gitignore": ".env\n", ".env": "SECRET=1\n", "stale.txt": "old", "link-out": "->" + outside})
	writeFiles(t, e.Workspace, map[string]string{".gitignore": ".env\n", "result.txt": "r"})
	envB, gitB := readFile(t, filepath.Join(dst, ".env")), hashTree(t, filepath.Join(dst, ".git"))
	check := func() {
		t.Helper()
		if readFile(t, filepath.Join(dst, ".env")) != envB || hashTree(t, filepath.Join(dst, ".git")) != gitB {
			t.Error(".env or .git changed")
		}
		if l, err := os.Readlink(filepath.Join(dst, "link-out")); err != nil || l != outside {
			t.Errorf("link-out is %q, %v", l, err)
		}
	}
	if err := e.Sandbox.Export(ctx, ".", sandbox.LocalDir{Path: dst, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if !lexists(filepath.Join(dst, "stale.txt")) {
		t.Error("an Export without Delete removed stale.txt")
	}
	check()
	if err := e.Sandbox.Export(ctx, ".", sandbox.LocalDir{Path: dst, Ignore: hermetic, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if lexists(filepath.Join(dst, "stale.txt")) || readFile(t, filepath.Join(dst, "result.txt")) != "r" {
		t.Error("Delete left stale.txt or lost result.txt")
	}
	check()
}

// TS-14-30 (host half): a missing export path comes back from Export as
// NotFound naming it, and nothing is created. 14-REQ-6.1.
func TestExportMissingPath_TS14_30(t *testing.T) {
	e := relaytest.New(t)
	dst := filepath.Join(t.TempDir(), "dst")
	err := e.Sandbox.Export(context.Background(), "missing", sandbox.LocalDir{Path: dst})
	if status.Code(err) != codes.NotFound || !strings.Contains(fmt.Sprint(err), "missing") {
		t.Errorf("Export(missing) = %v, want NotFound naming it", err)
	}
	if lexists(dst) {
		t.Error("the destination was created")
	}
	if tr := e.Relay.Last(); tr == nil || tr.Plan != nil {
		t.Errorf("transfer %+v: want one stream with no plan frame", tr)
	}
	proxyCall(t, e.Sandbox, "list_files", `{"path":"."}`)
}

// TS-14-33 (Export half): an EXPORT stream cannot change the workspace,
// even with Delete. 14-REQ-6.6. The module and push checks are in
// internal/rsyncx/server_test.go.
func TestExportLeavesWorkspace_TS14_33(t *testing.T) {
	e := relaytest.New(t)
	writeFiles(t, e.Workspace, map[string]string{"a.txt": "a", "dir/b.txt": "b"})
	dst := t.TempDir()
	writeFiles(t, dst, map[string]string{"extra.txt": "x"})
	snap := stamps(t, e.Workspace)
	if err := e.Sandbox.Export(context.Background(), ".", sandbox.LocalDir{Path: dst, Delete: true, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	if got := stamps(t, e.Workspace); !mapsEqual(got, snap) {
		t.Errorf("the workspace changed: %v, was %v", got, snap)
	}
	if lexists(filepath.Join(dst, "extra.txt")) || readFile(t, filepath.Join(dst, "dir", "b.txt")) != "b" {
		t.Error("dst still has extra.txt or lacks dir/b.txt")
	}
}
