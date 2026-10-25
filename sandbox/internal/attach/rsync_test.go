//go:build !windows

package attach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

type rsyncFn = func(sandboxpb.Agent_RsyncServer) error

func planFrame(rules ...string) *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Plan{Plan: &sandboxpb.RsyncPlan{Rules: rules}}}
}

var doneFrame = &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Done{Done: &sandboxpb.RsyncDone{}}}

// afterArgs reads start, sends plan, reads the args frame and then runs then.
func afterArgs(plan *sandboxpb.RsyncFrame, then rsyncFn) rsyncFn {
	return func(st sandboxpb.Agent_RsyncServer) error {
		_, _ = st.Recv()
		_ = st.Send(plan)
		if _, err := st.Recv(); err != nil {
			return err
		}
		return then(st)
	}
}

// LocalDir transfers are refused before any walk or stream on a closed or
// unavailable sandbox and on an agent below protocol 1.1.
func TestLocalDirIsRefusedBeforeAnyStream(t *testing.T) {
	ctx := context.Background()
	dst := filepath.Join(t.TempDir(), "dst")
	closed, cut, old := fakeOf(1), fakeOf(1), fakeOf(0)
	sbClosed, sbCut, sbOld := attachFake(t, closed, Config{}), attachFake(t, cut, Config{}), attachFake(t, old, Config{})
	_ = sbClosed.Close(ctx)
	Fail(sbCut, "cut")
	for _, c := range []struct {
		sb   sandbox.Sandbox
		want error
		says string
	}{{sbClosed, sandbox.ErrClosed, ""}, {sbCut, sandbox.ErrUnavailable, "cut"}, {sbOld, sandbox.ErrUnsupported, "1.0"}} {
		_, e1 := c.sb.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()})
		e2 := c.sb.Export(ctx, ".", sandbox.LocalDir{Path: dst})
		for _, e := range []error{e1, e2} {
			if !errors.Is(e, c.want) || !strings.Contains(e.Error(), c.says) {
				t.Errorf("%v, want %v naming %q", e, c.want, c.says)
			}
		}
	}
	if closed.Count("Rsync")+cut.Count("Rsync")+old.Count("Rsync") != 0 || exists(dst) {
		t.Error("a refused transfer opened a stream or created the destination")
	}
}

// A bad Sync source or Export path or destination is refused naming it,
// opening no stream and creating nothing.
func TestLocalDirRefusesABadPath(t *testing.T) {
	f := fakeOf(1)
	sb := attachFake(t, f, Config{})
	ctx := context.Background()
	dir := testutil.Tree(t, map[string]string{"file": "orig"})
	file, dst := filepath.Join(dir, "file"), filepath.Join(dir, "new")
	for _, p := range []string{"", filepath.Join(dir, "missing"), file} {
		if r, err := sb.Sync(ctx, sandbox.LocalDir{Path: p}); r != (sandbox.SyncResult{}) || err == nil || !strings.Contains(err.Error(), strconv.Quote(p)) {
			t.Errorf("Sync(%q) = %+v, %v; want an error quoting the path", p, r, err)
		}
	}
	for _, c := range []struct{ path, to, says string }{
		{"../x", dst, "../x"}, {"/etc", dst, "/etc"}, {"a/../../b", dst, "a/../../b"}, {".", file, file},
		{".", filepath.Join(dir, "nope", "child"), "nope/child"}, {".", "", `""`},
	} {
		if err := sb.Export(ctx, c.path, sandbox.LocalDir{Path: c.to}); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("Export(%q, %q) = %v, want an error naming %s", c.path, c.to, err, c.says)
		}
	}
	if f.Count("Rsync") != 0 || exists(dst) || exists(filepath.Join(dir, "nope")) || read(file) != "orig" {
		t.Error("a refused transfer opened a stream or changed the destination")
	}
	isLive(t, sb)
}

// The agent's deliberate refusal comes back with its message and code, and
// the sandbox stays live; the host's own refusal of rules over 8 MiB too.
func TestRsyncRefusalsLeaveTheSandboxLive(t *testing.T) {
	src := t.TempDir()
	huge := strings.Repeat("x", 8<<20+1)
	for _, c := range []struct {
		fn   rsyncFn
		code codes.Code
		says string
	}{
		{func(st sandboxpb.Agent_RsyncServer) error {
			_, _ = st.Recv()
			return status.Error(codes.InvalidArgument, "agent says no")
		}, codes.InvalidArgument, "agent says no"},
		{func(st sandboxpb.Agent_RsyncServer) error {
			_, _ = st.Recv()
			return status.Error(codes.NotFound, "agent says no")
		}, codes.NotFound, "agent says no"},
		{nil, codes.Unimplemented, "1.1 does not implement Rsync"},
		{func(st sandboxpb.Agent_RsyncServer) error {
			_, _ = st.Recv()
			_ = st.Send(planFrame(huge))
			_, err := st.Recv()
			return err
		}, codes.Unknown, "8 MiB"},
	} {
		f := fakeOf(1)
		f.SetRsync(c.fn)
		sb := attachFake(t, f, Config{})
		_, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: src})
		if err == nil || !strings.Contains(err.Error(), c.says) || status.Code(err) != c.code {
			t.Errorf("Sync = %v (code %v), want %v saying %q", err, status.Code(err), c.code, c.says)
		}
		isLive(t, sb)
	}
}

