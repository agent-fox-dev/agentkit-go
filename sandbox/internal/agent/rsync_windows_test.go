//go:build windows

package agent

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// On Windows the agent ends an Rsync stream with FailedPrecondition before any
// plan, and keeps serving.
func TestRsyncIsUnsupportedOnWindows(t *testing.T) {
	c := configured(t, "")
	st, err := c.Rsync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{Direction: sandboxpb.RsyncStart_SYNC}}})
	if f, err := st.Recv(); f != nil || status.Code(err) != codes.FailedPrecondition {
		t.Errorf("got %v, %v; want FailedPrecondition before any plan", f, err)
	}
	if _, err := c.Hello(context.Background(), &sandboxpb.HelloRequest{}); err != nil {
		t.Errorf("the agent stopped serving: %v", err)
	}
}
