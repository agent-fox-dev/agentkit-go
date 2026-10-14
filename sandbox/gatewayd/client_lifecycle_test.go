package gatewayd_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// listed reports whether p lists id.
func listed(t *testing.T, p *gateway.Provider, id string) bool {
	t.Helper()
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(infos, func(i gateway.Info) bool { return i.ID == id })
}

// TS-19-17: Destroy removes a known sandbox, and an unknown or repeated id
// returns nil. 19-REQ-3.4.
func TestDestroy_TS19_17(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb := create(t, p, unrestrictedSpec)
	dir := be.ByID(sb.ID()).Dir()
	read := toolOf(t, sb, "read_file")
	for _, id := range []string{sb.ID(), sb.ID(), "no-such-id"} {
		if err := p.Destroy(ctx, id); err != nil {
			t.Errorf("Destroy(%s) = %v", id, err)
		}
	}
	if !gone(dir) || listed(t, p, sb.ID()) {
		t.Error("the destroyed sandbox is still there")
	}
	if r := read.Execute(ctx, []byte(`{"path":"x"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("read_file after Destroy: %+v, want sandbox_unavailable", r)
	}
}

// TS-19-26: a handshake failure, or a context ending after Gateway.Create,
// leaves no sandbox on the gateway or the backend. 19-REQ-4.5.
func TestCreateCleanup_TS19_26(t *testing.T) {
	be1 := gwtest.NewAgents(func(r *gwtest.Recorder) { r.SetHello(&sandboxpb.HelloResponse{ProtocolMajor: 99}) })
	env1 := gwtest.Start(t, gatewayd.Config{Backend: be1, NetworkModes: unrestricted})
	p1 := env1.Dial(t, gwtest.Token)
	sb, err := p1.Create(context.Background(), unrestrictedSpec)
	if sb != nil || !errors.Is(err, sandbox.ErrProtocol) {
		t.Fatalf("Create = %v, %v; want ErrProtocol", sb, err)
	}
	eventually(t, "the failed handshake's sandbox to go", func() bool {
		infos, err := p1.List(context.Background())
		return err == nil && len(infos) == 0 && gone(be1.Sandboxes()[0].Dir)
	})

	be2 := gwtest.NewCounting()
	ctx2, cancel := context.WithCancel(context.Background())
	defer cancel()
	be2.SetAfterCreate(func(rpc context.Context) { cancel(); <-rpc.Done() })
	env2 := gwtest.Start(t, gatewayd.Config{Backend: be2, NetworkModes: unrestricted})
	p2 := env2.Dial(t, gwtest.Token)
	sb, err = p2.Create(ctx2, unrestrictedSpec)
	if sb != nil || err == nil {
		t.Fatalf("Create = %v, %v; want a failure", sb, err)
	}
	eventually(t, "the abandoned sandbox to go", func() bool {
		infos, err := p2.List(context.Background())
		return err == nil && len(infos) == 0 && gone(be2.Last().Dir())
	})
}

// TS-19-28: closing a gateway sandbox destroys it on the gateway and removes
// the backend sandbox. 19-REQ-4.7.
func TestSandboxClose_TS19_28(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	p := env.Dial(t, gwtest.Token)
	ctx := context.Background()
	sb, err := p.Create(ctx, unrestrictedSpec)
	if err != nil {
		t.Fatal(err)
	}
	dir := be.ByID(sb.ID()).Dir()
	if err := sb.Close(ctx); err != nil || !gone(dir) || listed(t, p, sb.ID()) {
		t.Errorf("Close = %v; the sandbox should be gone everywhere", err)
	}
	if err := sb.Close(ctx); err != nil {
		t.Errorf("a second Close = %v", err)
	}
}
