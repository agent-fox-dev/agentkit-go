package gatewayd_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Each is one WARN line with only the peer and the reason; a header with no token never reaches the Authenticator.
func TestARefusalIsLoggedWithThePeerAndItsReason(t *testing.T) {
	base := context.Background()
	noToken := []context.Context{base, gwtest.Ctx(base, "", "authorization", "Basic x"), gwtest.Ctx(base, "", "authorization", "bearer x"),
		gwtest.Ctx(base, "", "authorization", "Bearer "), metadata.AppendToOutgoingContext(base, "authorization", "Bearer x", "authorization", "Bearer x")}
	var calls atomic.Int32
	authenticate := func(_ context.Context, tok string) (string, bool) {
		if calls.Add(1); tok == "boom" {
			panic("boom")
		}
		return map[string]string{"empty": "", "long": strings.Repeat("a", 257)}[tok], tok == "empty" || tok == "long"
	}
	five := slices.Repeat([]string{"no token"}, 5)
	for _, c := range []struct {
		mode string
		cfg  gatewayd.Config
		ctxs []context.Context
		want []string
	}{
		{"static", gatewayd.Config{Tokens: []string{gwtest.Token}}, append(noToken, authed(tok40)), append(five, "refused")},
		{"callback", gatewayd.Config{Authenticate: authenticate}, append(noToken, authed(tok40), authed("empty"), authed("long"), authed("boom")),
			append(five, "refused", "invalid principal", "invalid principal", "panic")},
	} {
		c.cfg.Backend = gwtest.NewCounting()
		env, logs := start(t, c.cfg)
		gw := env.RawGateway(t)
		for _, ctx := range c.ctxs {
			_, err := gw.List(ctx, &sandboxpb.ListRequest{})
			wantRefused(t, c.mode, err)
		}
		var got []string
		for _, rec := range logs.Messages("authentication refused") {
			got = append(got, rec["reason"].(string))
			if keys := slices.Sorted(maps.Keys(rec)); rec["level"] != "WARN" || rec["peer"] == "" || !slices.Equal(keys, []string{"level", "msg", "peer", "reason", "time"}) {
				t.Errorf("%s: the record %v is not a WARN with only a peer and a reason", c.mode, rec)
			}
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: the refusal reasons are %q, want %q", c.mode, got, c.want)
		}
	}
	if n := calls.Load(); n != 4 {
		t.Errorf("the authenticator was called %d times, want 4: once for each RPC with a token", n)
	}
}

// Each removal closes the sandbox once, on context.Background (an embedder's Destroy on its own context), and logs its reason;
// the owner logged is a static principal's first 8 characters or a callback one in full, and no secret is logged.
func TestRemovalsAndCreatesAreLoggedWithoutSecrets(t *testing.T) {
	type key struct{}
	const principal, envValue, payload = "tenant-1-lease-0042", "ENV-SECRET-asdfgh", "TOOL-PAYLOAD-zxcvbn"
	for _, c := range []struct {
		mode, owner string
		cfg         gatewayd.Config
	}{
		{"static", hexSHA256(gwtest.Token)[:8], gatewayd.Config{Tokens: []string{gwtest.Token}}},
		{"callback", principal, gatewayd.Config{Authenticate: newAuthn(gwtest.Token, principal).Authenticate}},
	} {
		be := &ctxRec{Counting: gwtest.NewCounting(), ctxs: map[string][]context.Context{}}
		c.cfg.Backend = be
		env, logs := start(t, c.cfg)
		p, ctx := env.Dial(t, gwtest.Token), context.Background()
		client := create(t, p, sandbox.Spec{Network: unrestrictedSpec.Network, Env: map[string]string{"API_KEY": envValue}})
		if r := run(t, client, "write_file", `{"path":"f.txt","content":"`+payload+`"}`); !r.OK {
			t.Fatalf("%s: write_file: %+v", c.mode, r)
		}
		expired := create(t, p, sandbox.Spec{Network: unrestrictedSpec.Network, TTL: 50 * time.Millisecond})
		cut, embedder, atShutdown := create(t, p, unrestrictedSpec), create(t, p, unrestrictedSpec), create(t, p, unrestrictedSpec)
		must(t, client.Close(ctx))
		testutil.Eventually(t, "the expiry", func() bool { return len(destroys(logs, expired.ID())) == 1 })
		must(t, localtest.Cut(be.ByID(cut.ID()).Sandbox))
		testutil.Eventually(t, "the cut sandbox's removal", func() bool { _, _ = p.List(ctx); return len(destroys(logs, cut.ID())) == 1 })
		caller := context.WithValue(ctx, key{}, "the embedder's")
		must(t, env.Server.Destroy(caller, embedder.ID()))
		must(t, shutdown(t, env.Server))
		for sb, want := range map[sandbox.Sandbox]string{client: "client", expired: "expired", cut: "unavailable", embedder: "embedder", atShutdown: "shutdown"} {
			closes, wantCtx := be.closes(sb.ID()), context.Background()
			if want == "embedder" {
				wantCtx = caller
			}
			if got := destroys(logs, sb.ID()); !slices.Equal(got, []string{want}) || len(closes) != 1 || closes[0] != wantCtx {
				t.Errorf("%s: sandbox %s was logged destroyed %q and closed with %v, want once by %s", c.mode, sb.ID(), got, closes, want)
			}
		}
		created := logs.Messages("sandbox created")
		for _, m := range created {
			if m["owner"] != c.owner || m["id"] == "" || m["network"] != "unrestricted" || m["ttl"] == nil {
				t.Errorf("%s: the create line %v lacks an id, the owner %s, a TTL or the network", c.mode, m, c.owner)
			}
		}
		if len(created) != 5 || len(logs.Messages("backend reap")) == 0 {
			t.Errorf("%s: %d create lines and no reap line:\n%s", c.mode, len(created), logs.String())
		}
		for _, secret := range []string{gwtest.Token, hexSHA256(gwtest.Token), envValue, payload} {
			if strings.Contains(logs.String(), secret) {
				t.Errorf("%s: the log contains %q", c.mode, secret)
			}
		}
	}
}
