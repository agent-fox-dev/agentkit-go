package gateway

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// testToken is a well-formed bearer token.
const testToken = "test-token-0123456789-abcdefghijklmnop"

// rpcRecord is one RPC the fake gateway served.
type rpcRecord struct {
	method string
	md     metadata.MD
}

// fakeGW is a fake gateway built from the generated stubs: a Gateway server
// whose answers the test sets, and a fakeagent.Agent on the same server, over
// an in-memory listener. Interceptors record every RPC's method and incoming
// metadata.
type fakeGW struct {
	sandboxpb.UnimplementedGatewayServer
	agent *fakeagent.Agent

	mu          sync.Mutex
	caps        *sandboxpb.CapabilitiesResponse
	capsErr     error
	capsBlock   bool
	list        *sandboxpb.ListResponse
	reaps       []*sandboxpb.ReapResponse
	create      fakeCreate
	lastCreate  *sandboxpb.CreateRequest
	syncGit     fakeCreate
	lastSyncGit *sandboxpb.SyncGitRequest
	seq         int
	live        map[string]bool
	destroys    []destroyed
	destroyErr  map[string]error
	denyAll     bool
	tlsVersion  uint16
	records     []rpcRecord
	counts      map[string]int
	open        int
	dials       []string

	srv   *grpc.Server
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

// defaultCaps is the fake's Capabilities answer unless a test sets another:
// a podman backend offering only NetworkNone, honouring Image, with a 1h
// maximum TTL.
func defaultCaps() *sandboxpb.CapabilitiesResponse {
	return &sandboxpb.CapabilitiesResponse{ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor, Version: "v-test",
		Provider: "podman", Isolation: sandboxpb.Isolation_ISOLATION_CONTAINER, Network: []sandboxpb.NetworkMode{sandboxpb.NetworkMode_NONE},
		Image: true, Workdir: true, Notes: "podman notes.", MaxTtlNanos: int64(3600e9), MaxSandboxes: 4}
}

// newFakeGW starts a plaintext fake gateway; tlsCfg, when set, serves TLS.
func newFakeGW(t *testing.T, tlsCfg *tls.Config) *fakeGW {
	t.Helper()
	f := &fakeGW{agent: fakeagent.New(), caps: defaultCaps(), counts: map[string]int{}, live: map[string]bool{},
		destroyErr: map[string]error{}, conns: make(chan net.Conn), done: make(chan struct{})}
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxSendMsgSize(sandbox.MaxMessageBytes),
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			if f.record(ctx, info.FullMethod) {
				return nil, status.Error(codes.Unauthenticated, "denied")
			}
			return h(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			f.record(ss.Context(), info.FullMethod)
			return h(srv, ss)
		}),
	}
	if tlsCfg != nil {
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	f.srv = grpc.NewServer(opts...)
	sandboxpb.RegisterGatewayServer(f.srv, f)
	sandboxpb.RegisterAgentServer(f.srv, f.agent)
	go func() { _ = f.srv.Serve(f) }()
	t.Cleanup(func() { f.srv.Stop(); _ = f.Close() })
	return f
}

// record notes an RPC and the TLS version it arrived over, and reports
// whether the fake denies it: in deny-all mode, every Gateway RPC.
func (f *fakeGW) record(ctx context.Context, method string) bool {
	md, _ := metadata.FromIncomingContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, rpcRecord{method, md.Copy()})
	if p, ok := peer.FromContext(ctx); ok {
		if ti, ok := p.AuthInfo.(credentials.TLSInfo); ok {
			f.tlsVersion = ti.State.Version
		}
	}
	return f.denyAll && strings.HasPrefix(method, "/agentkit.sandbox.v1.Gateway/")
}

// count notes one call of a Gateway RPC.
func (f *fakeGW) count(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[name]++
}

// openConns is how many client connections are open.
func (f *fakeGW) openConns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

// dialled is the addresses the recording Dialer was given.
func (f *fakeGW) dialled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dials...)
}

// n is how many times the named Gateway RPC was served.
func (f *fakeGW) n(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[name]
}

// rpcCount is how many RPCs of either service were served.
func (f *fakeGW) rpcCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.records)
}

// recorded is a copy of the records so far.
func (f *fakeGW) recorded() []rpcRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rpcRecord(nil), f.records...)
}

