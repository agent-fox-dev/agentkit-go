package attach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// minorOne is a fake agent reporting protocol 1.1.
func minorOne() *fakeagent.Agent {
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: 1, Version: "v1", Workdir: "/ws"})
	return f
}

// TS-14-17: Sync refuses an empty, missing or non-directory Path after the
// state check, naming the path and opening no Rsync stream. 14-REQ-4.1.
func TestSyncLocalDirPathChecks_TS14_17(t *testing.T) {
	f := minorOne()
	sb := attachFake(t, f, Config{})
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, p := range []string{"", filepath.Join(dir, "missing"), file} {
		r, err := sb.Sync(ctx, sandbox.LocalDir{Path: p})
		if r != (sandbox.SyncResult{}) || err == nil || !strings.Contains(err.Error(), strconv.Quote(p)) {
			t.Errorf("Sync(%q) = %+v, %v; want a zero result and an error quoting the path", p, r, err)
		}
	}
	if n := f.Count("Rsync"); n != 0 {
		t.Errorf("%d Rsync streams opened", n)
	}
	if r := proxies(t, sb)["read_file"].Execute(ctx, []byte(`{}`)); r.Error == "sandbox_unavailable" {
		t.Errorf("the sandbox did not stay live: %+v", r)
	}
	if err := sb.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: ""}); !errors.Is(err, sandbox.ErrClosed) {
		t.Errorf("Sync on a closed sandbox = %v, want ErrClosed before the path check", err)
	}
}

// TS-14-25: Export refuses a CleanRel-rejected path or an unusable
// destination before any RPC, naming it and creating nothing. 14-REQ-5.1,
// 14-REQ-5.2.
func TestExportLocalDirChecks_TS14_25(t *testing.T) {
	f := minorOne()
	sb := attachFake(t, f, Config{})
	ctx := context.Background()
	p := t.TempDir()
	dst := filepath.Join(p, "new")
	file := filepath.Join(p, "file")
	if err := os.WriteFile(file, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../x", "/etc", "a/../../b"} {
		err := sb.Export(ctx, bad, sandbox.LocalDir{Path: dst})
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("Export(%q) = %v, want an error naming it", bad, err)
		}
		if _, err := os.Lstat(dst); err == nil {
			t.Fatalf("Export(%q) created the destination", bad)
		}
	}
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: file}); err == nil || !strings.Contains(err.Error(), file) {
		t.Errorf("a regular-file destination: %v", err)
	}
	if b, _ := os.ReadFile(file); string(b) != "orig" {
		t.Error("the regular-file destination changed")
	}
	child := filepath.Join(p, "nope", "child")
	if err := sb.Export(ctx, ".", sandbox.LocalDir{Path: child}); err == nil || !strings.Contains(err.Error(), "nope/child") {
		t.Errorf("a missing-parent destination: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(p, "nope")); err == nil {
		t.Error("the missing parent was created")
	}
	if n := f.Count("Rsync"); n != 0 {
		t.Errorf("%d Rsync streams opened", n)
	}
	if r := proxies(t, sb)["read_file"].Execute(ctx, []byte(`{}`)); r.Error == "sandbox_unavailable" {
		t.Errorf("the sandbox did not stay live: %+v", r)
	}
}