// A transport failure or a malformed stream makes the sandbox unavailable,
// and nothing is retried.
func TestRsyncMalformedStreamsMakeTheSandboxUnavailable(t *testing.T) {
	src := t.TempDir()
	for name, fn := range map[string]rsyncFn{
		"internal": func(st sandboxpb.Agent_RsyncServer) error {
			_, _ = st.Recv()
			return status.Error(codes.Internal, "boom")
		},
		"no plan":         func(st sandboxpb.Agent_RsyncServer) error { _, _ = st.Recv(); return nil },
		"done first":      func(st sandboxpb.Agent_RsyncServer) error { _, _ = st.Recv(); return st.Send(doneFrame) },
		"OK without done": afterArgs(planFrame(), func(sandboxpb.Agent_RsyncServer) error { return nil }),
		"a second plan":   afterArgs(planFrame(), func(st sandboxpb.Agent_RsyncServer) error { return st.Send(planFrame()) }),
	} {
		f := fakeOf(1)
		f.SetRsync(fn)
		sb := attachFake(t, f, Config{})
		if _, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: src}); err == nil {
			t.Errorf("%s: Sync succeeded", name)
		}
		if err := sb.Export(context.Background(), ".", sandbox.LocalDir{Path: t.TempDir()}); !errors.Is(err, sandbox.ErrUnavailable) || f.Count("Rsync") != 1 {
			t.Errorf("%s: the next Export = %v after %d streams, want ErrUnavailable after 1", name, err, f.Count("Rsync"))
		}
	}
}

// A Sync whose context is cancelled mid-transfer returns its error and
// leaves the sandbox live; one the TTL interrupts leaves it unavailable.
func TestSyncEndsWithTheContextOrTheTTL(t *testing.T) {
	for _, ttl := range []time.Duration{time.Hour, 200 * time.Millisecond} {
		f := fakeOf(1)
		started := make(chan struct{})
		f.SetRsync(afterArgs(planFrame(), func(st sandboxpb.Agent_RsyncServer) error {
			close(started)
			<-st.Context().Done()
			return st.Context().Err()
		}))
		sb := attachFake(t, f, Config{Spec: sandbox.Spec{TTL: ttl}})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-started
			if ttl == time.Hour {
				cancel()
			}
		}()
		_, err := sb.Sync(ctx, sandbox.LocalDir{Path: t.TempDir()})
		cancel()
		if ttl == time.Hour && (!errors.Is(err, context.Canceled) || Live(sb) != nil) {
			t.Errorf("a cancelled Sync = %v, Live = %v; want context.Canceled and live", err, Live(sb))
		}
		if ttl != time.Hour && (err == nil || !errors.Is(Live(sb), sandbox.ErrUnavailable)) {
			t.Errorf("a Sync past the TTL = %v, Live = %v; want an error and unavailable", err, Live(sb))
		}
	}
}

