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

func TestCreateRefusesASpecOnTheClientBeforeAnyRPC(t *testing.T) {
	f := newFakeGW(t, nil) // MaxTTL 1h, NetworkNone only
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
			t.Errorf("%s: %v, want a SpecError of gateway/podman for it", c.field, err)
		}
	}
	if n := f.count("/Create"); n != 0 {
		t.Errorf("%d Create RPCs were sent, want 0", n)
	}
}

// An empty Image becomes the client's DefaultImage when the backend honours
// Image, and DefaultImage's error is returned unchanged, with no RPC.
func TestCreateFillsAnEmptyImageWithTheClientsDefault(t *testing.T) {
	const ref = "quay.io/agentfox/agentkit-sandbox:0.19.0"
	failed := errors.New("no image for you")
	t.Cleanup(func() { defaultImage = sandbox.DefaultImage })
	for _, c := range []struct {
		image   string
		honours bool
		defErr  error
		sent    string
	}{{"", true, nil, ref}, {"mine", true, nil, "mine"}, {"", false, nil, ""}, {"", true, failed, ""}} {
		defaultImage = func() (string, error) { return ref, c.defErr }
		f := newFakeGW(t, nil)
		f.with(func() { f.caps.Image = c.honours })
		sb, err := f.dial(t).Create(context.Background(), sandbox.Spec{Image: c.image})
		var sent string
		f.with(func() { sent = f.lastCreate.GetSpec().GetImage() })
		if err != c.defErr || sent != c.sent || (err != nil && f.count("/Create") != 0) {
			t.Errorf("Image %q, backend honours images %v: %v, image sent %q; want %v and %q", c.image, c.honours, err, sent, c.defErr, c.sent)
		}
		if sb != nil {
			_ = sb.Close(context.Background())
		}
	}
}

// Each refusal kind of Create and of a GitRepo Sync becomes its sentinel,
// carrying the gateway's message, and an UNSUPPORTED field a SpecError.
func TestARefusalBecomesTheMatchingError(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	sb := mustCreate(t, p)
	ctx := context.Background()
	sentinels := []error{sandbox.ErrUnsupported, sandbox.ErrNoAgent, sandbox.ErrNoPublishedImage, sandbox.ErrProtocol, ErrCapacity}
	for _, c := range []struct {
		kind                  sandboxpb.Refusal_Kind
		field, reason, msg    string
		sentinel              error
		specReason, inMessage string
	}{
		{sandboxpb.Refusal_UNSUPPORTED, "Resources.CPUs", "r", "says no", sandbox.ErrUnsupported, "r (gateway: says no)", "says no"},
		{sandboxpb.Refusal_UNSUPPORTED, "Image", "r", "r", sandbox.ErrUnsupported, "r", "r"},
		{sandboxpb.Refusal_UNSUPPORTED, "Image", "", "says no", sandbox.ErrUnsupported, "says no", "says no"},
		{sandboxpb.Refusal_UNSUPPORTED, "", "r", "says no", sandbox.ErrUnsupported, "", "says no"},
		{sandboxpb.Refusal_NO_AGENT, "", "", "says no", sandbox.ErrNoAgent, "", "says no"},
		{sandboxpb.Refusal_NO_PUBLISHED_IMAGE, "", "", "says no", sandbox.ErrNoPublishedImage, "", "says no"},
		{sandboxpb.Refusal_PROTOCOL, "", "", "says no", sandbox.ErrProtocol, "", "says no"},
		{sandboxpb.Refusal_CAPACITY, "", "", "says no", ErrCapacity, "", "says no"},
		{sandboxpb.Refusal_REFUSED, "", "", "says no", nil, "", "says no"},
	} {
		r := &sandboxpb.Refusal{Kind: c.kind, Field: c.field, Reason: c.reason, Message: c.msg}
		f.with(func() {
			f.create, f.syncGit = &sandboxpb.CreateResponse{Refusal: r}, &sandboxpb.SyncGitResponse{Refusal: r}
		})
		_, createErr := p.Create(ctx, testSpec)
		_, syncErr := sb.Sync(ctx, sandbox.GitRepo{URL: "https://example.test/r.git"})
		for _, err := range []error{createErr, syncErr} {
			if err == nil || !strings.Contains(err.Error(), c.inMessage) {
				t.Errorf("%v: %v does not carry %q", c.kind, err, c.inMessage)
				continue
			}
			for _, s := range sentinels {
				if errors.Is(err, s) != (s == c.sentinel) {
					t.Errorf("%v: errors.Is(%v, %v) = %v", c.kind, err, s, !(s == c.sentinel))
				}
			}
			var se *sandbox.SpecError
			if errors.As(err, &se) != (c.field != "") || (se != nil && (se.Provider != "gateway/podman" || se.Field != c.field || se.Reason != c.specReason)) {
				t.Errorf("%v %s: %#v, want a SpecError exactly for a field, with reason %q", c.kind, c.field, err, c.specReason)
			}
		}
	}
	f.with(func() { f.create, f.createErr = nil, status.Error(codes.Unavailable, "down") })
	if _, err := p.Create(ctx, testSpec); err == nil || !strings.Contains(err.Error(), "http://127.0.0.1") {
		t.Errorf("a failed Create RPC gave %v, want an error naming the gateway", err)
	}
}

// A sandbox the gateway created but whose effective spec the client cannot
// read is destroyed, as is one created after Create's context ended.
func TestCreateDestroysASandboxItCannotUse(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	f.with(func() {
		f.create = &sandboxpb.CreateResponse{Id: "bad", Spec: &sandboxpb.SandboxSpec{Provider: []byte("{not json")}}
	})
	if sb, err := p.Create(context.Background(), testSpec); sb != nil || err == nil || !strings.Contains(err.Error(), "effective spec") {
		t.Errorf("Create with an unreadable effective spec = %v, %v", sb, err)
	}
	f.with(func() {
		if len(f.destroys) != 1 || f.destroys[0] != "bad" || f.destroyIn <= 25*time.Second {
			t.Errorf("Destroy was sent for %v with a deadline %v away, want [bad] and about 30 s", f.destroys, f.destroyIn)
		}
	})
}
