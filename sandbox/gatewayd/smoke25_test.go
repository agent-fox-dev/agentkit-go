package gatewayd_test

import (
	"context"
	"errors"
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

// The smoke tests below use no stand-in for the server, its tap handle and
// interceptors, the registry, the sandbox/gateway client, the Agent relay, a
// local provider (through gwtest.Counting) or the in-memory gRPC listener.

// TS-25-45 (smoke, 25-PATH-1): an embedder authenticates a lease token, the
// backend sees the principal at Create, and a tool runs through the relay.
// 25-REQ-3.4, 25-REQ-6.1, 25-REQ-7.2.
func TestSmokeLeaseToken_TS25_45(t *testing.T) {
	const principal = "tenant-1-lease-0042"
	be := gwtest.NewCounting()
	a := newAuthn("lease-token", principal, "lease-raw", principal)
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger})
	ctx := context.Background()

	p := env.Dial(t, "lease-token")
	sb := create(t, p, unrestrictedSpec)
	r := toolOf(t, sb, "execute").Execute(ctx, []byte(`{"command":"echo through-the-relay"}`))
	if !r.OK || !strings.Contains(r.Text+" "+fmt.Sprint(r.Data), "through-the-relay") {
		t.Errorf("the tool result did not reach the client: %+v", r)
	}
	if got := be.Principals(); len(got) != 1 || got[0] != (gwtest.Principal{Principal: principal, OK: true}) {
		t.Errorf("the backend's Create principals are %v, want [(%q, true)]", got, principal)
	}
	created := logs.Messages("sandbox created")
	if len(created) != 1 || created[0]["owner"] != principal {
		t.Errorf("sandbox created lines %v, want one with owner %q", created, principal)
	}

	// The client makes several RPCs for one Create; a raw Create counts the
	// authenticator's calls for exactly one.
	before := callsWith(a, "lease-raw")
	rawCreate(t, env.RawGateway(t), "lease-raw", sandboxpb.NetworkMode_UNRESTRICTED)
	if n := callsWith(a, "lease-raw") - before; n != 1 {
		t.Errorf("the authenticator was called %d times for one Create RPC, want 1", n)
	}
}

// TS-25-46 (smoke, 25-PATH-2): the embedder ends a leased sandbox mid-call and
// the client sees it gone. 25-REQ-8.1, 25-REQ-8.3, 25-REQ-8.6.
func TestSmokeEmbedderDestroy_TS25_46(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("lease-token", "tenant-1")
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger})
	p := env.Dial(t, "lease-token")
	ctx := context.Background()
	sb := create(t, p, unrestrictedSpec)
	exec := toolOf(t, sb, "execute")

	before := callsWith(a, "lease-token")
	inFlight := make(chan string, 1)
	go func() { inFlight <- exec.Execute(ctx, []byte(`{"command":"sleep 30"}`)).Error }()
	eventually(t, "the long call to be authenticated", func() bool { return callsWith(a, "lease-token") > before })
	time.Sleep(200 * time.Millisecond) // the call is past the relay's routing

	if err := env.Server.Destroy(ctx, sb.ID()); err != nil {
		t.Fatalf("Destroy = %v", err)
	}
	select {
	case code := <-inFlight:
		if code != "sandbox_unavailable" {
			t.Errorf("the call in flight ended with %q, want sandbox_unavailable", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call in flight did not end within 10 s")
	}
	if r := exec.Execute(ctx, []byte(`{"command":"true"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("the next proxy call: %+v, want sandbox_unavailable", r)
	}
	if listed(t, p, sb.ID()) {
		t.Error("List still has the sandbox")
	}
	if got := destroyReason(logs, sb.ID()); got != "embedder" {
		t.Errorf("the destroy reason is %q, want embedder", got)
	}
	if n := be.Closes(); n != 1 {
		t.Errorf("the backend closed %d sandboxes, want 1", n)
	}
	if err := env.Server.Destroy(ctx, sb.ID()); !errors.Is(err, gatewayd.ErrNotHeld) {
		t.Errorf("a second Destroy = %v, want ErrNotHeld", err)
	}
}

// TS-25-47 (smoke, 25-PATH-3): two tokens of one principal share a sandbox
// until one is revoked. 25-REQ-3.6, 25-REQ-5.2, 25-REQ-5.3, 25-REQ-6.4.
func TestSmokeSharedPrincipal_TS25_47(t *testing.T) {
	a := newAuthn("tok-a", "P", "tok-b", "P", "tok-c", "Q")
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger})
	ctx := context.Background()
	id := create(t, env.Dial(t, "tok-a"), unrestrictedSpec).ID()
	pb, pc := env.Dial(t, "tok-b"), env.Dial(t, "tok-c")
	gw, ag := env.RawGateway(t), sandboxpb.NewAgentClient(env.Raw(t))

	if !listed(t, pb, id) {
		t.Error("tok-b does not list the sandbox tok-a created")
	}
	if listed(t, pc, id) {
		t.Error("tok-c lists a sandbox of another principal")
	}
	_, err := gw.SyncGit(gwtest.Ctx(ctx, "tok-c"), &sandboxpb.SyncGitRequest{Id: id, Url: repo.URL})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), id) {
		t.Errorf("tok-c's SyncGit = %v, want Unavailable naming the id", err)
	}

	a.Revoke("tok-a")
	_, err = gw.List(gwtest.Ctx(ctx, "tok-a"), &sandboxpb.ListRequest{})
	wantRefused(t, "an RPC with the revoked token", err)
	var reasons []any
	for _, rec := range logs.Messages("authentication refused") {
		reasons = append(reasons, rec["reason"])
	}
	if !slices.Equal(reasons, []any{"refused"}) {
		t.Errorf("the refusal reasons are %v, want [refused]", reasons)
	}
	if _, err := ag.Hello(gwtest.Ctx(ctx, "tok-b", gateway.SandboxIDKey, id), &sandboxpb.HelloRequest{}); err != nil {
		t.Errorf("tok-b's relay call after tok-a was revoked: %v", err)
	}
}

// TS-25-48 (smoke, 25-PATH-4): a static-mode server exposes the token hash as
// the principal and logs only its 8-character fingerprint. 25-REQ-2.2,
// 25-REQ-2.3.
func TestSmokeStaticPrincipal_TS25_48(t *testing.T) {
	be := gwtest.NewCounting()
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Tokens: []string{gwtest.Token}, Logger: logger})
	create(t, env.Dial(t, gwtest.Token), unrestrictedSpec)

	full := hexSHA256(gwtest.Token)
	if got := be.Principals(); len(got) != 1 || got[0] != (gwtest.Principal{Principal: full, OK: true}) {
		t.Errorf("the backend's Create principals are %v, want [(%q, true)]", got, full)
	}
	created := logs.Messages("sandbox created")
	if len(created) != 1 || created[0]["owner"] != full[:8] {
		t.Errorf("sandbox created lines %v, want one with owner %s", created, full[:8])
	}
	if strings.Contains(logs.String(), full) {
		t.Error("a log line carries the full hash")
	}
}