// set runs fn under the fake's lock.
func (f *fakeGW) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// Dial is the recording Dialer: it hands one end of a net.Pipe to the
// server and returns the other.
func (f *fakeGW) Dial(ctx context.Context, addr string) (net.Conn, error) {
	client, server := net.Pipe()
	f.set(func() { f.dials = append(f.dials, addr) })
	select {
	case f.conns <- &countedConn{Conn: newAsyncConn(server), f: f}:
		return client, nil
	case <-f.done:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeGW) Accept() (net.Conn, error) {
	select {
	case c := <-f.conns:
		f.set(func() { f.open++ })
		return c, nil
	case <-f.done:
		return nil, net.ErrClosed
	}
}

func (f *fakeGW) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

func (f *fakeGW) Addr() net.Addr { return memAddr{} }

type memAddr struct{}

func (memAddr) Network() string { return "mem" }
func (memAddr) String() string  { return "fakegw" }

// asyncConn is a net.Conn whose writes never wait for the reader. A
// net.Pipe write blocks until the other end reads it all, which deadlocks a
// TLS handshake the client abandons mid-flight: the server waits to finish
// writing its flight while the client waits to write its alert. TCP's
// buffers break that cycle; this does the same.
type asyncConn struct {
	net.Conn
	writes chan []byte
	done   chan struct{}
	once   sync.Once
}

func newAsyncConn(c net.Conn) *asyncConn {
	a := &asyncConn{Conn: c, writes: make(chan []byte, 1024), done: make(chan struct{})}
	go func() {
		for {
			select {
			case b := <-a.writes:
				if _, err := a.Conn.Write(b); err != nil {
					_ = a.Close()
					return
				}
			case <-a.done:
				return
			}
		}
	}()
	return a
}

func (a *asyncConn) Write(p []byte) (int, error) {
	select {
	case a.writes <- slices.Clone(p):
		return len(p), nil
	case <-a.done:
		return 0, net.ErrClosed
	}
}

func (a *asyncConn) Close() error {
	a.once.Do(func() { close(a.done) })
	return a.Conn.Close()
}

// countedConn is a server-side connection that counts itself closed once.
type countedConn struct {
	net.Conn
	f    *fakeGW
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.f.set(func() { c.f.open-- }) })
	return c.Conn.Close()
}

func (f *fakeGW) Capabilities(ctx context.Context, _ *sandboxpb.CapabilitiesRequest) (*sandboxpb.CapabilitiesResponse, error) {
	f.count("Capabilities")
	f.mu.Lock()
	caps, err, block := f.caps, f.capsErr, f.capsBlock
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return caps, err
}

func (f *fakeGW) List(context.Context, *sandboxpb.ListRequest) (*sandboxpb.ListResponse, error) {
	f.count("List")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.list == nil {
		return &sandboxpb.ListResponse{}, nil
	}
	return f.list, nil
}

func (f *fakeGW) Reap(context.Context, *sandboxpb.ReapRequest) (*sandboxpb.ReapResponse, error) {
	f.count("Reap")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reaps) == 0 {
		return &sandboxpb.ReapResponse{}, nil
	}
	r := f.reaps[0]
	f.reaps = f.reaps[1:]
	return r, nil
}

// dial is Dial against f over http://127.0.0.1, closed at the end of the
// test.
func (f *fakeGW) dial(t *testing.T) *Provider {
	t.Helper()
	p, err := Dial(context.Background(), "http://127.0.0.1", Options{Token: testToken, Dialer: f.Dial})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// testPKI is a test CA and a leaf certificate it signed for gw.example.test.
type testPKI struct {
	pool   *x509.CertPool
	server *tls.Config
}

// newTestPKI generates a fresh CA and a leaf for gw.example.test.
func newTestPKI(t *testing.T) testPKI {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agentkit test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "gw.example.test"},
		DNSNames: []string{"gw.example.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return testPKI{pool: pool, server: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}},
		MinVersion: tls.VersionTLS12}}
}

// fakeCreate is how the fake answers Create.
type fakeCreate struct {
	refusal *sandboxpb.Refusal
	err     error
}

// destroyed is one Destroy the fake received.
type destroyed struct {
	id         string
	deadlineIn time.Duration
}

func (f *fakeGW) Create(_ context.Context, req *sandboxpb.CreateRequest) (*sandboxpb.CreateResponse, error) {
	f.count("Create")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCreate = req
	if f.create.err != nil || f.create.refusal != nil {
		return &sandboxpb.CreateResponse{Refusal: f.create.refusal}, f.create.err
	}
	f.seq++
	id := fmt.Sprintf("sb-%d", f.seq)
	f.live[id] = true
	now := time.Now()
	return &sandboxpb.CreateResponse{Id: id, Spec: req.GetSpec(), CreatedUnixNanos: now.UnixNano(),
		ExpiresUnixNanos: now.Add(time.Hour).UnixNano()}, nil
}

func (f *fakeGW) SyncGit(_ context.Context, req *sandboxpb.SyncGitRequest) (*sandboxpb.SyncGitResponse, error) {
	f.count("SyncGit")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSyncGit = req
	return &sandboxpb.SyncGitResponse{Refusal: f.syncGit.refusal}, f.syncGit.err
}

func (f *fakeGW) Destroy(ctx context.Context, req *sandboxpb.DestroyRequest) (*sandboxpb.DestroyResponse, error) {
	f.count("Destroy")
	var in time.Duration
	if d, ok := ctx.Deadline(); ok {
		in = time.Until(d)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroys = append(f.destroys, destroyed{req.GetId(), in})
	if err := f.destroyErr[req.GetId()]; err != nil {
		return nil, err
	}
	delete(f.live, req.GetId())
	return &sandboxpb.DestroyResponse{}, nil
}

// lastImage is the image of the last Create the fake received.
func (f *fakeGW) lastImage() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCreate.GetSpec().GetImage()
}

// lastSyncGitRequest is the last SyncGit the fake received.
func (f *fakeGW) lastSyncGitRequest() *sandboxpb.SyncGitRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSyncGit
}

// has reports whether the fake still holds id.
func (f *fakeGW) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.live[id]
}

// lastDestroy is the last Destroy the fake received.
func (f *fakeGW) lastDestroy() destroyed {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.destroys) == 0 {
		return destroyed{}
	}
	return f.destroys[len(f.destroys)-1]
}

// destroyedIDs is the ids of every Destroy received.
func (f *fakeGW) destroyedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, d := range f.destroys {
		ids = append(ids, d.id)
	}
	return ids
}
