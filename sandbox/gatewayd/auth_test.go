package gatewayd_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/peer"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// everyRPC makes every RPC of both services, and two no handler serves, with ctx.
func everyRPC(t *testing.T, env *gwtest.Env, ctx context.Context) map[string]error {
	t.Helper()
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	errs := map[string]error{}
	_, errs["Capabilities"] = gw.Capabilities(ctx, &sandboxpb.CapabilitiesRequest{})
	_, errs["Create"] = gw.Create(ctx, createReq(0))
	_, errs["SyncGit"] = gw.SyncGit(ctx, &sandboxpb.SyncGitRequest{Id: "x", Url: repo.URL})
	_, errs["List"] = gw.List(ctx, &sandboxpb.ListRequest{})
	_, errs["Destroy"] = gw.Destroy(ctx, &sandboxpb.DestroyRequest{Id: "x"})
	_, errs["Reap"] = gw.Reap(ctx, &sandboxpb.ReapRequest{})
	_, errs["Hello"] = ag.Hello(ctx, &sandboxpb.HelloRequest{})
	_, errs["Tools"] = ag.Tools(ctx, &sandboxpb.ToolsRequest{})
	_, errs["Call"] = ag.Call(ctx, &sandboxpb.CallRequest{Name: "read_file"})
	if st, err := ag.SyncTar(ctx); err == nil {
		_, errs["SyncTar"] = st.CloseAndRecv()
	}
	if es, err := ag.ExportTar(ctx, &sandboxpb.ExportRequest{}); err == nil {
		_, errs["ExportTar"] = es.Recv()
	}
	if rs, err := ag.Rsync(ctx); err == nil {
		_ = rs.Send(&sandboxpb.RsyncFrame{Frame: &sandboxpb.RsyncFrame_Start{Start: &sandboxpb.RsyncStart{}}})
		_ = rs.CloseSend()
		for errs["Rsync"] == nil {
			_, errs["Rsync"] = rs.Recv()
		}
	}
	errs["unserved method"] = cc.Invoke(ctx, "/agentkit.sandbox.v1.Agent/Future", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{})
	errs["unserved service"] = cc.Invoke(ctx, "/x.Y/Z", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{})
	return errs
}

// Each such RPC is refused in the tap, before any handler or backend; in callback mode after one Authenticator call per Bearer RPC.
func TestEveryRPCWithoutAnAcceptedTokenIsRefusedBeforeTheBackend(t *testing.T) {
	for _, mode := range []string{"static", "callback"} {
		be, a := gwtest.NewCounting(), newAuthn()
		cfg := gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}}
		if mode == "callback" {
			cfg = gatewayd.Config{Backend: be, Authenticate: a.Authenticate}
		}
		env, logs := start(t, cfg)
		testutil.Eventually(t, "the reap at Serve start", func() bool { return be.Reaps() == 1 })
		bearers := 0
		for _, auth := range []string{"", "Basic " + gwtest.Token, "Bearer " + tok40, "Bearer " + gwtest.Token[:31], "Bearer " + gwtest.TokenB} {
			errs := everyRPC(t, env, gwtest.Ctx(context.Background(), "", "authorization", auth))
			for name, err := range errs {
				wantRefused(t, mode+": "+name+" with authorization "+auth, err)
			}
			if strings.HasPrefix(auth, "Bearer ") {
				bearers += len(errs)
			}
		}
		if be.Creates() != 0 || be.Closes() != 0 || be.Syncs() != 0 || be.Reaps() != 1 || mode == "callback" && a.called() != bearers {
			t.Errorf("%s: the backend saw %d creates, %d closes, %d syncs, %d reaps; the authenticator %d calls for %d RPCs",
				mode, be.Creates(), be.Closes(), be.Syncs(), be.Reaps(), a.called(), bearers)
		}
		if out := logs.String(); strings.Contains(out, gwtest.Token[:31]) || strings.Contains(out, tok40) {
			t.Errorf("%s: the log contains a token", mode)
		}
	}
}

