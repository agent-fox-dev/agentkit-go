package gatewayd_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// callsWith is how many times a was called with tok.
func callsWith(a *authn, tok string) int {
	n := 0
	for _, c := range a.Calls() {
		if c.token == tok {
			n++
		}
	}
	return n
}

// relayExecute runs `execute` with command in sandbox id through the Agent
// relay, as the holder of token.
func relayExecute(t *testing.T, ag sandboxpb.AgentClient, token, id, command string) (*sandboxpb.CallResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return ag.Call(gwtest.Ctx(ctx, token, gateway.SandboxIDKey, id),
		&sandboxpb.CallRequest{Name: "execute", Input: fmt.Appendf(nil, `{"command":%q}`, command)})
}

// TS-25-14: revoking a token refuses later RPCs but leaves in-flight RPCs and
// the principal's sandboxes alone. 25-REQ-3.6.
func TestRevocation_TS25_14(t *testing.T) {
	a := newAuthn("tok-a", "P", "tok-b", "P")
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: a.Authenticate})
	sb := create(t, env.Dial(t, "tok-a"), unrestrictedSpec)
	exec := toolOf(t, sb, "execute")

	before := callsWith(a, "tok-a")
	done := make(chan bool, 1)
	go func() {
		r := exec.Execute(context.Background(), []byte(`{"command":"sleep 0.5; echo finished"}`))
		done <- r.OK
	}()
	eventually(t, "the execute RPC to be authenticated", func() bool { return callsWith(a, "tok-a") > before })
	a.Revoke("tok-a")

	if ok := <-done; !ok {
		t.Error("the execute that was in flight when its token was revoked did not return its result")
	}
	gw := env.RawGateway(t)
	_, err := gw.List(gwtest.Ctx(context.Background(), "tok-a"), &sandboxpb.ListRequest{})
	wantRefused(t, "an RPC with the revoked token", err)
	if ids := listIDs(t, gw, "tok-b"); !slices.Contains(ids, sb.ID()) {
		t.Errorf("tok-b lists %v, want the sandbox %s that tok-a created", ids, sb.ID())
	}
}

// TS-25-19: a second token of the same principal reaches the first token's
// sandbox through every RPC. 25-REQ-5.1, 25-REQ-5.2.
func TestSharedPrincipal_TS25_19(t *testing.T) {
	logs, logger := gwtest.NewLogs()
	a := newAuthn("tok-a", "P", "tok-b", "P")
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewSlot(nil, 19), NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger})
	sb := create(t, env.Dial(t, "tok-a"), unrestrictedSpec)
	id := sb.ID()
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	ctx := context.Background()

	if ids := listIDs(t, gw, "tok-b"); !slices.Contains(ids, id) {
		t.Errorf("tok-b lists %v, want %s", ids, id)
	}
	// SyncGit goes first: Tools, which a relayed Call needs, takes the GitRepo slot.
	if r, err := gw.SyncGit(gwtest.Ctx(ctx, "tok-b"), &sandboxpb.SyncGitRequest{Id: id, Url: repo.URL}); err != nil || r.GetRefusal() != nil {
		t.Errorf("SyncGit with tok-b: %v, %v", r.GetRefusal(), err)
	}
	if _, err := ag.Tools(gwtest.Ctx(ctx, "tok-b", gateway.SandboxIDKey, id), &sandboxpb.ToolsRequest{}); err != nil {
		t.Errorf("the relayed Tools with tok-b: %v", err)
	}
	if r, err := relayExecute(t, ag, "tok-b", id, "echo hi"); err != nil || !r.GetOk() {
		t.Errorf("the relay tool call with tok-b: %v, %v", r, err)
	}
	if _, err := gw.Destroy(gwtest.Ctx(ctx, "tok-b"), &sandboxpb.DestroyRequest{Id: id}); err != nil {
		t.Errorf("Destroy with tok-b: %v", err)
	}
	if got := destroyReason(logs, id); got != "client" {
		t.Errorf("the sandbox was destroyed with reason %q, want client", got)
	}
	if ids := listIDs(t, gw, "tok-a"); slices.Contains(ids, id) {
		t.Errorf("tok-a still lists the destroyed sandbox: %v", ids)
	}
}

