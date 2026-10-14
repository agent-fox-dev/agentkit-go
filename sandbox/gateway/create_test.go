package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// testSpec is a spec the fake's default capabilities accept, with an image
// so the client's DefaultImage (none in a test binary) is not needed.
var testSpec = sandbox.Spec{Image: "example/img"}

// mustCreate creates a sandbox through p, closed at the end of the test.
func mustCreate(t *testing.T, p *Provider) sandbox.Sandbox {
	t.Helper()
	sb, err := p.Create(context.Background(), testSpec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb
}

// TS-19-19: after Close, Create, Reap, List and Destroy return ErrClosed
// without sending an RPC. 19-REQ-3.6.
func TestClosedProvider_TS19_19(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	n := f.rpcCount()
	ctx := context.Background()
	_, e1 := p.Create(ctx, testSpec)
	_, e2 := p.Reap(ctx)
	_, e3 := p.List(ctx)
	e4 := p.Destroy(ctx, "x")
	for i, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, ErrClosed) {
			t.Errorf("call %d after Close: %v, want ErrClosed", i, err)
		}
	}
	if got := f.rpcCount(); got != n {
		t.Errorf("%d RPCs were sent after Close", got-n)
	}
}

// TS-19-20: an Unauthenticated answer to any Gateway RPC after Dial matches
// ErrUnauthenticated. 19-REQ-3.7.
func TestUnauthenticatedAfterDial_TS19_20(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	sb := mustCreate(t, p)
	f.set(func() { f.denyAll = true })
	ctx := context.Background()
	_, e1 := p.Create(ctx, testSpec)
	_, e2 := p.List(ctx)
	e3 := p.Destroy(ctx, "x")
	_, e4 := p.Reap(ctx)
	_, e5 := sb.Sync(ctx, sandbox.GitRepo{URL: "https://example.test/r.git"})
	for i, err := range []error{e1, e2, e3, e4, e5} {
		if !errors.Is(err, ErrUnauthenticated) {
			t.Errorf("call %d: %v, want ErrUnauthenticated", i, err)
		}
	}
}

// TS-19-21: a spec over MaxTTL, with an unencodable Provider, or refused by
// CheckSpec fails on the client before any Create RPC. 19-REQ-4.1.
func TestCreateLocalChecks_TS19_21(t *testing.T) {
	f := newFakeGW(t, nil) // podman, [NetworkNone], Image honoured, MaxTTL 1h
	p := f.dial(t)
	for _, c := range []struct {
		spec  sandbox.Spec
		field string
	}{
		{sandbox.Spec{Image: "i", TTL: 2 * time.Hour}, "TTL"},
		{sandbox.Spec{Image: "i", Provider: map[string]any{"c": make(chan int)}}, "Provider"},
		{sandbox.Spec{Image: "i", Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}}, "Network.Mode"},
	} {
		_, err := p.Create(context.Background(), c.spec)
		var se *sandbox.SpecError
		if !errors.As(err, &se) || !errors.Is(err, sandbox.ErrUnsupported) || se.Provider != "gateway/podman" || se.Field != c.field {
			t.Errorf("%s: %v (%+v), want a SpecError for gateway/podman", c.field, err, se)
		}
	}
	if n := f.n("Create"); n != 0 {
		t.Errorf("%d Create RPCs were sent, want 0", n)
	}
}

