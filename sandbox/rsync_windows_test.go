//go:build windows

package sandbox_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/relaytest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-14-41: on Windows the host refuses LocalDir transfers with
// ErrUnsupported naming the platform, and the agent answers Rsync with
// FailedPrecondition. 14-REQ-8.7.
func TestLocalDirWindows_TS14_41(t *testing.T) {
	ctx := context.Background()
	f := fakeagent.New()
	f.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: 1, Version: "v1", Workdir: "/ws"})
	sb := fakeSandbox(t, f)
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "dst")
	_, e1 := sb.Sync(ctx, sandbox.LocalDir{Path: src})
	e2 := sb.Export(ctx, ".", sandbox.LocalDir{Path: dst})
	for _, e := range []error{e1, e2} {
		if !errors.Is(e, sandbox.ErrUnsupported) || !strings.Contains(e.Error(), "windows") {
			t.Errorf("%v, want ErrUnsupported naming windows", e)
		}
	}
	if n := f.Count("Rsync"); n != 0 {
		t.Errorf("%d Rsync streams opened", n)
	}

	d := relaytest.Direct(t)
	st, err := localtest.RawClient(d.Sandbox).Rsync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC}}})
	fr, err := st.Recv()
	if fr != nil || status.Code(err) != codes.FailedPrecondition {
		t.Errorf("the raw stream got %v, %v; want FailedPrecondition before any plan", fr, err)
	}
}
