//go:build windows

package agent

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-14-41 (agent half): a Windows-built agent ends an Rsync stream with
// FailedPrecondition before any plan frame. 14-REQ-8.7.
func TestRsyncWindows_TS14_41(t *testing.T) {
	c, _ := serveRaw(t, newAgent(t, "", testEnv(t), time.Hour))
	st, err := c.Rsync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC}}})
	f, err := st.Recv()
	if f != nil || status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, %v; want FailedPrecondition before any plan", f, err)
	}
	if _, err := c.Hello(context.Background(), &sandboxpb.HelloRequest{}); err != nil {
		t.Errorf("the agent stopped serving: %v", err)
	}
}
