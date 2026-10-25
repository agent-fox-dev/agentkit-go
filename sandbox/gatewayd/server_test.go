package gatewayd_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/internal/testutil"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gateway"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd"
	"github.com/agent-fox-dev/agentkit-go/sandbox/gatewayd/internal/gwtest"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

func shutdown(t *testing.T, s *gatewayd.Server) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.Shutdown(ctx)
}

// A failed reap is only logged; Serve serves once, returns the listener's error, and nil after Shutdown.
func TestServeReapsTheBackendUntilShutdown(t *testing.T) {
	be := gwtest.NewCounting()
	be.SetCaps(bothModes)
	be.SetReaps(gwtest.Reaped{Err: errors.New("reap failed")})
	env, logs := start(t, gatewayd.Config{Backend: be, ReapInterval: 5 * time.Millisecond})
	testutil.Eventually(t, "four reaps", func() bool { return be.Reaps() >= 4 })
	if err := env.Server.Serve(gwtest.NewListener()); err == nil || !strings.Contains(logs.String(), "reap failed") {
		t.Errorf("a second Serve = %v, want a refusal; and the failed reap logged", err)
	}
	if err := shutdown(t, env.Server); err != nil || <-env.Served != nil {
		t.Errorf("Shutdown = %v; Serve should then return nil", err)
	}
	for _, early := range []bool{false, true} {
		srv, err := gatewayd.New(gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}})
		must(t, err)
		l := gwtest.NewListener()
		if early {
			_ = shutdown(t, srv)
		} else {
			_ = l.Close()
		}
		err = srv.Serve(l)
		if _, dialErr := l.Dial(context.Background(), ""); (err == nil) == !early || dialErr == nil {
			t.Errorf("Serve on a closed listener (early Shutdown %v) = %v; the listener should be closed and only the early Shutdown return nil", early, err)
		}
		_ = shutdown(t, srv)
	}
}

func TestServeSpeaksTLSWhenConfigured(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"gw.example.test"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	must(t, err)
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	env, _ := start(t, gatewayd.Config{Backend: local.Unisolated(),
		TLS: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}})
	p, err := gateway.Dial(context.Background(), "https://gw.example.test", gateway.Options{Token: gwtest.Token, TLSConfig: &tls.Config{RootCAs: pool}, Dialer: env.Listener.Dial})
	must(t, err)
	defer p.Close()
	sb := create(t, p, unrestrictedSpec)
	// A message near sandbox.MaxMessageBytes passes the server's limits.
	if r := run(t, sb, "write_file", `{"path":"big.bin","content":"`+strings.Repeat("x", 12<<20)+`"}`); !r.OK {
		t.Fatalf("write_file of 12 MiB: %+v", r.Error)
	}
	if fi, err := os.Stat(filepath.Join(sb.Spec().Workdir, "big.bin")); err != nil || fi.Size() != 12<<20 {
		t.Errorf("the written file: %v; want 12 MiB", err)
	}
	if _, err := gateway.Dial(context.Background(), gwtest.URL, gateway.Options{Token: gwtest.Token, Dialer: env.Listener.Dial}); err == nil {
		t.Error("a plaintext Dial to the TLS gateway succeeded")
	}
}

// blockedCall is the result of a relayed Call that sandbox id's agent holds until its context ends.
func blockedCall(t *testing.T, env *gwtest.Env, be *gwtest.Agents, token, id string) <-chan error {
	rec := be.ByID(id).Rec
	rec.SetBlock(true)
	res := make(chan error, 1)
	go func() {
		_, err := sandboxpb.NewAgentClient(env.Raw(t)).Call(authed(token, gateway.SandboxIDKey, id), &sandboxpb.CallRequest{})
		res <- err
	}()
	<-rec.Entered()
	return res
}

// The relayed call in flight ends and the owner's next RPCs find the sandbox gone, whoever created it.
func TestServerDestroyEndsAHeldSandbox(t *testing.T) {
	be := gwtest.NewAgents(nil)
	env, _ := start(t, gatewayd.Config{Backend: be, Authenticate: newAuthn("tok", "tenant-1").Authenticate})
	gw := env.RawGateway(t)
	id := rawCreate(t, gw, "tok")
	inFlight := blockedCall(t, env, be, "tok", id)
	must(t, env.Server.Destroy(context.Background(), id))
	_, herr := sandboxpb.NewAgentClient(env.Raw(t)).Hello(authed("tok", gateway.SandboxIDKey, id), &sandboxpb.HelloRequest{})
	_, serr := gw.SyncGit(authed("tok"), &sandboxpb.SyncGitRequest{Id: id, Url: repo.URL})
	_, derr := gw.Destroy(authed("tok"), &sandboxpb.DestroyRequest{Id: id})
	if err := <-inFlight; status.Code(err) != codes.Unavailable || status.Code(herr) != codes.Unavailable || status.Code(serr) != codes.Unavailable ||
		derr != nil || len(listIDs(t, gw, "tok")) != 0 {
		t.Errorf("after Destroy: the call in flight %v, relay %v, SyncGit %v, Destroy %v; want Unavailable thrice, then OK and nothing listed", err, herr, serr, derr)
	}
}

