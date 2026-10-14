package gatewayd_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// TS-19-36: token B can neither list, destroy, relay to nor git-sync a
// sandbox created with token A. 19-REQ-5.6.
func TestOwnership_TS19_36(t *testing.T) {
	env := gwtest.Start(t, gatewayd.Config{Backend: local.Unisolated(), NetworkModes: unrestricted})
	pA, pB := env.Dial(t, gwtest.Token), env.Dial(t, gwtest.TokenB)
	ctx := context.Background()
	sbA := create(t, pA, unrestrictedSpec)
	infos, err := pB.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(infos, func(i gateway.Info) bool { return i.ID == sbA.ID() }) {
		t.Error("token B lists token A's sandbox")
	}
	if err := pB.Destroy(ctx, sbA.ID()); err != nil {
		t.Errorf("token B's Destroy: %v, want nil", err)
	}
	if r := toolOf(t, sbA, "execute").Execute(ctx, []byte(`{"command":"true"}`)); !r.OK {
		t.Errorf("execute through A after B's Destroy: %+v", r)
	}
	cc := env.Raw(t)
	_, err = sandboxpb.NewAgentClient(cc).Hello(gwtest.Ctx(ctx, gwtest.TokenB, gateway.SandboxIDKey, sbA.ID()), &sandboxpb.HelloRequest{})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), sbA.ID()) {
		t.Errorf("token B's Hello: %v, want Unavailable naming the id", err)
	}
	gw := sandboxpb.NewGatewayClient(cc)
	ra, ea := gw.SyncGit(gwtest.Ctx(ctx, gwtest.TokenB), &sandboxpb.SyncGitRequest{Id: sbA.ID(), Url: "https://example.test/r.git"})
	rb, eb := gw.SyncGit(gwtest.Ctx(ctx, gwtest.TokenB), &sandboxpb.SyncGitRequest{Id: "no-such-id", Url: "https://example.test/r.git"})
	if status.Code(ea) != status.Code(eb) || ra.GetRefusal().GetKind() != rb.GetRefusal().GetKind() || (ra.GetRefusal() == nil) != (rb.GetRefusal() == nil) {
		t.Errorf("SyncGit for A's id (%v, %v) differs from an unknown id (%v, %v)", ra, ea, rb, eb)
	}
}
