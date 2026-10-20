package gatewayd_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/localtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// closeCall is one Close of a backend sandbox.
type closeCall struct {
	id  string
	ctx context.Context
}

// closeRec is a backend over Counting whose sandboxes record the context of
// each Close and, when SetFail is used, can answer it with an error.
type closeRec struct {
	*gwtest.Counting

	mu    sync.Mutex
	calls []closeCall
	fail  func(ctx context.Context, id string) error
}

func newCloseRec() *closeRec { return &closeRec{Counting: gwtest.NewCounting()} }

// SetFail makes a sandbox's Close return fn's error, once it has closed.
func (b *closeRec) SetFail(fn func(ctx context.Context, id string) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = fn
}

// Calls is every Close so far, in order.
func (b *closeRec) Calls() []closeCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]closeCall(nil), b.calls...)
}

// closesOf is the contexts of the Close calls of sandbox id.
func (b *closeRec) closesOf(id string) []context.Context {
	var out []context.Context
	for _, c := range b.Calls() {
		if c.id == id {
			out = append(out, c.ctx)
		}
	}
	return out
}

func (b *closeRec) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := b.Counting.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &recSandbox{Sandbox: sb, b: b}, nil
}

// recSandbox is a closeRec's sandbox.
type recSandbox struct {
	sandbox.Sandbox
	b *closeRec
}

// Unwrap is the wrapped sandbox, for attach's accessors.
func (s *recSandbox) Unwrap() sandbox.Sandbox { return s.Sandbox }

func (s *recSandbox) Close(ctx context.Context) error {
	s.b.mu.Lock()
	s.b.calls = append(s.b.calls, closeCall{id: s.ID(), ctx: ctx})
	fail := s.b.fail
	s.b.mu.Unlock()
	err := s.Sandbox.Close(ctx)
	if fail != nil {
		if ferr := fail(ctx, s.ID()); ferr != nil {
			return ferr
		}
	}
	return err
}

// ctxKey keys the value a test puts on the context it passes to Destroy.
type ctxKey struct{}

// destroyLogs is how many 'sandbox destroyed' records name id.
func destroyLogs(logs *gwtest.Logs, id string) int {
	n := 0
	for _, rec := range logs.Messages("sandbox destroyed") {
		if rec["id"] == id {
			n++
		}
	}
	return n
}

// shortTTL is a Spec whose sandbox the gateway removes after ttl.
func shortTTL(ttl time.Duration) sandbox.Spec {
	return sandbox.Spec{Network: sandbox.Network{Mode: sandbox.NetworkUnrestricted}, TTL: ttl}
}

// TS-25-31: the server's own Close calls use a background context with no
// principal, for a client Destroy, expiry, unavailability, Shutdown and a
// Create error path. 25-REQ-7.6, 25-REQ-8.7.
func TestServerCloseContexts_TS25_31(t *testing.T) {
	be := newCloseRec()
	a := newAuthn("tok-a", "tenant-1")
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger})
	p := env.Dial(t, "tok-a")
	ctx := context.Background()

	closed := map[string][]closeCall{}
	scenario := func(name string, trigger func()) {
		t.Helper()
		before := len(be.Calls())
		trigger()
		eventually(t, name+": the backend Close", func() bool { return len(be.Calls()) > before })
		closed[name] = be.Calls()[before:]
	}
	scenario("client Destroy", func() {
		if err := p.Destroy(ctx, create(t, p, unrestrictedSpec).ID()); err != nil {
			t.Fatal(err)
		}
	})
	scenario("expiry", func() {
		sb, err := p.Create(ctx, shortTTL(400*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "the sandbox to expire", func() bool { return destroyReason(logs, sb.ID()) == "expired" })
	})
	scenario("unavailable", func() {
		sb := create(t, p, unrestrictedSpec)
		if err := localtest.Cut(be.ByID(sb.ID()).Sandbox); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the cut sandbox to be removed", func() bool {
			_, _ = p.List(ctx)
			return destroyReason(logs, sb.ID()) == "unavailable"
		})
	})
	scenario("Create error path", func() {
		be.SetUnrelayable(true)
		if _, err := p.Create(ctx, unrestrictedSpec); err == nil {
			t.Fatal("Create over a backend sandbox with no agent connection succeeded")
		}
		be.SetUnrelayable(false)
	})
	scenario("Shutdown", func() {
		create(t, p, unrestrictedSpec)
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := env.Server.Shutdown(sctx); err != nil {
			t.Fatal(err)
		}
	})

	for name, calls := range closed {
		if len(calls) != 1 {
			t.Errorf("%s: %d backend Close calls, want 1", name, len(calls))
			continue
		}
		c := calls[0].ctx
		if pr, ok := gatewayd.PrincipalFromContext(c); pr != "" || ok {
			t.Errorf("%s: the Close context carries the principal (%q, %v)", name, pr, ok)
		}
		if _, hasDeadline := c.Deadline(); hasDeadline || c.Done() != nil || c.Err() != nil || c != context.Background() {
			t.Errorf("%s: the Close context %v is not context.Background()", name, c)
		}
	}
}

