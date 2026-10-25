package gatewayd_test

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Every token of the creator's principal reaches it; another principal's, even a byte off, gets an unknown id's answers.
func TestASandboxBelongsToThePrincipalThatCreatedIt(t *testing.T) {
	a := newAuthn("tok-a", "P\xff", "tok-b", "P\xff", "tok-q", "P\xfe")
	env, logs := start(t, gatewayd.Config{Backend: gwtest.NewSlot(nil, 19), Authenticate: a.Authenticate})
	id := create(t, env.Dial(t, "tok-a"), unrestrictedSpec).ID()
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	hello := func(token, target string) error {
		_, err := ag.Hello(authed(token, gateway.SandboxIDKey, target), &sandboxpb.HelloRequest{})
		return err
	}
	syncGit := func(token, target string) error {
		_, err := gw.SyncGit(authed(token), &sandboxpb.SyncGitRequest{Id: target, Url: repo.URL})
		return err
	}
	destroy := func(token, target string) error {
		_, err := gw.Destroy(authed(token), &sandboxpb.DestroyRequest{Id: target})
		return err
	}
	if err := destroy("tok-q", id); err != nil || slices.Contains(listIDs(t, gw, "tok-q"), id) || !slices.Contains(listIDs(t, gw, "tok-a"), id) {
		t.Errorf("another principal's Destroy: %v; it should answer OK, list nothing and leave the sandbox", err)
	}
	for name, call := range map[string]func(token, target string) error{"relay": hello, "SyncGit": syncGit} {
		foreign, unknown := call("tok-q", id), call("tok-q", "no-such-id")
		if status.Code(foreign) != codes.Unavailable || strings.ReplaceAll(foreign.Error(), id, "ID") != strings.ReplaceAll(unknown.Error(), "no-such-id", "ID") {
			t.Errorf("%s: another principal is answered %v, an unknown id %v; want the same Unavailable", name, foreign, unknown)
		}
	}
	for i, call := range []func(token, target string) error{syncGit, hello, destroy} { // Destroy last
		if err := call("tok-b", id); err != nil {
			t.Errorf("call %d (SyncGit, relay, Destroy) with a second token of the principal: %v", i, err)
		}
	}
	if !slices.Equal(destroys(logs, id), []string{"client"}) {
		t.Errorf("the sandbox was logged destroyed %v, want once by the client", destroys(logs, id))
	}
}

// The RPC naming it is answered Unavailable and the sandbox removed.
func TestASandboxWhoseBackendIsGoneIsRemovedWhenNamed(t *testing.T) {
	be := gwtest.NewCounting()
	env, logs := start(t, gatewayd.Config{Backend: be})
	cc := env.Raw(t)
	id := rawCreate(t, sandboxpb.NewGatewayClient(cc), gwtest.Token)
	must(t, localtest.Cut(be.ByID(id).Sandbox))
	testutil.Eventually(t, "the relay to answer the cut sandbox unavailable", func() bool {
		_, err := sandboxpb.NewAgentClient(cc).Hello(authed(gwtest.Token, gateway.SandboxIDKey, id), &sandboxpb.HelloRequest{})
		return status.Code(err) == codes.Unavailable && strings.Contains(err.Error(), "is no longer available")
	})
	if !gone(be.ByID(id).Dir()) || !slices.Equal(destroys(logs, id), []string{"unavailable"}) || len(listIDs(t, sandboxpb.NewGatewayClient(cc), gwtest.Token)) != 0 {
		t.Errorf("the cut sandbox: closed %v, logged %v; want closed, removed and logged unavailable", gone(be.ByID(id).Dir()), destroys(logs, id))
	}
}