// A failing Close is returned wrapped with the id; an id never, no longer or not yet held is ErrNotHeld and closes nothing.
func TestServerDestroyOfAnIDNotHeldIsErrNotHeld(t *testing.T) {
	be := gwtest.NewCounting()
	idle, err := gatewayd.New(gatewayd.Config{Backend: be, Tokens: []string{gwtest.Token}, NetworkModes: unrestricted})
	must(t, err)
	if err := idle.Destroy(context.Background(), "x"); !errors.Is(err, gatewayd.ErrNotHeld) || !strings.Contains(err.Error(), `"x"`) {
		t.Errorf("Destroy before Serve = %v, want ErrNotHeld naming the id", err)
	}
	env, _ := start(t, gatewayd.Config{Backend: be})
	gw, ctx := env.RawGateway(t), context.Background()
	be.SetCloseErr(errors.New("close failed: sentinel"))
	failing := rawCreate(t, gw, gwtest.Token)
	if err := env.Server.Destroy(ctx, failing); err == nil || err.Error() != `gatewayd: destroy "`+failing+`": close failed: sentinel` {
		t.Errorf("Destroy over a failing Close = %v, want it wrapped with the id", err)
	}
	be.SetCloseErr(nil)
	got := make(chan error, 1)
	be.SetAfterCreate(func(context.Context) { got <- env.Server.Destroy(ctx, be.Last().ID()) })
	inCreate := rawCreate(t, gw, gwtest.Token)
	for _, err := range []error{<-got, env.Server.Destroy(ctx, failing), env.Server.Destroy(ctx, "no-such-id")} {
		if !errors.Is(err, gatewayd.ErrNotHeld) || be.Closes() != 1 {
			t.Errorf("Destroy of an id not held, or not yet = %v after %d closes, want ErrNotHeld and none", err, be.Closes()-1)
		}
	}
	must(t, shutdown(t, env.Server))
	if err := env.Server.Destroy(ctx, inCreate); !errors.Is(err, gatewayd.ErrNotHeld) {
		t.Errorf("Destroy after Shutdown = %v, want ErrNotHeld", err)
	}
}

// Closing them ends the relayed calls in flight; Serve returns nil; the joined Close errors are returned once.
func TestShutdownClosesEverySandboxAndStopsServing(t *testing.T) {
	be := gwtest.NewAgents(nil)
	env, _ := start(t, gatewayd.Config{Backend: be})
	gw := env.RawGateway(t)
	inFlight := blockedCall(t, env, be, gwtest.Token, rawCreate(t, gw, gwtest.Token))
	rawCreate(t, gw, gwtest.Token)
	if err := shutdown(t, env.Server); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	if err := <-inFlight; status.Code(err) != codes.Unavailable {
		t.Errorf("the call in flight: %v, want Unavailable", err)
	}
	for _, sb := range be.Sandboxes() {
		if !gone(sb.Dir) {
			t.Errorf("sandbox %s was not closed", sb.ID())
		}
	}
	if _, err := env.Listener.Dial(context.Background(), ""); err == nil || <-env.Served != nil || shutdown(t, env.Server) != nil {
		t.Error("after Shutdown the listener accepts, Serve returned an error or a second Shutdown failed")
	}
	failing := gwtest.NewCounting()
	failing.SetCloseErr(errors.New("close failed"))
	env2, _ := start(t, gatewayd.Config{Backend: failing})
	rawCreate(t, env2.RawGateway(t), gwtest.Token)
	if err := shutdown(t, env2.Server); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Errorf("Shutdown over a failing Close = %v, want an error carrying it", err)
	}
}

// Its late sandbox is closed and not recorded, and a Shutdown whose context ends stops the server at once.
func TestShutdownDoesNotWaitForACreateInProgress(t *testing.T) {
	for _, force := range []bool{false, true} {
		be := gwtest.NewCounting()
		release := make(chan struct{})
		env, _ := start(t, gatewayd.Config{Backend: be})
		gw := env.RawGateway(t)
		held := rawCreate(t, gw, gwtest.Token)
		be.SetBlock(release)
		created := make(chan error, 1)
		go func() { _, err := gw.Create(authed(gwtest.Token), createReq(0)); created <- err }()
		testutil.Eventually(t, "the Create to reach the backend", func() bool { return be.Creates() == 2 })
		ctx, cancel := context.WithCancel(context.Background())
		if force {
			cancel()
		}
		done := make(chan error, 1)
		go func() { done <- env.Server.Shutdown(ctx) }()
		testutil.Eventually(t, "Shutdown to close the held sandbox", func() bool { return gone(be.ByID(held).Dir()) })
		if !force {
			close(release)
		}
		if err := <-created; status.Code(err) != codes.Unavailable || <-done != nil {
			t.Errorf("force %v: the Create in progress: %v, want Unavailable; Shutdown should return nil", force, err)
		}
		if !force && (len(be.Sandboxes()) != 2 || !gone(be.Last().Dir())) {
			t.Errorf("the late sandbox was left open")
		}
		cancel()
	}
}