// TS-25-33: Server.Destroy removes any held sandbox through the removal path
// with reason "embedder" and the caller's ctx. 25-REQ-8.1.
func TestEmbedderDestroy_TS25_33(t *testing.T) {
	be := newCloseRec()
	a := newAuthn("tok-a", "tenant-1")
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger})
	const ttl = 500 * time.Millisecond
	sb, err := env.Dial(t, "tok-a").Create(context.Background(), shortTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	id := sb.ID()

	dctx := context.WithValue(context.Background(), ctxKey{}, "the caller's")
	if err := env.Server.Destroy(dctx, id); err != nil {
		t.Fatalf("Destroy = %v, want nil", err)
	}
	closes := be.closesOf(id)
	if len(closes) != 1 || closes[0].Value(ctxKey{}) != "the caller's" {
		t.Errorf("the backend Close calls were %v, want one with the caller's context", closes)
	}
	if got := destroyReason(logs, id); got != "embedder" {
		t.Errorf("the destroy reason is %q, want embedder", got)
	}
	time.Sleep(2 * ttl) // the expiry timer, had it not been stopped, fires in this time
	if n, c := destroyLogs(logs, id), len(be.closesOf(id)); n != 1 || c != 1 {
		t.Errorf("%d destroy lines and %d Close calls after the TTL, want 1 and 1", n, c)
	}
}

// TS-25-34: a failing Close or a cancelled ctx leaves the entry removed and
// returns the wrapped error. 25-REQ-8.2.
func TestDestroyCloseFailure_TS25_34(t *testing.T) {
	be := newCloseRec()
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	p := env.Dial(t, "tok-a")
	sbFails, sbCancelled := create(t, p, unrestrictedSpec), create(t, p, unrestrictedSpec)
	sentinel := errors.New("close failed: sentinel")
	be.SetFail(func(ctx context.Context, id string) error {
		if id == sbFails.ID() {
			return sentinel
		}
		return ctx.Err()
	})
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err1 := env.Server.Destroy(context.Background(), sbFails.ID())
	if err1 == nil || !strings.Contains(err1.Error(), sbFails.ID()) || !errors.Is(err1, sentinel) {
		t.Errorf("Destroy over a failing Close = %v, want an error naming %s that wraps the sentinel", err1, sbFails.ID())
	}
	err2 := env.Server.Destroy(cancelled, sbCancelled.ID())
	if err2 == nil || !strings.Contains(err2.Error(), sbCancelled.ID()) || !errors.Is(err2, context.Canceled) {
		t.Errorf("Destroy with a cancelled ctx = %v, want an error naming %s that wraps context.Canceled", err2, sbCancelled.ID())
	}
	for _, id := range []string{sbFails.ID(), sbCancelled.ID()} {
		if listed(t, p, id) {
			t.Errorf("sandbox %s is still listed after its Destroy failed", id)
		}
		if err := env.Server.Destroy(context.Background(), id); !errors.Is(err, gatewayd.ErrNotHeld) {
			t.Errorf("a second Destroy(%s) = %v, want ErrNotHeld", id, err)
		}
	}
}

