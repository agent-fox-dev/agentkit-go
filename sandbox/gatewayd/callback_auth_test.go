package gatewayd_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// authCall is one call of an authn.
type authCall struct {
	token   string
	hasPeer bool
}

// authn is an Authenticator for tests: it maps tokens to principals, accepts
// any other token as the principal in any when that is set, and records each
// call with the token it got and whether its context carried a peer.
type authn struct {
	mu     sync.Mutex
	tokens map[string]string
	any    string
	calls  []authCall
}

// newAuthn maps each token to the principal that follows it.
func newAuthn(pairs ...string) *authn {
	a := &authn{tokens: map[string]string{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		a.tokens[pairs[i]] = pairs[i+1]
	}
	return a
}

func (a *authn) Authenticate(ctx context.Context, tok string) (string, bool) {
	_, hasPeer := peer.FromContext(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, authCall{token: tok, hasPeer: hasPeer})
	if p, ok := a.tokens[tok]; ok {
		return p, true
	}
	return a.any, a.any != ""
}

// Set maps tok to principal; Revoke stops accepting tok.
func (a *authn) Set(tok, principal string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens == nil {
		a.tokens = map[string]string{}
	}
	a.tokens[tok] = principal
}

func (a *authn) Revoke(tok string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.tokens, tok)
}

// Calls is every call so far, in order.
func (a *authn) Calls() []authCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.calls)
}

// wantRefused fails t unless err is the refusal every unauthenticated RPC gets.
func wantRefused(t *testing.T, what string, err error) {
	t.Helper()
	if status.Code(err) != codes.Unauthenticated || status.Convert(err).Message() != refusalText {
		t.Errorf("%s: %v, want Unauthenticated %q", what, err, refusalText)
	}
}

// TS-25-9: callback mode refuses a missing, repeated, non-Bearer and empty
// authorization without calling the authenticator. 25-REQ-3.1.
func TestCallbackNoTokenSkipsAuthenticator_TS25_9(t *testing.T) {
	a := &authn{any: "p"}
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: a.Authenticate})
	gw := env.RawGateway(t)
	base := context.Background()
	for name, ctx := range map[string]context.Context{
		"no header":    base,
		"two values":   metadata.AppendToOutgoingContext(base, "authorization", "Bearer aaaa", "authorization", "Bearer bbbb"),
		"basic":        gwtest.Ctx(base, "", "authorization", "Basic xyz"),
		"lower prefix": gwtest.Ctx(base, "", "authorization", "bearer x"),
		"empty token":  gwtest.Ctx(base, "", "authorization", "Bearer "),
	} {
		_, err := gw.List(ctx, &sandboxpb.ListRequest{})
		wantRefused(t, name, err)
	}
	if n := len(a.Calls()); n != 0 {
		t.Errorf("the authenticator was called %d times, want 0", n)
	}
}

// TS-25-10: the authenticator runs exactly once per unary RPC and per stream,
// with the tap context and the raw token. 25-REQ-3.2.
func TestAuthenticatorCalledOncePerRPC_TS25_10(t *testing.T) {
	a := &authn{any: "p"}
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: a.Authenticate})
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	ctx := context.Background()

	if _, err := gw.List(gwtest.Ctx(ctx, "t1"), &sandboxpb.ListRequest{}); err != nil {
		t.Fatal(err)
	}
	if n := len(a.Calls()); n != 1 {
		t.Fatalf("%d calls after one unary RPC, want 1", n)
	}
	// The stream names no sandbox, so the relay answers Unavailable; it was
	// authenticated all the same.
	if err := firstRecv(ag.SyncTar(gwtest.Ctx(ctx, "t2"))); status.Code(err) != codes.Unavailable {
		t.Fatalf("the stream: %v, want Unavailable from the relay", err)
	}
	if n := len(a.Calls()); n != 2 {
		t.Fatalf("%d calls after one stream, want 2", n)
	}
	for _, tok := range []string{"a", strings.Repeat("b", 31), "a b~!="} {
		before := len(a.Calls())
		if _, err := gw.List(gwtest.Ctx(ctx, tok), &sandboxpb.ListRequest{}); err != nil {
			t.Fatalf("token %q: %v", tok, err)
		}
		calls := a.Calls()
		if len(calls) != before+1 || calls[before].token != tok {
			t.Errorf("token %q: calls %v, want one more call with that token", tok, calls[before:])
		}
	}
	for i, c := range a.Calls() {
		if !c.hasPeer {
			t.Errorf("call %d (token %q): the context carried no peer address", i, c.token)
		}
	}
}