// TS-25-20: a different principal gets the answers spec 19 gives for an
// unknown id. 25-REQ-5.3.
func TestForeignPrincipal_TS25_20(t *testing.T) {
	a := newAuthn("tok-p", "P", "tok-q", "Q")
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewSlot(nil, 20), NetworkModes: unrestricted, Authenticate: a.Authenticate})
	sb := create(t, env.Dial(t, "tok-p"), unrestrictedSpec)
	id, unknown := sb.ID(), "no-such-sandbox"
	cc := env.Raw(t)
	gw, ag := sandboxpb.NewGatewayClient(cc), sandboxpb.NewAgentClient(cc)
	ctx := context.Background()

	if ids := listIDs(t, gw, "tok-q"); slices.Contains(ids, id) {
		t.Errorf("tok-q lists %v, want P's sandbox omitted", ids)
	}
	if _, err := gw.Destroy(gwtest.Ctx(ctx, "tok-q"), &sandboxpb.DestroyRequest{Id: id}); err != nil {
		t.Errorf("Destroy with tok-q: %v, want OK", err)
	}
	if ids := listIDs(t, gw, "tok-p"); !slices.Contains(ids, id) {
		t.Errorf("tok-p lists %v after tok-q's Destroy, want the sandbox still registered", ids)
	}

	// The answers for P's id and for an id that never existed differ only in the id.
	relay := func(token, target string) error {
		_, err := ag.Hello(gwtest.Ctx(ctx, token, gateway.SandboxIDKey, target), &sandboxpb.HelloRequest{})
		return err
	}
	syncGit := func(token, target string) error {
		_, err := gw.SyncGit(gwtest.Ctx(ctx, token), &sandboxpb.SyncGitRequest{Id: target, Url: repo.URL})
		return err
	}
	for name, call := range map[string]func(token, target string) error{"relay": relay, "SyncGit": syncGit} {
		foreign, gone := call("tok-q", id), call("tok-q", unknown)
		if status.Code(foreign) != codes.Unavailable || !strings.Contains(foreign.Error(), id) {
			t.Errorf("%s for P's id: %v, want Unavailable naming the id", name, foreign)
		}
		if status.Code(gone) != status.Code(foreign) ||
			strings.ReplaceAll(status.Convert(foreign).Message(), id, "<id>") != strings.ReplaceAll(status.Convert(gone).Message(), unknown, "<id>") {
			t.Errorf("%s: P's id is answered %q, an unknown id %q", name, foreign, gone)
		}
	}
}