// TS-25-35: Destroy of an id that is not held returns ErrNotHeld naming the
// id and calls no backend method. 25-REQ-8.3.
func TestDestroyNotHeld_TS25_35(t *testing.T) {
	if got := gatewayd.ErrNotHeld.Error(); got != "gatewayd: sandbox not held" {
		t.Errorf("ErrNotHeld = %q, want \"gatewayd: sandbox not held\"", got)
	}
	be := gwtest.NewCounting()
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	p := env.Dial(t, "tok-a")
	ctx := context.Background()

	clientDestroyed := create(t, p, unrestrictedSpec).ID()
	if err := p.Destroy(ctx, clientDestroyed); err != nil {
		t.Fatal(err)
	}
	expired, err := p.Create(ctx, shortTTL(300*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the sandbox to expire", func() bool { return !listed(t, p, expired.ID()) })
	embedderDestroyed := create(t, p, unrestrictedSpec).ID()
	if err := env.Server.Destroy(ctx, embedderDestroyed); err != nil {
		t.Fatal(err)
	}

	closes, reaps, syncs := be.Closes(), be.Reaps(), be.Syncs()
	for name, id := range map[string]string{
		"unknown": "no-such-sandbox", "client destroyed": clientDestroyed, "expired": expired.ID(), "embedder destroyed": embedderDestroyed,
	} {
		err := env.Server.Destroy(ctx, id)
		if !errors.Is(err, gatewayd.ErrNotHeld) || !strings.Contains(err.Error(), id) {
			t.Errorf("%s: Destroy(%s) = %v, want ErrNotHeld naming the id", name, id, err)
		}
	}
	idle, err := gatewayd.New(gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	if err != nil {
		t.Fatal(err)
	}
	if err := idle.Destroy(ctx, "x"); !errors.Is(err, gatewayd.ErrNotHeld) || !strings.Contains(err.Error(), "x") {
		t.Errorf("a server that is not serving: Destroy = %v, want ErrNotHeld naming the id", err)
	}
	if be.Closes() != closes || be.Reaps() != reaps || be.Syncs() != syncs {
		t.Errorf("the backend saw %d closes, %d reaps and %d syncs more", be.Closes()-closes, be.Reaps()-reaps, be.Syncs()-syncs)
	}

	held := create(t, p, unrestrictedSpec).ID()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := env.Server.Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	closes = be.Closes()
	if err := env.Server.Destroy(ctx, held); !errors.Is(err, gatewayd.ErrNotHeld) || !strings.Contains(err.Error(), held) {
		t.Errorf("after Shutdown: Destroy = %v, want ErrNotHeld naming the id", err)
	}
	if be.Closes() != closes {
		t.Errorf("Destroy after Shutdown closed %d sandboxes", be.Closes()-closes)
	}
}

// TS-25-36: a Destroy while Create is still in progress returns ErrNotHeld.
// 25-REQ-8.4.
func TestDestroyDuringCreate_TS25_36(t *testing.T) {
	be := gwtest.NewCounting()
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate})
	type result struct {
		err    error
		closes int
	}
	got := make(chan result, 1)
	be.SetAfterCreate(func(context.Context) {
		before := be.Closes()
		err := env.Server.Destroy(context.Background(), be.Last().ID())
		got <- result{err: err, closes: be.Closes() - before}
	})
	sb := create(t, env.Dial(t, "tok-a"), unrestrictedSpec)
	select {
	case r := <-got:
		if !errors.Is(r.err, gatewayd.ErrNotHeld) {
			t.Errorf("Destroy while Create was in progress = %v, want ErrNotHeld", r.err)
		}
		if r.closes != 0 {
			t.Errorf("Destroy closed %d backend sandboxes, want 0", r.closes)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the backend's Create hook never ran")
	}
	if !listed(t, env.Dial(t, "tok-a"), sb.ID()) {
		t.Error("the Create that was Destroyed in the middle did not complete normally")
	}
}

// TS-25-37: a concurrent removal winning the race closes the backend once and
// Destroy returns nil. 25-REQ-8.5.
func TestDestroyRace_TS25_37(t *testing.T) {
	be := newCloseRec()
	a := newAuthn("tok-a", "tenant-1")
	logs, logger := gwtest.NewLogs()
	env := gwtest.Start(t, gatewayd.Config{Backend: be, NetworkModes: unrestricted, Authenticate: a.Authenticate, Logger: logger, MaxSandboxes: 32})
	p := env.Dial(t, "tok-a")
	var ids []string
	for range 12 {
		ids = append(ids, create(t, p, unrestrictedSpec).ID())
	}
	ctx := context.Background()
	// Many callers per sandbox, released together, so that several find the
	// entry before the first removes it.
	const callers = 8
	errs := make(chan error, len(ids)*callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range ids {
		for range callers {
			wg.Add(2)
			go func() { defer wg.Done(); <-start; errs <- env.Server.Destroy(ctx, id) }()
			go func() { defer wg.Done(); <-start; _ = p.Destroy(ctx, id) }()
		}
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, gatewayd.ErrNotHeld) {
			t.Errorf("Server.Destroy returned %v, want nil or ErrNotHeld", err)
		}
	}
	for _, id := range ids {
		if n := len(be.closesOf(id)); n != 1 {
			t.Errorf("sandbox %s was closed %d times, want 1", id, n)
		}
		if n := destroyLogs(logs, id); n != 1 {
			t.Errorf("sandbox %s has %d destroy lines, want 1", id, n)
		}
	}
}

// TS-25-38: after an embedder Destroy the client sees what it sees after
// expiry. 25-REQ-8.6.
func TestAfterEmbedderDestroy_TS25_38(t *testing.T) {
	a := newAuthn("tok-a", "tenant-1")
	env := gwtest.Start(t, gatewayd.Config{Backend: gwtest.NewCounting(), NetworkModes: unrestricted, Authenticate: a.Authenticate})
	p := env.Dial(t, "tok-a")
	ctx := context.Background()
	sb := create(t, p, unrestrictedSpec)
	read := toolOf(t, sb, "read_file")

	if err := env.Server.Destroy(ctx, sb.ID()); err != nil {
		t.Fatal(err)
	}
	if r := read.Execute(ctx, []byte(`{"path":"x"}`)); r.Error != "sandbox_unavailable" {
		t.Errorf("a proxy call after Destroy: %+v, want sandbox_unavailable", r)
	}
	if listed(t, p, sb.ID()) {
		t.Error("List still has the destroyed sandbox")
	}
	if err := p.Destroy(ctx, sb.ID()); err != nil {
		t.Errorf("the client's Destroy = %v, want OK", err)
	}
	_, err := env.RawGateway(t).SyncGit(gwtest.Ctx(ctx, "tok-a"), &sandboxpb.SyncGitRequest{Id: sb.ID(), Url: repo.URL})
	if status.Code(err) != codes.Unavailable || !strings.Contains(err.Error(), sb.ID()) {
		t.Errorf("SyncGit = %v, want Unavailable naming the id", err)
	}
}