// TS-25-11: a refused callback-mode RPC is stopped in the tap, before any
// message is read and before any handler or backend runs. 25-REQ-3.3.
func TestCallbackRefusalStopsInTheTap_TS25_11(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn() // accepts nothing
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	// The server reaps the backend once when it starts; the RPCs below must
	// not add to it.
	eventually(t, "the reap at Serve start", func() bool { return be.Reaps() >= 1 })
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	ctx := gwtest.Ctx(context.Background(), "tok")

	rpcs := map[string]error{}
	_, rpcs["Create"] = gw.Create(ctx, &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)})
	_, rpcs["SyncGit"] = gw.SyncGit(ctx, &sandboxpb.SyncGitRequest{Id: "x", Url: "https://example.test/r.git"})
	_, rpcs["Reap"] = gw.Reap(ctx, &sandboxpb.ReapRequest{})
	rpcs["SyncTar"] = firstRecv(ag.SyncTar(ctx))
	rpcs["unserved Agent method"] = cc.Invoke(ctx, "/agentkit.sandbox.v1.Agent/Future", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{})
	rpcs["unserved service"] = cc.Invoke(ctx, "/x.Y/Z", &sandboxpb.HelloRequest{}, &sandboxpb.HelloResponse{})
	for name, err := range rpcs {
		wantRefused(t, name, err)
	}
	if n := len(a.Calls()); n != len(rpcs) {
		t.Errorf("the authenticator was called %d times for %d RPCs", n, len(rpcs))
	}
	if be.Creates() != 0 || be.Reaps() != 1 || be.Syncs() != 0 || be.Closes() != 0 {
		t.Errorf("the backend saw %d creates, %d reaps, %d syncs, %d closes; want 0, 1 (the start), 0, 0",
			be.Creates(), be.Reaps(), be.Syncs(), be.Closes())
	}
}

// TS-25-12: an accepted principal reaches the backend's Create context and
// the authenticator is not called again. 25-REQ-3.4, 25-REQ-7.2.
func TestPrincipalReachesCreate_TS25_12(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	gw := env.RawGateway(t)
	before := len(a.Calls())
	rawCreate(t, gw, "tok-a", sandboxpb.NetworkMode_UNRESTRICTED)
	if n := len(a.Calls()) - before; n != 1 {
		t.Errorf("the Create RPC called the authenticator %d times, want 1", n)
	}
	want := []gwtest.Principal{{Principal: "tenant-1", OK: true}}
	if got := be.Principals(); !slices.Equal(got, want) {
		t.Errorf("the backend's Create principals are %v, want %v", got, want)
	}
}

// TS-25-15: an empty principal is refused and no sandbox is created.
// 25-REQ-4.1.
func TestEmptyPrincipalRefused_TS25_15(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("tok-empty", "")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	_, err := env.RawGateway(t).Create(gwtest.Ctx(context.Background(), "tok-empty"),
		&sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)})
	wantRefused(t, "Create with an empty principal", err)
	if n := be.Creates(); n != 0 {
		t.Errorf("the backend created %d sandboxes, want 0", n)
	}
}

// TS-25-16: a 257-byte principal is refused and a 256-byte principal is
// accepted. 25-REQ-4.2.
func TestPrincipalLengthBound_TS25_16(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("tok-long", strings.Repeat("a", 257), "tok-max", strings.Repeat("a", 256))
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	gw := env.RawGateway(t)
	spec := &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)}
	_, err := gw.Create(gwtest.Ctx(context.Background(), "tok-long"), spec)
	wantRefused(t, "Create with a 257-byte principal", err)
	rawCreate(t, gw, "tok-max", sandboxpb.NetworkMode_UNRESTRICTED)
	if got := be.Principals(); len(got) != 1 || len(got[0].Principal) != 256 || !got[0].OK {
		t.Errorf("the backend's Create principals are %d entries (first of %d bytes), want one of 256 bytes", len(got), len(got[0].Principal))
	}
}

