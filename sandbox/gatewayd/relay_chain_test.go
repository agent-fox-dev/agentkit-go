package gatewayd_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// A gateway whose backend is a client of another gateway relays the Agent
// protocol on to the sandbox that lives on the other one: the relay sends the
// backend sandbox's own outgoing metadata, which for a gateway client's sandbox
// is the agentkit-sandbox-id that names it there. Without it the other
// gateway answers every Agent RPC "the RPC carries no agentkit-sandbox-id".

// chainedGateway serves an inner gateway over be and an outer gateway whose
// backend is a client of the inner one, and returns the outer one.
func chainedGateway(t *testing.T, be sandbox.Provider) (outer *gwtest.Env) {
	t.Helper()
	inner := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	return gwtest.Start(t, gatewayd.Config{Backend: inner.Dial(t, gwtest.Token), NetworkModes: unrestricted})
}

// TestRelayThroughAGatewayBackend: a client of the outer gateway creates a
// sandbox, syncs files into it, reads one back with the proxy tool and closes
// the sandbox, which destroys it on the inner gateway's backend.
func TestRelayThroughAGatewayBackend(t *testing.T) {
	be := gwtest.NewCounting()
	outer := chainedGateway(t, be)
	p := outer.Dial(t, gwtest.Token)
	ctx := context.Background()

	sb, err := p.Create(ctx, unrestrictedSpec)
	if err != nil {
		t.Fatalf("Create through a gateway over a gateway: %v", err)
	}
	if _, err := sb.Sync(ctx, sandbox.Tar{R: bytes.NewReader(tarOf(t, map[string]string{"x": "hello"}))}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if r := readFile(t, sb, "x"); r["ok"] != true {
		t.Errorf("read_file through the two gateways: %v", r)
	}
	if err := sb.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
	eventually(t, "the inner backend's sandbox to be closed", func() bool { return be.Closes() == 1 })
}

// TestRelayThroughAGatewayBackendKeepsTheAgentBlind: what the first gateway's
// client sends beyond the sandbox id is not passed on, and the agent behind the
// second gateway never sees the authorization header or the sandbox id key, as
// behind one gateway (19-REQ-7.3).
func TestRelayThroughAGatewayBackendKeepsTheAgentBlind(t *testing.T) {
	be := gwtest.NewAgents(nil)
	outer := chainedGateway(t, be)
	cc := outer.Raw(t)
	id := rawCreate(t, sandboxpb.NewGatewayClient(cc), gwtest.Token, sandboxpb.NetworkMode_UNRESTRICTED)

	ctx := gwtest.Ctx(context.Background(), gwtest.Token, gateway.SandboxIDKey, id, "x-private", "hunter2")
	if _, err := sandboxpb.NewAgentClient(cc).Call(ctx, &sandboxpb.CallRequest{Name: "read_file"}); err != nil {
		t.Fatalf("Call through the two gateways: %v", err)
	}
	rec := be.ByID(id).Rec
	if rec.Count("Call") == 0 {
		t.Fatal("the agent served no Call")
	}
	md := rec.Metadata("Call")
	for _, key := range []string{"authorization", gateway.SandboxIDKey, "x-private"} {
		if md.Get(key) != nil {
			t.Errorf("the Call reached the agent with %s in its metadata %v", key, md)
		}
	}
}
