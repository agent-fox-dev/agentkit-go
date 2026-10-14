package gatewayd_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gatewaywire"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// unrestricted is the ceiling the tests over local use: local offers no
// other mode.
var unrestricted = []sandbox.NetworkMode{sandbox.NetworkUnrestricted}

// bothModes is a stub backend's capabilities offering none and
// unrestricted.
var bothModes = sandbox.Capabilities{Provider: "stub", Network: []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}}

// authed is a background context carrying gwtest.Token.
func authed() context.Context { return gwtest.Ctx(context.Background(), gwtest.Token) }

// specPB is a wire spec with mode and ttl.
func specPB(mode sandboxpb.NetworkMode, ttl time.Duration) *sandboxpb.SandboxSpec {
	return &sandboxpb.SandboxSpec{Network: &sandboxpb.Network{Mode: mode}, TtlNanos: int64(ttl)}
}

// TS-19-38: a Create with an unknown enum value or Provider JSON that
// wire.Guard rejects is answered InvalidArgument without calling the
// backend. 19-REQ-6.1.
func TestCreateInvalid_TS19_38(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	gw := env.RawGateway(t)
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	for name, spec := range map[string]*sandboxpb.SandboxSpec{
		"mode 7":    specPB(7, 0),
		"syntax":    {Network: &sandboxpb.Network{Mode: sandboxpb.NetworkMode_UNRESTRICTED}, Provider: []byte("{")},
		"duplicate": {Network: &sandboxpb.Network{Mode: sandboxpb.NetworkMode_UNRESTRICTED}, Provider: []byte(`{"a":1,"a":2}`)},
		"too deep":  {Network: &sandboxpb.Network{Mode: sandboxpb.NetworkMode_UNRESTRICTED}, Provider: []byte(deep)},
	} {
		if _, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: spec}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", name, err)
		}
	}
	if n := be.Creates(); n != 0 {
		t.Errorf("the backend's Create ran %d times, want 0", n)
	}
}

// TS-19-39: a network mode outside the ceiling, or a TTL above MaxTTL
// (counting zero as 1h), is refused as UNSUPPORTED before the backend.
// 19-REQ-6.2.
func TestCreateCeilingAndTTL_TS19_39(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(bothModes)
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone}, MaxTTL: 30 * time.Minute})
	gw := env.RawGateway(t)
	for _, c := range []struct {
		spec  *sandboxpb.SandboxSpec
		field string
	}{
		{specPB(sandboxpb.NetworkMode_UNRESTRICTED, 10*time.Minute), "Network.Mode"},
		{specPB(sandboxpb.NetworkMode_NONE, 31*time.Minute), "TTL"},
		{specPB(sandboxpb.NetworkMode_NONE, 0), "TTL"},
	} {
		resp, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: c.spec})
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.GetRefusal(); r.GetKind() != sandboxpb.Refusal_UNSUPPORTED || r.GetField() != c.field || r == nil {
			t.Errorf("refusal %v, want UNSUPPORTED %s", r, c.field)
		}
	}
	resp, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_NONE, 30*time.Minute)})
	if err != nil || resp.GetRefusal() != nil {
		t.Fatalf("a none/30m Create: %v, %v", resp.GetRefusal(), err)
	}
	if n := be.Creates(); n != 1 {
		t.Errorf("the backend's Create ran %d times, want 1", n)
	}
}

// TS-19-41: backend Create errors map to UNSUPPORTED, NO_AGENT,
// NO_PUBLISHED_IMAGE, PROTOCOL or status Unavailable. 19-REQ-6.4.
func TestCreateBackendErrors_TS19_41(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	gw := env.RawGateway(t)
	for _, c := range []struct {
		err   error
		kind  sandboxpb.Refusal_Kind
		field string
	}{
		{&sandbox.SpecError{Provider: "stub", Field: "Resources.CPUs", Reason: "r"}, sandboxpb.Refusal_UNSUPPORTED, "Resources.CPUs"},
		{fmt.Errorf("%w: gpu", sandbox.ErrUnsupported), sandboxpb.Refusal_UNSUPPORTED, ""},
		{sandbox.ErrNoAgent, sandboxpb.Refusal_NO_AGENT, ""},
		{sandbox.ErrNoPublishedImage, sandboxpb.Refusal_NO_PUBLISHED_IMAGE, ""},
		{sandbox.ErrProtocol, sandboxpb.Refusal_PROTOCOL, ""},
	} {
		be.SetCreateErr(c.err)
		resp, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)})
		if err != nil {
			t.Fatalf("%v: %v", c.err, err)
		}
		r := resp.GetRefusal()
		if r == nil || r.GetKind() != c.kind || r.GetField() != c.field {
			t.Errorf("%v became %v, want %v field %q", c.err, r, c.kind, c.field)
		}
		if c.field != "" && r.GetReason() != "r" {
			t.Errorf("the SpecError's reason became %q", r.GetReason())
		}
	}
	be.SetCreateErr(errors.New("engine down"))
	_, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: specPB(sandboxpb.NetworkMode_UNRESTRICTED, 0)})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "engine down") {
		t.Errorf("engine down became %v, want Unavailable carrying it", err)
	}
}

// TS-19-43: a recorded Create answers the backend's id and spec, the
// creation time, and an expiry of created plus the effective TTL.
// 19-REQ-6.6.
func TestCreateRecorded_TS19_43(t *testing.T) {
	be := gwtest.NewCounting()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted})
	gw := env.RawGateway(t)
	for _, ttl := range []time.Duration{0, 90 * time.Second} {
		spec := specPB(sandboxpb.NetworkMode_UNRESTRICTED, ttl)
		spec.Labels = map[string]string{"k": "v"}
		before := time.Now()
		r, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: spec})
		after := time.Now()
		if err != nil || r.GetRefusal() != nil {
			t.Fatalf("Create: %v, %v", r.GetRefusal(), err)
		}
		bsb := be.Last()
		if r.GetId() != bsb.ID() {
			t.Errorf("id %q, want the backend's %q", r.GetId(), bsb.ID())
		}
		got, err := gatewaywire.SpecFromPB(r.GetSpec())
		if err != nil || !reflect.DeepEqual(got, bsb.Spec()) {
			t.Errorf("spec %+v (%v), want the backend's %+v", got, err, bsb.Spec())
		}
		created, expires := time.Unix(0, r.GetCreatedUnixNanos()), time.Unix(0, r.GetExpiresUnixNanos())
		if created.Before(before) || created.After(after) {
			t.Errorf("created %v is not between %v and %v", created, before, after)
		}
		want := ttl
		if want == 0 {
			want = time.Hour
		}
		if d := expires.Sub(created); d != want {
			t.Errorf("expires - created = %v, want %v", d, want)
		}
		l, err := gw.List(authed(), &sandboxpb.ListRequest{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range l.GetSandboxes() {
			if e.GetId() == r.GetId() {
				found = e.GetCreatedUnixNanos() == r.GetCreatedUnixNanos() && e.GetExpiresUnixNanos() == r.GetExpiresUnixNanos()
			}
		}
		if !found {
			t.Errorf("List does not report %s with its times", r.GetId())
		}
	}
}
