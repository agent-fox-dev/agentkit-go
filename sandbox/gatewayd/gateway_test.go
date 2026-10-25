package gatewayd_test

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/gatewaywire"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// An unreadable spec is InvalidArgument; a mode outside the ceiling or a TTL above MaxTTL (zero being 1h) UNSUPPORTED.
func TestCreateChecksTheSpecBeforeTheBackend(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(bothModes)
	env, _ := start(t, gatewayd.Config{Backend: be, NetworkModes: []sandbox.NetworkMode{sandbox.NetworkNone}, MaxTTL: 30 * time.Minute})
	gw := env.RawGateway(t)
	withProvider := func(b string) *sandboxpb.SandboxSpec {
		s := specPB(sandboxpb.NetworkMode_NONE, 0)
		s.Provider = []byte(b)
		return s
	}
	deep := strings.Repeat(`{"a":`, 70) + "1" + strings.Repeat("}", 70)
	for _, spec := range []*sandboxpb.SandboxSpec{specPB(7, 0), withProvider("{"), withProvider(`{"a":1,"a":2}`), withProvider(deep)} {
		if _, err := gw.Create(authed(gwtest.Token), &sandboxpb.CreateRequest{Spec: spec}); status.Code(err) != codes.InvalidArgument {
			t.Errorf("%v: %v, want InvalidArgument", spec, err)
		}
	}
	for _, c := range []struct {
		spec  *sandboxpb.SandboxSpec
		field string
	}{
		{specPB(sandboxpb.NetworkMode_UNRESTRICTED, time.Minute), "Network.Mode"},
		{specPB(sandboxpb.NetworkMode_NONE, 31*time.Minute), "TTL"},
		{specPB(sandboxpb.NetworkMode_NONE, 0), "TTL"},
		{specPB(sandboxpb.NetworkMode_NONE, 30*time.Minute), ""},
	} {
		resp, err := gw.Create(authed(gwtest.Token), &sandboxpb.CreateRequest{Spec: c.spec})
		r := resp.GetRefusal()
		if err != nil || c.field == "" && r != nil || c.field != "" && (r.GetKind() != sandboxpb.Refusal_UNSUPPORTED || r.GetField() != c.field) {
			t.Errorf("%v: refusal %v, %v; want UNSUPPORTED %q", c.spec, r, err, c.field)
		}
	}
	if n := be.Creates(); n != 1 {
		t.Errorf("the backend's Create ran %d times, want only for the valid spec", n)
	}
}

func TestCreateTurnsABackendErrorIntoItsRefusal(t *testing.T) {
	be := gwtest.NewCounting()
	env, _ := start(t, gatewayd.Config{Backend: be})
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
		resp, err := gw.Create(authed(gwtest.Token), createReq(0))
		r := resp.GetRefusal()
		if err != nil || r.GetKind() != c.kind || r.GetField() != c.field || r.GetMessage() != c.err.Error() || c.field != "" && r.GetReason() != "r" {
			t.Errorf("%v became %v, %v; want %v field %q", c.err, r, err, c.kind, c.field)
		}
	}
	be.SetCreateErr(errors.New("engine down"))
	if _, err := gw.Create(authed(gwtest.Token), createReq(0)); status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "engine down") {
		t.Errorf("engine down became %v, want Unavailable carrying it", err)
	}
}

// A sandbox with no agent connection, or returned after its caller left, is closed and its reservation freed.
func TestCreateClosesASandboxItCannotHandOver(t *testing.T) {
	be := gwtest.NewCounting()
	env, _ := start(t, gatewayd.Config{Backend: be, MaxSandboxes: 1})
	gw := env.RawGateway(t)
	be.SetUnrelayable(true)
	if _, err := gw.Create(authed(gwtest.Token), createReq(0)); status.Code(err) != codes.Internal || !strings.Contains(err.Error(), `"local"`) || !gone(be.Last().Dir()) {
		t.Errorf("an unrelayable sandbox gave %v (closed: %v), want Internal naming the backend", err, gone(be.Last().Dir()))
	}
	be.SetCloseErr(errors.New("close boom"))
	if _, err := gw.Create(authed(gwtest.Token), createReq(0)); !strings.Contains(err.Error(), "and closing it failed: close boom") {
		t.Errorf("an unrelayable sandbox whose Close failed gave %v, want the Close error noted", err)
	}
	be.SetUnrelayable(false)
	be.SetCloseErr(nil)
	ctx, cancel := context.WithCancel(authed(gwtest.Token))
	be.SetAfterCreate(func(rpc context.Context) { cancel(); <-rpc.Done() })
	if _, err := gw.Create(ctx, createReq(0)); status.Code(err) != codes.Canceled {
		t.Errorf("a Create whose caller left: %v, want Canceled", err)
	}
	be.SetAfterCreate(nil)
	testutil.Eventually(t, "the abandoned sandbox to be closed", func() bool { return gone(be.Last().Dir()) })
	if ids := listIDs(t, gw, gwtest.Token); len(ids) != 0 {
		t.Errorf("List holds %v", ids)
	}
	rawCreate(t, gw, gwtest.Token) // the one slot is free again
}

