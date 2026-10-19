//go:build !windows

package sandbox_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// smallSrc is a fresh source directory with one small file.
func smallSrc(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"small.txt": "s"})
	return src
}

// TS-14-37: a closed or unavailable sandbox, or an agent below protocol
// minor 1, refuses LocalDir transfers before any walk or Rsync stream.
// 14-REQ-8.1, 14-REQ-8.2.
func TestLocalDirPrechecks_TS14_37(t *testing.T) {
	isolate(t)
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	dst := filepath.Join(t.TempDir(), "dst")
	closed := newLocal(t)
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	cut := newLocal(t)
	if err := localtest.Cut(cut); err != nil {
		t.Fatal(err)
	}
	// The cut is noticed by the next RPC.
	if _, err := cut.Sync(ctx, sandbox.Tar{R: bytes.NewReader(make([]byte, 1024))}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("a Tar sync after the cut: %v", err)
	}
	for _, c := range []struct {
		sb   sandbox.Sandbox
		want error
	}{{closed, sandbox.ErrClosed}, {cut, sandbox.ErrUnavailable}} {
		_, e1 := c.sb.Sync(ctx, sandbox.LocalDir{Path: missing})
		e2 := c.sb.Export(ctx, ".", sandbox.LocalDir{Path: dst})
		if !errors.Is(e1, c.want) || !errors.Is(e2, c.want) {
			t.Errorf("Sync = %v, Export = %v; want %v", e1, e2, c.want)
		}
	}
	f := fakeagent.New()
	old := fakeSandbox(t, f)
	_, e1 := old.Sync(ctx, sandbox.LocalDir{Path: smallSrc(t)})
	e2 := old.Export(ctx, ".", sandbox.LocalDir{Path: dst})
	for _, e := range []error{e1, e2} {
		if !errors.Is(e, sandbox.ErrUnsupported) || !strings.Contains(e.Error(), "1.0") || !strings.Contains(e.Error(), "1.1") {
			t.Errorf("a minor-0 agent: %v, want ErrUnsupported naming 1.0 and 1.1", e)
		}
	}
	if n := f.Count("Rsync"); n != 0 {
		t.Errorf("%d Rsync streams opened", n)
	}
	if lexists(dst) {
		t.Error("dst was created")
	}
}

// TS-14-24: cancelling ctx during a LocalDir Sync returns an error wrapping
// ctx.Err, keeps earlier files and leaves the sandbox live. 14-REQ-4.8.
func TestSyncCancelled_TS14_24(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	armed := make(chan struct{})
	e := relaytest.Throttled(t, relaytest.Throttle{Rate: 256 << 10, OnBytes: func(total int64) {
		select {
		case <-armed:
			if total >= 512<<10 {
				cancel()
			}
		default:
		}
	}})
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"earlier.txt": "e"})
	if _, err := e.Sandbox.Sync(context.Background(), sandbox.LocalDir{Path: src, Ignore: hermetic}); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, src, map[string]string{"big.bin": string(bytes.Repeat([]byte{7}, 16<<20))})
	close(armed)
	_, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sync = %v, want an error wrapping context.Canceled", err)
	}
	if !lexists(filepath.Join(e.Workspace, "earlier.txt")) {
		t.Error("earlier.txt is gone")
	}
	proxyCall(t, e.Sandbox, "list_files", `{"path":"."}`)
	if _, err := e.Sandbox.Sync(context.Background(), sandbox.LocalDir{Path: smallSrc(t), Ignore: hermetic}); err != nil {
		t.Errorf("a Sync after the cancel: %v", err)
	}
}

