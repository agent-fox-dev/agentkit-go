//go:build !windows

package attach

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func planFrame() *sandboxpb.RsyncFrame {
	return &sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Plan{Plan: &sandboxpb.RsyncPlan{}}}
}

// TS-14-38: a deliberate refusal on the Rsync stream, Unimplemented
// included, comes back with the agent's message and gRPC code, and the
// sandbox stays live. 14-REQ-8.3.
func TestRsyncRefusals_TS14_38(t *testing.T) {
	src := t.TempDir()
	for _, code := range []codes.Code{codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound, codes.Unimplemented} {
		f := minorOne()
		f.SetRsync(func(st sandboxpb.Agent_RsyncServer) error {
			_, _ = st.Recv()
			return status.Error(code, "agent says no")
		})
		sb := attachFake(t, f, Config{})
		_, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: src})
		if err == nil || !strings.Contains(err.Error(), "agent says no") || status.Code(err) != code {
			t.Errorf("%v: Sync = %v (code %v), want the agent's message and code", code, err, status.Code(err))
		}
		calls := f.Count("Call")
		if r := proxies(t, sb)["read_file"].Execute(context.Background(), []byte(`{}`)); r.Error == "sandbox_unavailable" {
			t.Errorf("%v: the sandbox did not stay live: %+v", code, r)
		}
		if f.Count("Call") != calls+1 {
			t.Errorf("%v: the Call did not reach the agent", code)
		}
		if n := f.Count("Rsync"); n != 1 {
			t.Errorf("%v: %d Rsync streams, want 1", code, n)
		}
	}
}

// TS-14-39: a transport failure, an OK stream without done, or an
// out-of-order agent frame makes the sandbox unavailable, and no transfer is
// retried. 14-REQ-8.4, 14-REQ-8.6.
func TestRsyncTransportFailures_TS14_39(t *testing.T) {
	src := t.TempDir()
	cases := map[string]func(*fakeagent.Agent) func(sandboxpb.Agent_RsyncServer) error{
		"internal": func(*fakeagent.Agent) func(sandboxpb.Agent_RsyncServer) error {
			return func(st sandboxpb.Agent_RsyncServer) error {
				_, _ = st.Recv()
				return status.Error(codes.Internal, "boom")
			}
		},
		"stopAfterPlan": func(f *fakeagent.Agent) func(sandboxpb.Agent_RsyncServer) error {
			return func(st sandboxpb.Agent_RsyncServer) error {
				_, _ = st.Recv()
				_ = st.Send(planFrame())
				go f.Stop()
				<-st.Context().Done()
				return st.Context().Err()
			}
		},
		"okNoDone": func(*fakeagent.Agent) func(sandboxpb.Agent_RsyncServer) error {
			return func(st sandboxpb.Agent_RsyncServer) error {
				_, _ = st.Recv()
				_ = st.Send(planFrame())
				_, _ = st.Recv()
				return nil
			}
		},
		"doneFirst": func(*fakeagent.Agent) func(sandboxpb.Agent_RsyncServer) error {
			return func(st sandboxpb.Agent_RsyncServer) error {
				_, _ = st.Recv()
				return st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Done{Done: &sandboxpb.RsyncDone{}}})
			}
		},
		"twoPlans": func(*fakeagent.Agent) func(sandboxpb.Agent_RsyncServer) error {
			return func(st sandboxpb.Agent_RsyncServer) error {
				_, _ = st.Recv()
				_ = st.Send(planFrame())
				_, _ = st.Recv()
				return st.Send(planFrame())
			}
		},
	}
	for name, behaviour := range cases {
		f := minorOne()
		f.SetRsync(behaviour(f))
		sb := attachFake(t, f, Config{})
		if _, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: src}); err == nil {
			t.Errorf("%s: Sync succeeded", name)
		}
		if n := f.Count("Rsync"); n != 1 {
			t.Errorf("%s: %d Rsync streams after one Sync", name, n)
		}
		if _, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: src}); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%s: the next Sync = %v, want ErrUnavailable", name, err)
		}
		if err := sb.Export(context.Background(), ".", sandbox.LocalDir{Path: t.TempDir()}); !errors.Is(err, sandbox.ErrUnavailable) {
			t.Errorf("%s: the next Export = %v, want ErrUnavailable", name, err)
		}
		if n := f.Count("Rsync"); n != 1 {
			t.Errorf("%s: %d Rsync streams after the later calls", name, n)
		}
	}
	// A successful transfer also opens exactly one stream: the fake receives
	// with the real client.
	f := minorOne()
	recv := t.TempDir()
	f.SetRsync(func(st sandboxpb.Agent_RsyncServer) error {
		_, _ = st.Recv()
		_ = st.Send(planFrame())
		_, _ = st.Recv()
		if err := rsyncx.RunClient(st.Context(), rsyncx.AgentConn(st), rsyncx.ClientArgs(false), recv); err != nil {
			return err
		}
		return st.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Done{Done: &sandboxpb.RsyncDone{}}})
	})
	sb := attachFake(t, f, Config{})
	if _, err := sb.Sync(context.Background(), sandbox.LocalDir{Path: src}); err != nil {
		t.Errorf("the successful case: %v", err)
	}
	if n := f.Count("Rsync"); n != 1 {
		t.Errorf("a successful Sync opened %d streams", n)
	}
}
