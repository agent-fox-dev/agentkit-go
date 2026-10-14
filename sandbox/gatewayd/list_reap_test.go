package gatewayd_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// TS-19-37: List answers the caller's entries and Reap answers the
// backend's ids with the joined error text. 19-REQ-5.8.
func TestListAndReap_TS19_37(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(bothModes)
	env := gwtest.Start(t, gatewayd.Config{Backend: be})
	gw := env.RawGateway(t)
	created := map[string]*sandboxpb.CreateResponse{}
	for _, run := range []string{"1", "2"} {
		spec := specPB(sandboxpb.NetworkMode_NONE, 0)
		spec.Labels = map[string]string{"run": run}
		r, err := gw.Create(authed(), &sandboxpb.CreateRequest{Spec: spec})
		if err != nil || r.GetRefusal() != nil {
			t.Fatalf("Create: %v, %v", r.GetRefusal(), err)
		}
		created[r.GetId()] = r
	}
	l, err := gw.List(authed(), &sandboxpb.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(l.GetSandboxes()) != 2 {
		t.Fatalf("List holds %d entries, want 2", len(l.GetSandboxes()))
	}
	for _, e := range l.GetSandboxes() {
		c := created[e.GetId()]
		if c == nil || !maps.Equal(e.GetLabels(), c.GetSpec().GetLabels()) ||
			e.GetCreatedUnixNanos() != c.GetCreatedUnixNanos() || e.GetExpiresUnixNanos() != c.GetExpiresUnixNanos() {
			t.Errorf("entry %v does not match its CreateResponse %v", e, c)
		}
	}

	be.SetReaps(gwtest.Reaped{IDs: []string{"x2", "x1"}, Err: errors.Join(errors.New("rm x3: busy"), errors.New("rm x4: gone"))}, gwtest.Reaped{})
	r, err := gw.Reap(authed(), &sandboxpb.ReapRequest{})
	if err != nil {
		t.Fatal(err)
	}
	ids := slices.Sorted(slices.Values(r.GetIds()))
	if !slices.Equal(ids, []string{"x1", "x2"}) || !strings.Contains(r.GetError(), "rm x3: busy") || !strings.Contains(r.GetError(), "rm x4: gone") {
		t.Errorf("first Reap %v, want x1 x2 and both errors", r)
	}
	r, err = gw.Reap(authed(), &sandboxpb.ReapRequest{})
	if err != nil || len(r.GetIds()) != 0 || r.GetError() != "" {
		t.Errorf("second Reap %v, %v; want nothing", r, err)
	}
}