// A Sync moves the source minus both sides' rules into the workspace,
// deletes what the source lacks and no rule protects, reports what changed,
// and the tools see the new files. A relative Path is resolved against the
// working directory at call time.
func TestSyncLocalDirFillsTheWorkspace(t *testing.T) {
	sb, ws := real(t)
	testutil.WriteTree(t, ws, map[string]string{".git/HEAD": "ref", ".git/info/exclude": "keep.log\n", "keep.log": "k", "stale.txt": "s"})
	base := testutil.Tree(t, map[string]string{"src/.gitignore": "*.tmp\n", "src/x.tmp": "t", "src/a.txt": "hello", "src/sub/b.txt": "b", "src/.git/HEAD": "other"})
	if err := os.Symlink("/etc", filepath.Join(base, "src", "esc")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	r, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: "src", Ignore: hermetic, ExcludeGit: true, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 3 || r.Bytes != int64(len("*.tmp\nhello"+"b")) || r.Deleted != 1 || r.Sent == 0 {
		t.Errorf("SyncResult = %+v; want 3 files of 12 bytes, stale.txt deleted, something sent", r)
	}
	for p, want := range map[string]bool{"a.txt": true, "sub/b.txt": true, "keep.log": true, "x.tmp": false, "esc": false, "stale.txt": false} {
		if exists(filepath.Join(ws, p)) != want {
			t.Errorf("%s exists = %v, want %v", p, !want, want)
		}
	}
	if read(filepath.Join(ws, ".git", "HEAD")) != "ref" {
		t.Error("the workspace's .git changed")
	}
	if res := proxies(t, sb)["read_file"].Execute(context.Background(), []byte(`{"path":"a.txt"}`)); !strings.Contains(res.Text+testutil.MustJSON(t, res.Data), "hello") {
		t.Errorf("read_file a.txt after the sync: %+v", res)
	}
}

// Export moves a workspace path, minus the rules, into a destination it
// creates or reaches through a symlink, leaving the workspace unchanged; a
// file goes under its base name whatever the ignore rules; Delete keeps the
// destination's ignored files, .git and escaping links; a missing path is
// NotFound naming it.
func TestExportLocalDirFillsTheDestination(t *testing.T) {
	sb, ws := real(t)
	testutil.WriteTree(t, ws, map[string]string{".gitignore": "*.log\n", "logs/app.log": "L", "out/r.txt": "r", ".git/HEAD": "ref"})
	if err := os.Symlink("/etc", filepath.Join(ws, "esc")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before := testutil.MustJSON(t, snapshot(ws))
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(base)
	for _, to := range []string{"new", "link"} {
		if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: to, Ignore: hermetic}); err != nil {
			t.Fatal(err)
		}
	}
	got := filepath.Join(base, "real")
	if read(filepath.Join(base, "new", "out", "r.txt")) != "r" || read(filepath.Join(got, "out", "r.txt")) != "r" {
		t.Error("the export did not land in new/ and through link/")
	}
	for _, p := range []string{"logs/app.log", "esc", ".git"} {
		if exists(filepath.Join(got, p)) {
			t.Errorf("the export carried %s", p)
		}
	}
	if err := sb.Export(ctx, "logs/app.log", sandbox.LocalDir{Path: "file", Ignore: hermetic}); err != nil || read(filepath.Join(base, "file", "app.log")) != "L" {
		t.Errorf("a file export: %v", err)
	}
	dst := testutil.Tree(t, map[string]string{".gitignore": ".env\n", ".env": "SECRET", ".git/HEAD": "mine", "stale.txt": "old"})
	if err := os.Symlink(base, filepath.Join(dst, "link-out")); err != nil {
		t.Fatal(err)
	}
	if err := sb.Export(ctx, "out", sandbox.LocalDir{Path: dst, Ignore: hermetic, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dst, "stale.txt")) || read(filepath.Join(dst, "r.txt")) != "r" || read(filepath.Join(dst, ".env")) != "SECRET" ||
		read(filepath.Join(dst, ".git", "HEAD")) != "mine" || !exists(filepath.Join(dst, "link-out")) {
		t.Error("Delete removed a protected entry or kept stale.txt")
	}
	err := sb.Export(ctx, "missing", sandbox.LocalDir{Path: "never"})
	if status.Code(err) != codes.NotFound || !strings.Contains(err.Error(), "missing") || exists(filepath.Join(base, "never")) {
		t.Errorf("Export(missing) = %v, want NotFound naming it and nothing created", err)
	}
	if after := testutil.MustJSON(t, snapshot(ws)); after != before {
		t.Error("an export changed the workspace")
	}
}

// snapshot is every entry under root with its size and modification time.
func snapshot(root string) map[string]string {
	out := map[string]string{}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			out[p] = fi.Mode().String() + fi.ModTime().String() + strconv.FormatInt(fi.Size(), 10)
		}
		return nil
	})
	return out
}

// A host-side client failure with the stream intact (a non-empty directory
// where a file must land) is the library's error; the sandbox stays live.
func TestExportClientFailureLeavesTheSandboxLive(t *testing.T) {
	sb, ws := real(t)
	testutil.WriteTree(t, ws, map[string]string{"out/new.txt": "n"})
	dst := testutil.Tree(t, map[string]string{"out/new.txt/keep.txt": "k"})
	err := sb.Export(context.Background(), ".", sandbox.LocalDir{Path: dst, Ignore: hermetic})
	if err == nil || errors.Is(err, sandbox.ErrUnavailable) || !strings.Contains(err.Error(), "rsync client") {
		t.Fatalf("Export = %v, want the client's error", err)
	}
	if read(filepath.Join(dst, "out", "new.txt", "keep.txt")) != "k" {
		t.Error("the blocking directory changed")
	}
	isLive(t, sb)
}
