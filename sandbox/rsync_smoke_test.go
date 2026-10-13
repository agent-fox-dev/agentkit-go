//go:build !windows

package sandbox_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// hermeticEnv points HOME, XDG_CONFIG_HOME and git's global and system
// configuration at empty places, so the zero LocalDir.Ignore finds no
// global excludes.
func hermeticEnv(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", empty)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// TS-14-46: an embedder syncs a project into a local sandbox, re-syncs one
// change, and a proxy read_file sees it. 14-PATH-1, 14-REQ-4.2, 14-REQ-4.5.
func TestSmokeSyncResync_TS14_46(t *testing.T) {
	isolate(t)
	hermeticEnv(t)
	ctx := context.Background()
	src := t.TempDir()
	files := map[string]string{".gitignore": "build/\n", "build/out.o": "o", ".git/HEAD": "ref: refs/heads/main\n",
		"esc": "->/etc", "old.txt": "old", "notes.txt": "first"}
	for i := range 15 {
		files[fmt.Sprintf("pkg/f%02d.go", i)] = fmt.Sprintf("package pkg // %d\n", i)
	}
	writeFiles(t, src, files)
	sb := newLocal(t)
	ws := sb.Agent().Workdir
	r, err := sb.Sync(ctx, sandbox.LocalDir{Path: src, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	// 20 regular files in src; build/out.o is ignored.
	if r.Files != 19 || r.Sent <= 0 {
		t.Errorf("the first Sync = %+v, want Files 19 and a positive Sent", r)
	}
	if readFile(t, filepath.Join(ws, ".git/HEAD")) != files[".git/HEAD"] || lexists(filepath.Join(ws, "build")) || lexists(filepath.Join(ws, "esc")) {
		t.Error("the workspace lacks .git/HEAD or holds build or esc")
	}
	read := tool(t, sb, "read_file")
	newNotes := "second, and longer"
	if err := os.WriteFile(filepath.Join(src, "notes.txt"), []byte(newNotes), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(src, "old.txt")); err != nil {
		t.Fatal(err)
	}
	r, err = sb.Sync(ctx, sandbox.LocalDir{Path: src, Delete: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Files != 1 || r.Bytes != int64(len(newNotes)) || r.Deleted != 1 {
		t.Errorf("the second Sync = %+v, want Files 1, Bytes %d, Deleted 1", r, len(newNotes))
	}
	if lexists(filepath.Join(ws, "old.txt")) {
		t.Error("old.txt is still in the workspace")
	}
	if res := read.Execute(ctx, []byte(`{"path":"notes.txt"}`)); !res.OK || !strings.Contains(fmt.Sprint(res.Data["content"]), newNotes) {
		t.Errorf("read_file notes.txt = %+v", res)
	}
}

// TS-14-47: an embedder exports the model's edits into a host directory
// with Delete, and its .git and ignored .env are untouched. 14-PATH-2,
// 14-REQ-5.5.
func TestSmokeExportDelete_TS14_47(t *testing.T) {
	isolate(t)
	hermeticEnv(t)
	ctx := context.Background()
	sb := newLocal(t)
	dst := t.TempDir()
	writeFiles(t, dst, map[string]string{".git/HEAD": "ref: refs/heads/main\n", ".git/config": "[core]\n",
		".gitignore": ".env\n", ".env": "TOKEN=1\n", "stale.txt": "s"})
	// A workspace .git the export must leave behind.
	writeFiles(t, sb.Agent().Workdir, map[string]string{".git/HEAD": "evil", ".git/hooks/post-checkout": "#!/bin/sh\n"})
	gitB, envB := hashTree(t, filepath.Join(dst, ".git")), readFile(t, filepath.Join(dst, ".env"))
	proxyCall(t, sb, "write_file", `{"path":"out/result.txt","content":"result"}`)
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: dst, Delete: true}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dst, "out/result.txt")) != "result" {
		t.Error("dst/out/result.txt does not hold the written content")
	}
	if lexists(filepath.Join(dst, "stale.txt")) {
		t.Error("stale.txt survived Delete")
	}
	if hashTree(t, filepath.Join(dst, ".git")) != gitB || readFile(t, filepath.Join(dst, ".env")) != envB {
		t.Error("dst/.git or dst/.env changed")
	}
	if lexists(filepath.Join(dst, ".git/hooks")) {
		t.Error("the workspace's .git reached dst")
	}
}

// TS-14-48: hostile Rsync streams on a live local sandbox are refused with
// InvalidArgument, and the sandbox keeps working. 14-PATH-3, 14-REQ-6.1,
// 14-REQ-6.3, 14-REQ-6.4.
func TestSmokeHostileStreams_TS14_48(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	sb := newLocal(t)
	raw := localtest.RawClient(sb)
	plan, err := rawPlan(t, raw, &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_EXPORT, Path: "../outside"})
	if plan != nil || status.Code(err) != codes.InvalidArgument {
		t.Errorf("EXPORT ../outside: plan %v, %v; want InvalidArgument before plan", plan != nil, err)
	}
	base, err := rsyncx.ServerArgs(rsyncx.ClientArgs(false), false, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, opt := range []string{"--frobnicate", "--remove-source-files"} {
		st, err := raw.Rsync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC}}})
		if f, err := st.Recv(); err != nil || f.GetPlan() == nil {
			t.Fatalf("%s: want a plan, got %v, %v", opt, f, err)
		}
		args := slices.Insert(slices.Clone(base), 1, opt)
		_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Args{Args: &sandboxpb.RsyncArgs{Args: args}}})
		var rerr error
		for rerr == nil {
			_, rerr = st.Recv()
		}
		if status.Code(rerr) != codes.InvalidArgument || !strings.Contains(status.Convert(rerr).Message(), opt) {
			t.Errorf("%s: the stream ended %v, want InvalidArgument naming it", opt, rerr)
		}
	}
	proxyCall(t, sb, "list_files", `{"path":"."}`)
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: smallSrc(t), Ignore: hermetic}); err != nil {
		t.Errorf("a Sync after the hostile streams: %v", err)
	}
}
