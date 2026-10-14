package gatewayd_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// listIDs is the ids a raw List with token answers.
func listIDs(t *testing.T, gw sandboxpb.GatewayClient, token string) []string {
	t.Helper()
	l, err := gw.List(gwtest.Ctx(context.Background(), token), &sandboxpb.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range l.GetSandboxes() {
		ids = append(ids, e.GetId())
	}
	return ids
}

// TS-19-40: capacity counts recorded and in-progress creates, holds under
// concurrency, and is freed by pruning dead entries. 19-REQ-6.3.
func TestCapacity_TS19_40(t *testing.T) {
	be := gwtest.NewCounting()
	release := make(chan struct{})
	be.SetBlock(release)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, MaxSandboxes: 2})
	p := env.Dial(t, gwtest.Token)
	results := make(chan error, 5)
	for range 5 {
		go func() {
			sb, err := p.Create(context.Background(), unrestrictedSpec)
			if err == nil {
				t.Cleanup(func() { _ = sb.Close(context.Background()) })
			}
			results <- err
		}()
	}
	// The three refusals come back while two creates wait in the backend.
	var errs []error
	for range 3 {
		errs = append(errs, <-results)
	}
	close(release)
	for range 2 {
		errs = append(errs, <-results)
	}
	ok, capacity := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, gateway.ErrCapacity):
			capacity++
		default:
			t.Errorf("an unexpected error: %v", err)
		}
	}
	if ok != 2 || capacity != 3 || be.Creates() != 2 {
		t.Fatalf("%d created, %d refused for capacity, %d backend creates; want 2, 3, 2", ok, capacity, be.Creates())
	}
	if err := localtest.Cut(be.Sandboxes()[0].Sandbox); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a sixth Create to succeed after the cut", func() bool {
		sb, err := p.Create(context.Background(), unrestrictedSpec)
		if err == nil {
			t.Cleanup(func() { _ = sb.Close(context.Background()) })
		}
		return err == nil
	})
}

// TS-19-42: an unrelayable backend sandbox, or a caller gone by the time the
// backend returns, is closed and nothing is recorded. 19-REQ-6.5.
func TestUnrelayableAndGoneCaller_TS19_42(t *testing.T) {
	be1 := gwtest.NewCounting()
	be1.SetUnrelayable(true)
	env1 := gwtest.Start(t, gatewayd.Config{Backend: be1, NetworkModes: unrestricted})
	gw1 := env1.RawGateway(t)
	_, err := gw1.Create(authed(), &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)})
	if status.Code(err) != codes.Internal || !strings.Contains(err.Error(), be1.Capabilities().Provider) {
		t.Errorf("an unrelayable sandbox gave %v, want Internal naming %q", err, be1.Capabilities().Provider)
	}
	if !gone(be1.Last().Dir()) || len(listIDs(t, gw1, gwtest.Token)) != 0 {
		t.Error("the unrelayable sandbox was left behind")
	}

	be2 := gwtest.NewCounting()
	be2.SetCtxWait(true)
	env2 := gwtest.Start(t, gatewayd.Config{Backend: be2, NetworkModes: unrestricted})
	gw2 := env2.RawGateway(t)
	ctx, cancel := context.WithTimeout(authed(), 200*time.Millisecond)
	defer cancel()
	_, _ = gw2.Create(ctx, &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)})
	eventually(t, "the backend to create after the deadline", func() bool { return len(be2.Sandboxes()) == 1 })
	eventually(t, "the late sandbox's directory to go", func() bool { return gone(be2.Last().Dir()) })
	if ids := listIDs(t, gw2, gwtest.Token); len(ids) != 0 {
		t.Errorf("List holds %v after the caller left", ids)
	}
}

// TS-19-44: Destroy, expiry and a failed liveness check each remove the
// entry and close the backend sandbox, and a failing Close still removes
// it. 19-REQ-6.7.
func TestRemoval_TS19_44(t *testing.T) {
	be := gwtest.NewCounting()
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Logger: logger})
	gw := env.RawGateway(t)
	id1 := rawCreate(t, gw, gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	if _, err := gw.Destroy(authed(), &sandboxpb.DestroyRequest{Id: id1}); err != nil {
		t.Fatal(err)
	}
	if !gone(be.ByID(id1).Dir()) {
		t.Error("the destroyed sandbox's directory is still there")
	}
	if _, err := gw.Destroy(authed(), &sandboxpb.DestroyRequest{Id: "no-such-id"}); err != nil {
		t.Errorf("Destroy of an unknown id: %v", err)
	}

	r, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 2*time.Second)})
	if err != nil || r.GetRefusal() != nil {
		t.Fatalf("Create: %v, %v", r.GetRefusal(), err)
	}
	expiring := be.ByID(r.GetId())
	deadline := time.Now().Add(4 * time.Second)
	for !gone(expiring.Dir()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !gone(expiring.Dir()) || slices.Contains(listIDs(t, gw, gwtest.Token), r.GetId()) {
		t.Error("the 2s-TTL sandbox is still there after 4 s")
	}
	if reason := destroyReason(logs, r.GetId()); reason != "expired" {
		t.Errorf("the expired sandbox's destroy reason is %q", reason)
	}

	id3 := rawCreate(t, gw, gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	if err := localtest.Cut(be.ByID(id3).Sandbox); err != nil {
		t.Fatal(err)
	}
	eventually(t, "List to omit the cut sandbox", func() bool { return !slices.Contains(listIDs(t, gw, gwtest.Token), id3) })
	eventually(t, "the cut sandbox's directory to go", func() bool { return gone(be.ByID(id3).Dir()) })

	be2 := gwtest.NewCounting()
	be2.SetCloseErr(errors.New("close failed"))
	env2 := gwtest.Start(t, gatewayd.Config{Backend: be2, NetworkModes: unrestricted})
	gw2 := env2.RawGateway(t)
	id := rawCreate(t, gw2, gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)
	_, err = gw2.Destroy(authed(), &sandboxpb.DestroyRequest{Id: id})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "close failed") {
		t.Errorf("a failing Close gave %v, want Unavailable carrying it", err)
	}
	if slices.Contains(listIDs(t, gw2, gwtest.Token), id) {
		t.Error("the entry whose Close failed is still listed")
	}
}

// destroyReason is the reason of the destroy line logged for id.
func destroyReason(logs *gwtest.Logs, id string) string {
	for _, m := range logs.Messages("sandbox destroyed") {
		if m["id"] == id {
			r, _ := m["reason"].(string)
			return r
		}
	}
	return ""
}
