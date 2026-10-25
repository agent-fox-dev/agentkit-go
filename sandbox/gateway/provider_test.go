package gateway

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

func TestReapReturnsTheRemovedIDsSortedAndTheGatewaysError(t *testing.T) {
	f := newFakeGW(t, nil)
	reaps := []*sandboxpb.ReapResponse{{Ids: []string{"c", "a", "b"}}, {Ids: []string{"b", "a"}, Error: "remove x: busy"}}
	f.with(func() { f.reaps = reaps })
	p := f.dial(t)
	if ids, err := p.Reap(context.Background()); err != nil || !slices.Equal(ids, []string{"a", "b", "c"}) {
		t.Errorf("first Reap = %v, %v; want [a b c]", ids, err)
	}
	if ids, err := p.Reap(context.Background()); !slices.Equal(ids, []string{"a", "b"}) || err == nil || !strings.Contains(err.Error(), "remove x: busy") {
		t.Errorf("second Reap = %v, %v; want [a b] and the gateway's error", ids, err)
	}
}

func TestListReturnsTheSandboxesByCreationTimeThenID(t *testing.T) {
	f := newFakeGW(t, nil)
	f.with(func() {
		f.list = &sandboxpb.ListResponse{Sandboxes: []*sandboxpb.SandboxEntry{
			{Id: "z", CreatedUnixNanos: 20, ExpiresUnixNanos: 21},
			{Id: "b", Labels: map[string]string{"k": "w"}, CreatedUnixNanos: 10, ExpiresUnixNanos: 12},
			{Id: "a", CreatedUnixNanos: 10, ExpiresUnixNanos: 13},
		}}
	})
	infos, err := f.dial(t).List(context.Background())
	var got []string
	for _, i := range infos {
		got = append(got, fmt.Sprintf("%s %v %d %d", i.ID, i.Labels, i.Created.UnixNano(), i.Expires.UnixNano()))
	}
	if want := []string{"a map[] 10 13", "b map[k:w] 10 12", "z map[] 20 21"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("List = %q, %v; want %q", got, err, want)
	}
}

// Close destroys every sandbox still open, joins the failures, closes the
// connection, and is idempotent; afterwards every call fails with ErrClosed
// and no RPC.
func TestProviderCloseDestroysTheOpenSandboxesAndEndsTheProvider(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	s1, s2 := mustCreate(t, p), mustCreate(t, p)
	closed := mustCreate(t, p)
	if err := closed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.with(func() { f.destroyErr[s2.ID()] = status.Error(codes.Unavailable, "boom") })
	if err := p.Close(); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Close = %v, want an error carrying boom", err)
	}
	f.with(func() {
		if got := slices.Sorted(slices.Values(f.destroys)); !slices.Equal(got, slices.Sorted(slices.Values([]string{closed.ID(), s1.ID(), s2.ID()}))) {
			t.Errorf("Destroy was sent for %v", f.destroys)
		}
	})
	testutil.Eventually(t, "the connection to close", f.connsClosed)
	n := f.count("")
	ctx := context.Background()
	_, e1 := p.Create(ctx, testSpec)
	_, e2 := p.Reap(ctx)
	_, e3 := p.List(ctx)
	for i, err := range []error{e1, e2, e3, p.Destroy(ctx, "x")} {
		if !errors.Is(err, ErrClosed) {
			t.Errorf("call %d after Close: %v, want ErrClosed", i, err)
		}
	}
	if err := p.Close(); err != nil || f.count("") != n {
		t.Errorf("after Close: a second Close = %v, and %d RPCs were sent", err, f.count("")-n)
	}
}

func TestARefusedTokenAfterDialMatchesErrUnauthenticated(t *testing.T) {
	f := newFakeGW(t, nil)
	p := f.dial(t)
	sb := mustCreate(t, p)
	f.with(func() { f.denyAll = true })
	ctx := context.Background()
	_, e1 := p.Create(ctx, testSpec)
	_, e2 := p.List(ctx)
	_, e3 := p.Reap(ctx)
	_, e4 := sb.Sync(ctx, sandbox.GitRepo{URL: "https://example.test/r.git"})
	for i, err := range []error{e1, e2, e3, e4, p.Destroy(ctx, "x")} {
		if !errors.Is(err, ErrUnauthenticated) || !strings.Contains(err.Error(), "http://127.0.0.1") {
			t.Errorf("call %d: %v, want ErrUnauthenticated naming the gateway", i, err)
		}
	}
}