// TS-19-23: an empty Image is filled with the client's DefaultImage when the
// backend honours Image, and DefaultImage's error is returned unchanged.
// 19-REQ-4.2.
func TestDefaultImage_TS19_23(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	ctx := context.Background()
	const ref = "ghcr.io/agent-fox-dev/agentkit-sandbox:0.19.0"
	defaultImage = func() (string, error) { return ref, nil }
	t.Cleanup(func() { defaultImage = sandbox.DefaultImage })
	sb, err := p.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	_ = sb.Close(ctx)
	if got := f.lastImage(); got != ref {
		t.Errorf("the Create carried image %q, want %q", got, ref)
	}

	defaultImage = sandbox.DefaultImage
	n := f.n("Create")
	_, err = p.Create(ctx, sandbox.Spec{})
	_, want := sandbox.DefaultImage()
	if want == nil {
		t.Fatal("sandbox.DefaultImage succeeded in a test binary; the case needs it to fail")
	}
	if !errors.Is(err, sandbox.ErrNoPublishedImage) || err.Error() != want.Error() || f.n("Create") != n {
		t.Errorf("Create with no published image: %v (%d RPCs), want DefaultImage's %v and none", err, f.n("Create")-n, want)
	}

	sb, err = p.Create(ctx, sandbox.Spec{Image: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	_ = sb.Close(ctx)
	if got := f.lastImage(); got != "mine" {
		t.Errorf("Image mine was sent as %q", got)
	}

	f2 := newFakeGW(t, nil)
	f2.set(func() { f2.caps.Image = false })
	p2 := f2.dial(t)
	sb, err = p2.Create(ctx, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	_ = sb.Close(ctx)
	if got := f2.lastImage(); got != "" {
		t.Errorf("without Image support the image was sent as %q", got)
	}
}

// TS-19-24: each Create and SyncGit refusal kind maps to its sentinel
// carrying the gateway's message, and a gRPC error names the gateway.
// 19-REQ-4.3.
func TestRefusalMapping_TS19_24(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	sb := mustCreate(t, p)
	ctx := context.Background()
	const m = "the gateway says no"
	sentinels := []error{sandbox.ErrUnsupported, sandbox.ErrNoAgent, sandbox.ErrNoPublishedImage, sandbox.ErrProtocol, ErrCapacity}
	for _, c := range []struct {
		kind     sandboxpb.Refusal_Kind
		field    string
		sentinel error
	}{
		{sandboxpb.Refusal_UNSUPPORTED, "Resources.CPUs", sandbox.ErrUnsupported},
		{sandboxpb.Refusal_UNSUPPORTED, "", sandbox.ErrUnsupported},
		{sandboxpb.Refusal_NO_AGENT, "", sandbox.ErrNoAgent},
		{sandboxpb.Refusal_NO_PUBLISHED_IMAGE, "", sandbox.ErrNoPublishedImage},
		{sandboxpb.Refusal_PROTOCOL, "", sandbox.ErrProtocol},
		{sandboxpb.Refusal_CAPACITY, "", ErrCapacity},
		{sandboxpb.Refusal_REFUSED, "", nil},
	} {
		r := &sandboxpb.Refusal{Kind: c.kind, Field: c.field, Reason: "r", Message: m}
		f.set(func() { f.create.refusal, f.syncGit.refusal = r, r })
		_, ce := p.Create(ctx, testSpec)
		_, se := sb.Sync(ctx, sandbox.GitRepo{URL: "https://example.test/r.git"})
		for _, err := range []error{ce, se} {
			if err == nil || !strings.Contains(err.Error(), m) {
				t.Errorf("%v: %v does not carry the message", c.kind, err)
				continue
			}
			for _, s := range sentinels {
				if errors.Is(err, s) != (s == c.sentinel) {
					t.Errorf("%v: errors.Is(%v, %v) = %v", c.kind, err, s, errors.Is(err, s))
				}
			}
			if c.field != "" {
				var spe *sandbox.SpecError
				if !errors.As(err, &spe) || spe.Provider != "gateway/podman" || spe.Field != c.field || !strings.Contains(spe.Reason, "r") {
					t.Errorf("UNSUPPORTED %s became %v, want a SpecError for gateway/podman", c.field, err)
				}
			}
		}
	}
	f.set(func() { f.create = fakeCreate{err: status.Error(codes.Unavailable, "x")} })
	if _, err := p.Create(ctx, testSpec); err == nil || !strings.Contains(err.Error(), "http://127.0.0.1") {
		t.Errorf("a gRPC error became %v, want one naming the gateway", err)
	}
}
