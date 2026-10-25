package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

const testToken = "test-token-0123456789-abcdefghijklmnop"

// testSpec is a spec the fake's capabilities accept, with an image, so that
// the client's DefaultImage (none in a test binary) is not needed.
var testSpec = sandbox.Spec{Image: "example/img"}

// rpc is one RPC the fake served, with its incoming metadata.
type rpc struct {
	method string
	md     metadata.MD
}

// fakeGW is a gateway whose answers the test sets, serving the Gateway
// service and a fakeagent.Agent on one loopback listener. Its fields after
// mu are used under it, through with.
type fakeGW struct {
	sandboxpb.UnimplementedGatewayServer
	agent *fakeagent.Agent
	addr  string

	mu         sync.Mutex
	caps       *sandboxpb.CapabilitiesResponse
	capsErr    error
	list       *sandboxpb.ListResponse
	reaps      []*sandboxpb.ReapResponse
	create     *sandboxpb.CreateResponse // answered instead of a new sandbox
	createErr  error
	lastCreate *sandboxpb.CreateRequest
	syncGit    *sandboxpb.SyncGitResponse
	syncGitErr error
	lastSync   *sandboxpb.SyncGitRequest
	destroys   []string
	destroyIn  time.Duration // how far off the last Destroy's deadline was
	destroyErr map[string]error
	denyAll    bool // answer every Gateway RPC Unauthenticated
	rpcs       []rpc
	dials      []string
	open       int // client connections open
}

// newFakeGW starts a fake gateway, serving TLS when tlsCfg is set, with a
// podman backend that offers only NetworkNone, honours Image and Workdir,
// and accepts a TTL of at most an hour.
func newFakeGW(t *testing.T, tlsCfg *tls.Config) *fakeGW {
	t.Helper()
	f := &fakeGW{agent: fakeagent.New(), destroyErr: map[string]error{},
		caps: &sandboxpb.CapabilitiesResponse{ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor, Version: "v-test",
			Provider: "podman", Isolation: sandboxpb.Isolation_ISOLATION_CONTAINER, Network: []sandboxpb.NetworkMode{sandboxpb.NetworkMode_NONE},
			Image: true, Workdir: true, Notes: "podman notes.", MaxTtlNanos: int64(time.Hour), MaxSandboxes: 4}}
	opts := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(sandbox.MaxMessageBytes),
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = ln.Addr().String()
	srv := grpc.NewServer(opts...)
	sandboxpb.RegisterGatewayServer(srv, f)
	sandboxpb.RegisterAgentServer(srv, f.agent)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return f
}

// with runs fn under the fake's lock.
func (f *fakeGW) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// record notes an RPC and reports whether to deny it.
func (f *fakeGW) record(ctx context.Context, method string) (deny bool) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.with(func() {
		f.rpcs = append(f.rpcs, rpc{method, md.Copy()})
		deny = f.denyAll && strings.HasPrefix(method, "/agentkit.sandbox.v1.Gateway/")
	})
	return deny
}

// count is how many RPCs were served whose method ends in suffix.
func (f *fakeGW) count(suffix string) (n int) {
	f.with(func() {
		for _, r := range f.rpcs {
			if strings.HasSuffix(r.method, suffix) {
				n++
			}
		}
	})
	return n
}

// Dial is the client's Dialer: it records the address and connects to the
// fake whatever the address, counting the connection open until the client
// closes it.
func (f *fakeGW) Dial(ctx context.Context, addr string) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", f.addr)
	if err != nil {
		return nil, err
	}
	f.with(func() { f.dials, f.open = append(f.dials, addr), f.open+1 })
	return &countedConn{Conn: c, f: f}, nil
}

// dial is a Provider for f over http://127.0.0.1, closed at the end of the
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

// connsClosed reports whether every client connection has closed.
func (f *fakeGW) connsClosed() (done bool) {
	f.with(func() { done = f.open == 0 })
	return done
}

type countedConn struct {
	net.Conn
	f    *fakeGW
	once sync.Once
}

func (c *countedConn) Close() error {
	c.once.Do(func() { c.f.with(func() { c.f.open-- }) })
	return c.Conn.Close()
}

func (f *fakeGW) Capabilities(context.Context, *sandboxpb.CapabilitiesRequest) (caps *sandboxpb.CapabilitiesResponse, err error) {
	f.with(func() { caps, err = f.caps, f.capsErr })
	return caps, err
}

func (f *fakeGW) List(context.Context, *sandboxpb.ListRequest) (resp *sandboxpb.ListResponse, _ error) {
	f.with(func() { resp = f.list })
	return resp, nil
}

func (f *fakeGW) Reap(context.Context, *sandboxpb.ReapRequest) (resp *sandboxpb.ReapResponse, _ error) {
	f.with(func() {
		if len(f.reaps) > 0 {
			resp, f.reaps = f.reaps[0], f.reaps[1:]
		}
	})
	return resp, nil
}

func (f *fakeGW) Create(_ context.Context, req *sandboxpb.CreateRequest) (resp *sandboxpb.CreateResponse, err error) {
	f.with(func() {
		f.lastCreate, resp, err = req, f.create, f.createErr
		if resp == nil && err == nil {
			resp = &sandboxpb.CreateResponse{Id: fmt.Sprintf("sb-%d", len(f.rpcs)), Spec: req.GetSpec()}
		}
	})
	return resp, err
}

func (f *fakeGW) SyncGit(_ context.Context, req *sandboxpb.SyncGitRequest) (resp *sandboxpb.SyncGitResponse, err error) {
	f.with(func() { f.lastSync, resp, err = req, f.syncGit, f.syncGitErr })
	return resp, err
}

func (f *fakeGW) Destroy(ctx context.Context, req *sandboxpb.DestroyRequest) (_ *sandboxpb.DestroyResponse, err error) {
	d, _ := ctx.Deadline()
	f.with(func() {
		f.destroys, f.destroyIn, err = append(f.destroys, req.GetId()), time.Until(d), f.destroyErr[req.GetId()]
	})
	return &sandboxpb.DestroyResponse{}, err
}

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

// tool is the proxy tool name of sb.
func tool(t *testing.T, sb sandbox.Sandbox, name string) core.Tool {
	t.Helper()
	tl, err := sb.Tools(context.Background(), sandbox.ToolOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range tl {
		if x.Name == name {
			return x
		}
	}
	t.Fatalf("no proxy tool %s", name)
	return core.Tool{}
}

// testTLS is a pool trusting httptest's certificate for example.com, and a
// server config that presents it.
func testTLS(t *testing.T) (*x509.CertPool, *tls.Config) {
	t.Helper()
	s := httptest.NewUnstartedServer(nil)
	s.StartTLS()
	t.Cleanup(s.Close)
	pool := x509.NewCertPool()
	pool.AddCert(s.Certificate())
	return pool, &tls.Config{Certificates: s.TLS.Certificates, MinVersion: tls.VersionTLS12}
}
