package attach

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
)

// The agent's deliberate refusals leave the sandbox live, Unimplemented names
// the agent's protocol, and any other status makes it unavailable for good.
func TestClassifyTellsRefusalsFromTransportFailures(t *testing.T) {
	f := fakeOf(3)
	sb := attachFake(t, f, Config{})
	ctx, o := context.Background(), sandbox.ToolOptions{}
	for _, code := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.Unimplemented} {
		f.SetTools(nil, status.Error(code, "x"))
		n := f.Count("Tools")
		_, err := sb.Tools(ctx, o)
		if err == nil || errors.Is(err, sandbox.ErrUnavailable) || code == codes.Unimplemented && !strings.Contains(err.Error(), "1.3") {
			t.Errorf("%v: %v, want a refusal that leaves the sandbox live (naming 1.3 when Unimplemented)", code, err)
		}
		if _, _ = sb.Tools(ctx, o); f.Count("Tools") != n+2 {
			t.Errorf("%v: the second Tools call did not reach the agent", code)
		}
	}
	f.SetTools(nil, status.Error(codes.Internal, "x"))
	if _, err := sb.Tools(ctx, o); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Fatalf("Internal: %v, want ErrUnavailable", err)
	}
	n := f.Count("Tools")
	if _, err := sb.Tools(ctx, o); !errors.Is(err, sandbox.ErrUnavailable) || f.Count("Tools") != n {
		t.Errorf("after a transport failure Tools returned %v and made %d RPCs", err, f.Count("Tools")-n)
	}
}

// An unavailable or closed sandbox answers Tools, Sync and Export without an
// RPC, as Live reports it.
func TestGateRefusesWorkOnAnUnavailableOrClosedSandbox(t *testing.T) {
	ctx := context.Background()
	fu, fc := fakeagent.New(), fakeagent.New()
	unavail, closed := attachFake(t, fu, Config{}), attachFake(t, fc, Config{})
	Fail(unavail, "broken")
	if err := closed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	counts := func(f *fakeagent.Agent) [3]int {
		return [3]int{f.Count("Tools"), f.Count("SyncTar"), f.Count("ExportTar")}
	}
	beforeU, beforeC := counts(fu), counts(fc)
	for _, c := range []struct {
		sb   sandbox.Sandbox
		want error
	}{{unavail, sandbox.ErrUnavailable}, {closed, sandbox.ErrClosed}} {
		_, err1 := c.sb.Tools(ctx, sandbox.ToolOptions{})
		_, err2 := c.sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(emptyTar)})
		err3 := c.sb.Export(ctx, "", sandbox.TarOut{W: &bytes.Buffer{}})
		for i, err := range []error{err1, err2, err3, Live(c.sb)} {
			if !errors.Is(err, c.want) {
				t.Errorf("operation %d returned %v, want %v", i, err, c.want)
			}
		}
		if live := Live(c.sb); live.Error() != err1.Error() {
			t.Errorf("Live = %v, Tools = %v; want the same", live, err1)
		}
	}
	if counts(fu) != beforeU || counts(fc) != beforeC {
		t.Errorf("gated operations made RPCs: %v -> %v, %v -> %v", beforeU, counts(fu), beforeC, counts(fc))
	}
}