// TS-25-17: a panicking authenticator is recovered and the server keeps
// serving. 25-REQ-4.3.
func TestAuthenticatorPanicRecovered_TS25_17(t *testing.T) {
	authenticate := func(_ context.Context, tok string) (string, bool) {
		if tok == "tok-boom" {
			panic("PANIC-SECRET-xyz")
		}
		return "p", tok == "tok-ok"
	}
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: authenticate})
	gw := sandboxpb.NewGatewayClient(env.Raw(t))
	_, err := gw.List(gwtest.Ctx(context.Background(), "tok-boom"), &sandboxpb.ListRequest{})
	wantRefused(t, "the panicking token", err)
	if _, err := gw.List(gwtest.Ctx(context.Background(), "tok-ok"), &sandboxpb.ListRequest{}); err != nil {
		t.Errorf("the next RPC on the same connection: %v", err)
	}
	if _, err := env.RawGateway(t).List(gwtest.Ctx(context.Background(), "tok-ok"), &sandboxpb.ListRequest{}); err != nil {
		t.Errorf("an RPC on a new connection: %v", err)
	}
}

// TS-25-18 (property): any principal of 1 to 256 bytes reaches the backend
// unchanged. 25-REQ-4.4.
func TestPrincipalBytesRoundTrip_TS25_18(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate, MaxSandboxes: 64})
	gw := env.RawGateway(t)
	r := rand.New(rand.NewPCG(25, 18))
	lengths := []int{1, 2, 255, 256}
	for len(lengths) < 24 {
		lengths = append(lengths, 1+r.IntN(256))
	}
	for i, n := range lengths {
		p := make([]byte, n)
		for j := range p {
			p[j] = byte(r.IntN(256)) // non-ASCII, invalid UTF-8 and control bytes included
		}
		off := slices.Clone(p)
		off[r.IntN(n)] ^= 0x01
		a.Set("tok", string(p))
		a.Set("tok-same", string(p))
		a.Set("tok-off", string(off))

		id := rawCreate(t, gw, "tok", sandboxpb.NetworkMode_UNRESTRICTED)
		got := be.Principals()
		if len(got) != i+1 || got[i].Principal != string(p) || !got[i].OK {
			t.Fatalf("principal %x: the backend saw %q (ok %v)", p, got[len(got)-1].Principal, got[len(got)-1].OK)
		}
		if !slices.Contains(listIDs(t, gw, "tok-same"), id) {
			t.Errorf("principal %x: a second token of the same bytes does not see the sandbox", p)
		}
		if slices.Contains(listIDs(t, gw, "tok-off"), id) {
			t.Errorf("principal %x: a token differing in one byte sees the sandbox", p)
		}
	}
}

// TS-25-41: gwtest.Start defaults Tokens only when both Tokens and
// Authenticate are unset. 25-REQ-9.3.
func TestStartDefaultsTokensOnlyWhenUnset_TS25_41(t *testing.T) {
	ctx := context.Background()
	dial := func(env *gwtest.Env, tok string) error {
		p, err := gateway.Dial(ctx, gwtest.URL, gateway.Options{Token: tok, Dialer: env.Listener.Dial})
		if err == nil {
			_ = p.Close()
		}
		return err
	}

	e1 := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted})
	for _, tok := range []string{gwtest.Token, gwtest.TokenB} {
		if err := dial(e1, tok); err != nil {
			t.Errorf("an empty Config: Dial with %s: %v, want the default tokens accepted", tok[:12], err)
		}
	}

	a := newAuthn("lease-1", "tenant-1")
	e2 := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: a.Authenticate})
	if err := dial(e2, "lease-1"); err != nil {
		t.Errorf("callback mode: Dial with the authenticator's token: %v", err)
	}
	if err := dial(e2, gwtest.Token); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("callback mode: Dial with the default token: %v, want ErrUnauthenticated", err)
	}

	e3 := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Tokens: []string{tok40}})
	if err := dial(e3, tok40); err != nil {
		t.Errorf("explicit Tokens: Dial with the configured token: %v", err)
	}
	if err := dial(e3, gwtest.Token); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Errorf("explicit Tokens: Dial with a default token: %v, want ErrUnauthenticated", err)
	}
}