// The Authenticator gets the raw token and the peer; 1 to 256 bytes of any principal reach the backend, anything else is refused.
func TestCallbackModePrincipalRules(t *testing.T) {
	t.Setenv("GODEBUG", "panicnil=1") // recover() returns nil for panic(nil): the refusal must not rely on it
	principals := map[string]string{"empty": "", "long": strings.Repeat("a", 257), "max": strings.Repeat("a", 256), "a b~!=": "p",
		strings.Repeat("b", 31): "\x00\xff\n\té\x7f"}
	authenticate := func(ctx context.Context, tok string) (string, bool) {
		if _, ok := peer.FromContext(ctx); !ok || tok == "panic" {
			panic("PANIC-SECRET")
		} else if tok == "panic-nil" {
			panic(nil)
		}
		p, ok := principals[tok]
		return p, ok
	}
	be := gwtest.NewCounting()
	env, logs := start(t, gatewayd.Config{Backend: be, Authenticate: authenticate})
	gw := env.RawGateway(t)
	for _, tok := range []string{"empty", "long", "panic", "panic-nil"} {
		_, err := gw.Create(authed(tok), createReq(0))
		wantRefused(t, tok, err)
	}
	var want []gwtest.Principal
	for _, tok := range []string{"max", "a b~!=", strings.Repeat("b", 31)} {
		rawCreate(t, gw, tok)
		want = append(want, gwtest.Principal{Principal: principals[tok], OK: true})
	}
	if got := be.CreatePrincipals(); !slices.Equal(got, want) || strings.Contains(logs.String(), "PANIC-SECRET") {
		t.Errorf("the backend's Create principals are %v, want %v; or the log holds the panic value", got, want)
	}
}

// The RPC in flight finishes and the principal's sandboxes stay, reachable with its other tokens.
func TestRevokingATokenRefusesOnlyItsLaterRPCs(t *testing.T) {
	a := newAuthn("tok-a", "P", "tok-b", "P")
	env, _ := start(t, gatewayd.Config{Backend: gwtest.NewCounting(), Authenticate: a.Authenticate})
	sb := create(t, env.Dial(t, "tok-a"), unrestrictedSpec)
	exec, before := tool(t, sb, "execute"), a.called()
	done := make(chan bool, 1)
	go func() {
		done <- exec.Execute(context.Background(), []byte(`{"command":"sleep 0.3; echo finished"}`)).OK
	}()
	testutil.Eventually(t, "the execute RPC to be authenticated", func() bool { return a.called() > before })
	a.revoke("tok-a")
	if !<-done {
		t.Error("the execute in flight when its token was revoked did not return its result")
	}
	gw := env.RawGateway(t)
	_, err := gw.List(authed("tok-a"), &sandboxpb.ListRequest{})
	wantRefused(t, "the revoked token", err)
	if !slices.Contains(listIDs(t, gw, "tok-b"), sb.ID()) {
		t.Errorf("tok-b does not list the sandbox tok-a created")
	}
}

// Create, the Sync of a SyncGit and a Reap RPC carry the principal (a token's SHA-256 in static mode); the periodic reap none.
func TestTheBackendSeesThePrincipalOfTheRPCItServes(t *testing.T) {
	for _, c := range []struct{ mode, principal string }{{"static", hexSHA256(gwtest.Token)}, {"callback", "tenant-1"}} {
		be := gwtest.NewCounting()
		cfg := gatewayd.Config{Backend: be, ReapInterval: 10 * time.Millisecond}
		if c.mode == "callback" {
			cfg.Authenticate = newAuthn(gwtest.Token, c.principal).Authenticate
		}
		env, _ := start(t, cfg)
		testutil.Eventually(t, "the reap at Serve start", func() bool { return be.Reaps() >= 1 })
		gw := env.RawGateway(t)
		id := rawCreate(t, gw, gwtest.Token)
		if _, err := gw.SyncGit(authed(gwtest.Token), &sandboxpb.SyncGitRequest{Id: id, Url: repo.URL}); err != nil {
			t.Fatal(err) // local does not clone: a refusal, after the backend's Sync ran
		}
		if _, err := gw.Reap(authed(gwtest.Token), &sandboxpb.ReapRequest{}); err != nil {
			t.Fatal(err)
		}
		testutil.Eventually(t, "three periodic reaps", func() bool { return len(be.ReapPrincipals()) >= 4 })
		rpc := []gwtest.Principal{{Principal: c.principal, OK: true}}
		if !slices.Equal(be.CreatePrincipals(), rpc) || !slices.Equal(be.SyncPrincipals(), rpc) {
			t.Errorf("%s: Create principals %v, Sync principals %v; want %v each", c.mode, be.CreatePrincipals(), be.SyncPrincipals(), rpc)
		}
		reaps := be.ReapPrincipals()
		others := slices.DeleteFunc(slices.Clone(reaps), func(p gwtest.Principal) bool { return p == rpc[0] })
		if reaps[0] != (gwtest.Principal{}) || len(others) != len(reaps)-1 || slices.ContainsFunc(others, func(p gwtest.Principal) bool { return p.OK }) {
			t.Errorf("%s: the Reap principals are %v, want the RPC's once and none on every other", c.mode, reaps)
		}
	}
}