// Create answers the backend's id and spec, now and now plus the TTL (zero being 1h); List the caller's, oldest first.
func TestCreateAnswersTheRecordedSandboxAndListRepeatsIt(t *testing.T) {
	be := gwtest.NewCounting()
	env, _ := start(t, gatewayd.Config{Backend: be})
	gw := env.RawGateway(t)
	var created []*sandboxpb.CreateResponse
	for i, ttl := range []time.Duration{0, 90 * time.Second} {
		req := createReq(ttl)
		req.Spec.Labels = map[string]string{"run": fmt.Sprint(i)}
		before := time.Now()
		r, err := gw.Create(authed(gwtest.Token), req)
		got, serr := gatewaywire.SpecFromPB(r.GetSpec())
		if err != nil || serr != nil || r.GetId() != be.Last().ID() || !reflect.DeepEqual(got, be.Last().Spec()) {
			t.Fatalf("Create = %v, %v; want the backend's id and spec %+v", r, err, be.Last().Spec())
		}
		at, expires := time.Unix(0, r.GetCreatedUnixNanos()), time.Unix(0, r.GetExpiresUnixNanos())
		if at.Before(before) || at.After(time.Now()) || expires.Sub(at) != cmp.Or(ttl, time.Hour) {
			t.Errorf("created %v, expires %v later; want now and %v later", at, expires.Sub(at), cmp.Or(ttl, time.Hour))
		}
		created = append(created, r)
	}
	rawCreate(t, gw, gwtest.TokenB)
	l, err := gw.List(authed(gwtest.Token), &sandboxpb.ListRequest{})
	if err != nil || len(l.GetSandboxes()) != len(created) {
		t.Fatalf("List = %v, %v; want the caller's %d sandboxes", l.GetSandboxes(), err, len(created))
	}
	for i, e := range l.GetSandboxes() {
		c := created[i]
		if e.GetId() != c.GetId() || !maps.Equal(e.GetLabels(), c.GetSpec().GetLabels()) ||
			e.GetCreatedUnixNanos() != c.GetCreatedUnixNanos() || e.GetExpiresUnixNanos() != c.GetExpiresUnixNanos() {
			t.Errorf("entry %d is %v, want its CreateResponse %v", i, e, c)
		}
	}
}

// Creates in progress count towards the capacity; a sandbox whose backend is no longer live stops counting.
func TestCreateIsRefusedAtCapacity(t *testing.T) {
	be := gwtest.NewCounting()
	release := make(chan struct{})
	be.SetBlock(release)
	env, _ := start(t, gatewayd.Config{Backend: be, MaxSandboxes: 2})
	gw := env.RawGateway(t)
	results := make(chan *sandboxpb.CreateResponse, 5)
	for range 5 {
		go func() { r, _ := gw.Create(authed(gwtest.Token), createReq(0)); results <- r }()
	}
	for range 3 { // the refusals come back while two creates wait in the backend
		if r := <-results; r.GetRefusal().GetKind() != sandboxpb.Refusal_CAPACITY || !strings.Contains(r.GetRefusal().GetMessage(), "maximum of 2") {
			t.Errorf("a create beyond capacity: %v, want a CAPACITY refusal", r)
		}
	}
	close(release)
	for range 2 {
		if r := <-results; r.GetId() == "" {
			t.Errorf("a create within capacity: %v", r)
		}
	}
	must(t, localtest.Cut(be.Sandboxes()[0].Sandbox))
	testutil.Eventually(t, "a create to succeed after the cut", func() bool {
		r, err := gw.Create(authed(gwtest.Token), createReq(0))
		return err == nil && r.GetId() != ""
	})
}

// An unknown, repeated or foreign id is answered OK; a failing Close removes the entry, logs a warning, answers Unavailable.
func TestDestroyClosesTheCallersSandbox(t *testing.T) {
	be := gwtest.NewCounting()
	env, logs := start(t, gatewayd.Config{Backend: be})
	gw := env.RawGateway(t)
	id, other := rawCreate(t, gw, gwtest.Token), rawCreate(t, gw, gwtest.TokenB)
	for _, target := range []string{id, id, "no-such-id", other} {
		if _, err := gw.Destroy(authed(gwtest.Token), &sandboxpb.DestroyRequest{Id: target}); err != nil {
			t.Errorf("Destroy(%s): %v", target, err)
		}
	}
	if !gone(be.ByID(id).Dir()) || be.Closes() != 1 {
		t.Errorf("after the Destroys %d sandboxes were closed, want only the caller's", be.Closes())
	}
	be.SetCloseErr(errors.New("close failed"))
	_, err := gw.Destroy(authed(gwtest.TokenB), &sandboxpb.DestroyRequest{Id: other})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), "close failed") || len(listIDs(t, gw, gwtest.TokenB)) != 0 {
		t.Errorf("a failing Close gave %v, want Unavailable carrying it and no entry left", err)
	}
	if recs := logs.Messages("sandbox destroyed"); len(recs) != 2 || recs[1]["level"] != "WARN" || recs[1]["error"] != "close failed" {
		t.Errorf("the destroy lines are %v, want the second a WARN with the Close error", recs)
	}
}

func TestReapAnswersTheBackendsIdsAndErrorText(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(bothModes)
	env, logs := start(t, gatewayd.Config{Backend: be})
	testutil.Eventually(t, "the reap at Serve start", func() bool { return be.Reaps() == 1 })
	be.SetReaps(gwtest.Reaped{IDs: []string{"x2", "x1"}, Err: errors.Join(errors.New("rm x3: busy"), errors.New("rm x4: gone"))})
	for _, want := range []*sandboxpb.ReapResponse{{Ids: []string{"x2", "x1"}, Error: "rm x3: busy\nrm x4: gone"}, {}} {
		r, err := env.RawGateway(t).Reap(authed(gwtest.Token), &sandboxpb.ReapRequest{})
		if err != nil || !slices.Equal(r.GetIds(), want.GetIds()) || r.GetError() != want.GetError() {
			t.Errorf("Reap = %v, %v; want %v", r, err, want)
		}
	}
	if recs := logs.Messages("backend reap"); len(recs) != 3 || recs[1]["level"] != "WARN" || recs[1]["removed"] != 2.0 || recs[2]["level"] != "INFO" {
		t.Errorf("the reap lines are %v, want the failed one a WARN naming 2 removed", recs)
	}
}