// TS-14-34: a library failure inside the agent while the stream is intact
// ends the RPC with FailedPrecondition carrying the library's message, and
// the sandbox stays live. 14-REQ-6.7.
func TestAgentLibraryFailure_TS14_34(t *testing.T) {
	d := relaytest.Direct(t)
	raw := localtest.RawClient(d.Sandbox)
	ctx := context.Background()
	args, err := rsyncx.ServerArgs(rsyncx.ClientArgs(false), false, ".")
	if err != nil {
		t.Fatal(err)
	}
	st, err := raw.Rsync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC}}})
	if f, err := st.Recv(); err != nil || f.GetPlan() == nil {
		t.Fatalf("want a plan frame, got %v, %v", f, err)
	}
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Args{Args: &sandboxpb.RsyncArgs{Args: args}}})
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: bytes.Repeat([]byte{0xFF}, 64)}})
	_ = st.CloseSend()
	for err == nil {
		_, err = st.Recv()
	}
	if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() == "" {
		t.Errorf("the raw stream ended %v, want FailedPrecondition with the library's message", err)
	}
	proxyCall(t, d.Sandbox, "list_files", `{"path":"."}`)
	st, _ = raw.Rsync(ctx)
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC}}})
	if f, err := st.Recv(); err != nil || f.GetPlan() == nil {
		t.Errorf("a new stream got %v, %v; want a plan", f, err)
	}
	_ = st.CloseSend()
	for err == nil {
		_, err = st.Recv()
	}

	e := relaytest.New(t)
	e.Relay.SetMutator(func(fromHost bool, index int, f *sandboxpb.RsyncFrame) *sandboxpb.RsyncFrame {
		data := f.GetData()
		if !fromHost || data == nil || index < 3 {
			return f // start, args and the first data frame pass unchanged
		}
		flipped := make([]byte, len(data))
		for i, b := range data {
			flipped[i] = ^b
		}
		return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Data{Data: flipped}}
	})
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"a.txt": strings.Repeat("a", 100<<10), "b/c.txt": "c"})
	if _, err := e.Sandbox.Sync(ctx, sandbox.LocalDir{Path: src, Ignore: hermetic}); err == nil {
		t.Error("the corrupted Sync succeeded")
	}
	e.Relay.SetMutator(nil)
	proxyCall(t, e.Sandbox, "list_files", `{"path":"."}`)
}

// TS-14-35: when the agent's TTL expires during a LocalDir Sync, the Sync
// fails and the sandbox becomes unavailable. 14-REQ-6.8.
func TestSyncPastTTL_TS14_35(t *testing.T) {
	e := relaytest.Throttled(t, relaytest.Throttle{Rate: 256 << 10, TTL: time.Second})
	src := t.TempDir()
	writeFiles(t, src, map[string]string{"big.bin": string(bytes.Repeat([]byte{9}, 8<<20))})
	start := time.Now()
	_, err := e.Sandbox.Sync(context.Background(), sandbox.LocalDir{Path: src, Ignore: hermetic})
	if err == nil || time.Since(start) >= 10*time.Second {
		t.Fatalf("Sync = %v after %v, want an error within 10s", err, time.Since(start))
	}
	select {
	case <-e.Served: //nolint:errcheck // only Serve's return matters here, not its error
	case <-time.After(5 * time.Second):
		t.Error("the agent's Serve has not returned")
	}
	if _, err := e.Sandbox.Sync(context.Background(), sandbox.LocalDir{Path: smallSrc(t)}); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("the next Sync = %v, want ErrUnavailable", err)
	}
}

// TS-14-40: a host-side rsync client failure with the stream intact cancels
// the stream, returns the wrapped library error and keeps the sandbox live.
// 14-REQ-8.5. With -p the receiver makes a read-only dst/out writable before
// filling it, so the write failure here is a non-empty directory where
// out/new.txt must land (docs/errata/14_sandbox_rsync_sync.md).
func TestHostClientFailure_TS14_40(t *testing.T) {
	isolate(t)
	sb := newLocal(t)
	ctx := context.Background()
	proxyCall(t, sb, "write_file", `{"path":"out/new.txt","content":"n"}`)
	dst := t.TempDir()
	writeFiles(t, dst, map[string]string{"out/new.txt/keep.txt": "k"})
	err := sb.Export(ctx, ".", sandbox.LocalDir{Path: dst, Ignore: hermetic})
	if err == nil || errors.Is(err, sandbox.ErrUnavailable) || errors.Unwrap(err) == nil ||
		!(strings.Contains(err.Error(), "out/new.txt") || strings.Contains(err.Error(), "permission denied")) {
		t.Fatalf("Export = %v, want a wrapped library error naming out/new.txt or permission denied", err)
	}
	if readFile(t, filepath.Join(dst, "out/new.txt/keep.txt")) != "k" {
		t.Error("the blocking directory changed")
	}
	proxyCall(t, sb, "list_files", `{"path":"."}`)
	if _, err := sb.Sync(ctx, sandbox.LocalDir{Path: smallSrc(t), Ignore: hermetic}); err != nil {
		t.Errorf("a Sync after the failure: %v", err)
	}
}