// TS-25-29: the SyncGit and Gateway.Reap RPCs pass the principal to the
// backend. 25-REQ-7.3, 25-REQ-7.4.
func TestSyncAndReapCarryPrincipal_TS25_29(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	eventually(t, "the reap at Serve start", func() bool { return be.Reaps() >= 1 })
	gw := env.RawGateway(t)
	id := rawCreate(t, gw, "tok-a", sandboxpb.NetworkMode_UNRESTRICTED)
	ctx := gwtest.Ctx(context.Background(), "tok-a")
	// The Counting backend's sandbox is local, which does not clone: the RPC
	// is answered with a refusal, after the Sync was called.
	if _, err := gw.SyncGit(ctx, &sandboxpb.SyncGitRequest{Id: id, Url: repo.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Reap(ctx, &sandboxpb.ReapRequest{}); err != nil {
		t.Fatal(err)
	}
	want := gwtest.Principal{Principal: "tenant-1", OK: true}
	if got := be.SyncPrincipals(); !slices.Equal(got, []gwtest.Principal{want}) {
		t.Errorf("the sandbox's Sync principals are %v, want [%v]", got, want)
	}
	if got := be.ReapPrincipals(); len(got) == 0 || got[len(got)-1] != want {
		t.Errorf("the backend's Reap principals are %v, want the last to be %v", got, want)
	}
}

// TS-25-30: the periodic reap carries no principal at Serve start and on
// every interval, in either mode. 25-REQ-7.5.
func TestPeriodicReapHasNoPrincipal_TS25_30(t *testing.T) {
	a := newAuthn("tok-a", "tenant-1")
	for name, cfg := range map[string]gatewayd.Config{
		"callback": {Authenticate: a.Authenticate},
		"static":   {Tokens: []string{gwtest.Token}},
	} {
		t.Run(name, func(t *testing.T) {
			be := gwtest.NewCounting()
			cfg.Backend, cfg.NetworkModes, cfg.ReapInterval = be, unrestricted, 20*time.Millisecond
			gwtest.Start(t, cfg)
			eventually(t, "three periodic reaps", func() bool { return len(be.ReapPrincipals()) >= 3 })
			for i, p := range be.ReapPrincipals() {
				if p != (gwtest.Principal{}) {
					t.Errorf("reap %d carried (%q, %v), want (\"\", false)", i, p.Principal, p.OK)
				}
			}
		})
	}
}

// TS-25-42: gwtest.Counting records the PrincipalFromContext result of every
// Create, Reap and Sync. 25-REQ-9.4.
func TestCountingRecordsPrincipals_TS25_42(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	eventually(t, "the reap at Serve start", func() bool { return be.Reaps() >= 1 })
	sb := create(t, env.Dial(t, "tok-a"), unrestrictedSpec)
	gw := env.RawGateway(t)
	ctx := gwtest.Ctx(context.Background(), "tok-a")
	if _, err := gw.SyncGit(ctx, &sandboxpb.SyncGitRequest{Id: sb.ID(), Url: repo.URL}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Reap(ctx, &sandboxpb.ReapRequest{}); err != nil {
		t.Fatal(err)
	}
	rpc := gwtest.Principal{Principal: "tenant-1", OK: true}
	if got := be.CreatePrincipals(); !slices.Equal(got, []gwtest.Principal{rpc}) {
		t.Errorf("CreatePrincipals = %v, want [%v]", got, rpc)
	}
	if got := be.Principals(); !slices.Equal(got, be.CreatePrincipals()) {
		t.Errorf("Principals = %v, want CreatePrincipals", got)
	}
	if got := be.SyncPrincipals(); !slices.Equal(got, []gwtest.Principal{rpc}) {
		t.Errorf("SyncPrincipals = %v, want [%v]", got, rpc)
	}
	// One Reap at Serve start and one from the RPC, in that order.
	if got := be.ReapPrincipals(); !slices.Equal(got, []gwtest.Principal{{}, rpc}) {
		t.Errorf("ReapPrincipals = %v, want [(\"\", false) %v]", got, rpc)
	}
}

// TS-25-44: an unchanged sandbox/gateway Provider works against servers in
// both modes. 25-REQ-9.5, 25-REQ-5.2.
func TestClientSeesNoModeDifference_TS25_44(t *testing.T) {
	a := newAuthn("lease-token", "tenant-1")
	servers := map[string]struct {
		cfg   gatewayd.Config
		token string
	}{
		"static":   {gatewayd.Config{Tokens: []string{gwtest.Token}}, gwtest.Token},
		"callback": {gatewayd.Config{Authenticate: a.Authenticate}, "lease-token"},
	}
	transcripts := map[string][]string{}
	for name, s := range servers {
		t.Run(name, func(t *testing.T) {
			s.cfg.Backend, s.cfg.NetworkModes = gwtest.NewCounting(), unrestricted
			env := gwtest.Start(t, s.cfg)
			p := env.Dial(t, s.token)
			ctx := context.Background()
			var steps []string
			note := func(what string, ok bool) { steps = append(steps, fmt.Sprintf("%s: %v", what, ok)) }

			sb, err := p.Create(ctx, unrestrictedSpec)
			note("create", err == nil)
			if err != nil {
				t.Fatal(err)
			}
			r := toolOf(t, sb, "execute").Execute(ctx, []byte(`{"command":"echo hi"}`))
			note("tool ran", r.OK)
			note("tool output holds hi", strings.Contains(fmt.Sprint(r.Data), "hi"))
			infos, err := p.List(ctx)
			note("list", err == nil)
			note("listed", slices.ContainsFunc(infos, func(i gateway.Info) bool { return i.ID == sb.ID() }))
			note("destroy", p.Destroy(ctx, sb.ID()) == nil)
			infos, err = p.List(ctx)
			note("list again", err == nil)
			note("listed after destroy", slices.ContainsFunc(infos, func(i gateway.Info) bool { return i.ID == sb.ID() }))
			transcripts[name] = steps
			for _, step := range steps {
				if strings.HasSuffix(step, ": false") && !strings.HasPrefix(step, "listed after destroy") {
					t.Errorf("%s: step %q", name, step)
				}
			}
		})
	}
	if !slices.Equal(transcripts["static"], transcripts["callback"]) {
		t.Errorf("the client saw %v against a static gateway and %v against a callback gateway", transcripts["static"], transcripts["callback"])
	}
}
