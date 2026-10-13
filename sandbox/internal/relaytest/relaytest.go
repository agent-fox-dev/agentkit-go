// Package relaytest is a recording relay for tests: a gRPC Agent server that
// forwards every RPC unchanged to a real agent and records each Rsync
// transfer frame by frame. Only tests import it.
package relaytest

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/agent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/rsyncx"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Frame is one recorded RsyncFrame.
type Frame struct {
	// FromHost is true for a host-to-agent frame.
	FromHost bool
	Kind     string
	DataLen  int
}

// Transfer is one recorded Rsync stream.
type Transfer struct {
	Frames []Frame
	Start  *sandboxpb.RsyncStart
	Plan   *sandboxpb.RsyncPlan
	Args   *sandboxpb.RsyncArgs
	Done   *sandboxpb.RsyncDone
	// Status is the error the agent ended the stream with, or nil for OK.
	Status error
	// OtherRPCs counts the other RPCs served while the transfer ran.
	OtherRPCs int
}

// Kinds lists the kinds of the frames sent in one direction.
func (t *Transfer) Kinds(fromHost bool) []string {
	var out []string
	for _, f := range t.Frames {
		if f.FromHost == fromHost {
			out = append(out, f.Kind)
		}
	}
	return out
}

// HostData is the total length of the host's data frames.
func (t *Transfer) HostData() int64 {
	var n int64
	for _, f := range t.Frames {
		if f.FromHost {
			n += int64(f.DataLen)
		}
	}
	return n
}

// Relay forwards RPCs to a real agent and records Rsync transfers.
type Relay struct {
	sandboxpb.UnimplementedAgentServer
	up sandboxpb.AgentClient

	mu        sync.Mutex
	transfers []*Transfer
	running   *Transfer
	// mutate, when set, may replace each forwarded frame.
	mutate func(fromHost bool, index int, f *sandboxpb.RsyncFrame) *sandboxpb.RsyncFrame
}

// Transfers returns the transfers recorded so far.
func (r *Relay) Transfers() []*Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*Transfer(nil), r.transfers...)
}

// Last is the last recorded transfer, or nil.
func (r *Relay) Last() *Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.transfers) == 0 {
		return nil
	}
	return r.transfers[len(r.transfers)-1]
}

// SetMutator installs a hook that may replace each forwarded RsyncFrame.
func (r *Relay) SetMutator(fn func(fromHost bool, index int, f *sandboxpb.RsyncFrame) *sandboxpb.RsyncFrame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mutate = fn
}

// other records an RPC other than Rsync.
func (r *Relay) other() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running != nil {
		r.running.OtherRPCs++
	}
}

func (r *Relay) Hello(ctx context.Context, in *sandboxpb.HelloRequest) (*sandboxpb.HelloResponse, error) {
	r.other()
	return r.up.Hello(ctx, in)
}

func (r *Relay) Tools(ctx context.Context, in *sandboxpb.ToolsRequest) (*sandboxpb.ToolsResponse, error) {
	r.other()
	return r.up.Tools(ctx, in)
}

func (r *Relay) Call(ctx context.Context, in *sandboxpb.CallRequest) (*sandboxpb.CallResponse, error) {
	r.other()
	return r.up.Call(ctx, in)
}

func (r *Relay) SyncTar(stream sandboxpb.Agent_SyncTarServer) error {
	r.other()
	up, err := r.up.SyncTar(stream.Context())
	if err != nil {
		return err
	}
	for {
		c, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := up.Send(c); err != nil {
			break
		}
	}
	resp, err := up.CloseAndRecv()
	if err != nil {
		return err
	}
	return stream.SendAndClose(resp)
}

func (r *Relay) ExportTar(in *sandboxpb.ExportRequest, stream sandboxpb.Agent_ExportTarServer) error {
	r.other()
	up, err := r.up.ExportTar(stream.Context(), in)
	if err != nil {
		return err
	}
	for {
		c, err := up.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(c); err != nil {
			return err
		}
	}
}

// record notes one forwarded frame and returns it, possibly replaced.
func (r *Relay) record(t *Transfer, fromHost bool, index int, f *sandboxpb.RsyncFrame) *sandboxpb.RsyncFrame {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Frames = append(t.Frames, Frame{FromHost: fromHost, Kind: rsyncx.FrameKind(f), DataLen: len(f.GetData())})
	switch {
	case f.GetStart() != nil:
		t.Start = f.GetStart()
	case f.GetPlan() != nil:
		t.Plan = f.GetPlan()
	case f.GetArgs() != nil:
		t.Args = f.GetArgs()
	case f.GetDone() != nil:
		t.Done = f.GetDone()
	}
	if r.mutate != nil {
		f = r.mutate(fromHost, index, f)
	}
	return f
}

func (r *Relay) Rsync(stream sandboxpb.Agent_RsyncServer) error {
	t := &Transfer{}
	r.mu.Lock()
	r.transfers = append(r.transfers, t)
	r.running = t
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = nil
		r.mu.Unlock()
	}()
	up, err := r.up.Rsync(stream.Context())
	if err != nil {
		return err
	}
	go func() {
		for i := 0; ; i++ {
			f, err := stream.Recv()
			if err != nil {
				_ = up.CloseSend()
				return
			}
			if err := up.Send(r.record(t, true, i, f)); err != nil {
				return
			}
		}
	}()
	for i := 0; ; i++ {
		f, err := up.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			r.mu.Lock()
			t.Status = err
			r.mu.Unlock()
			return err
		}
		if err := stream.Send(r.record(t, false, i, f)); err != nil {
			return err
		}
	}
}

// newAgent builds a real agent over a fresh workspace with the given TTL.
func newAgent(t *testing.T, ttl time.Duration) (string, *agent.Agent) {
	t.Helper()
	ws := t.TempDir()
	a, err := agent.New(agent.Config{Workdir: ws, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}, TTL: ttl})
	if err != nil {
		t.Fatal(err)
	}
	return ws, a
}

// Env is a test environment: a host sandbox attached to the relay, the
// relay, and the agent's workspace.
type Env struct {
	Sandbox   sandbox.Sandbox
	Relay     *Relay
	Workspace string
	// Served receives the agent's Serve result, for Throttled.
	Served <-chan error
}

// New starts a real agent over a fresh workspace, a relay in front of it, and
// a host sandbox attached to the relay with attach.Dial.
func New(t *testing.T) *Env {
	t.Helper()
	ws, a := newAgent(t, time.Hour)
	agentHost, agentEnd := net.Pipe()
	go func() { _ = a.Serve(agentEnd) }()
	cc, err := grpc.NewClient("passthrough:///agent",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return agentHost, nil }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxCallSendMsgSize(sandbox.MaxMessageBytes)))
	if err != nil {
		t.Fatal(err)
	}
	r := &Relay{up: sandboxpb.NewAgentClient(cc)}
	srv := grpc.NewServer(grpc.MaxRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxSendMsgSize(sandbox.MaxMessageBytes))
	sandboxpb.RegisterAgentServer(srv, r)
	hostEnd, relayEnd := net.Pipe()
	lis := &oneConn{c: make(chan net.Conn, 1), done: make(chan struct{})}
	lis.c <- relayEnd
	go func() { _ = srv.Serve(lis) }()
	sb, err := attach.Dial(context.Background(), hostEnd, attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sb.Close(context.Background())
		srv.Stop()
		_ = cc.Close()
	})
	return &Env{Sandbox: sb, Relay: r, Workspace: ws}
}

// Direct starts a real agent over a fresh workspace and a host sandbox
// attached to it directly, with no relay: the reference a relayed run is
// compared with.
func Direct(t *testing.T) *Env {
	t.Helper()
	ws, a := newAgent(t, time.Hour)
	hostEnd, agentEnd := net.Pipe()
	go func() { _ = a.Serve(agentEnd) }()
	sb, err := attach.Dial(context.Background(), hostEnd, attach.Config{Spec: sandbox.Spec{TTL: time.Hour}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return &Env{Sandbox: sb, Workspace: ws}
}

// Throttle configures Throttled.
type Throttle struct {
	// Rate is the host end's bytes per second, each way.
	Rate int
	// OnBytes, when set, is called with the running total of bytes that
	// crossed the host end, both ways.
	OnBytes func(total int64)
	// TTL is the agent's TTL; zero means an hour.
	TTL time.Duration
}

// Throttled starts a real agent over a fresh workspace and a host sandbox
// attached to it with attach.Dial over a net.Pipe pair whose host end is
// rate-limited and counted.
func Throttled(t *testing.T, th Throttle) *Env {
	t.Helper()
	ttl := th.TTL
	if ttl == 0 {
		ttl = time.Hour
	}
	ws, a := newAgent(t, ttl)
	hostEnd, agentEnd := net.Pipe()
	served := make(chan error, 1)
	go func() { served <- a.Serve(agentEnd) }()
	sb, err := attach.Dial(context.Background(), &throttled{Conn: hostEnd, th: th}, attach.Config{Spec: sandbox.Spec{TTL: ttl}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return &Env{Sandbox: sb, Workspace: ws, Served: served}
}

// throttleChunk is the most one throttled Read or Write moves at a time.
const throttleChunk = 4 << 10

// throttled is a rate-limited, counted net.Conn.
type throttled struct {
	net.Conn
	th    Throttle
	mu    sync.Mutex
	total int64
}

func (c *throttled) account(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.total += int64(n)
	total := c.total
	c.mu.Unlock()
	if c.th.OnBytes != nil {
		c.th.OnBytes(total)
	}
	if c.th.Rate > 0 {
		time.Sleep(time.Duration(n) * time.Second / time.Duration(c.th.Rate))
	}
}

func (c *throttled) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k, err := c.Conn.Write(p[:min(len(p), throttleChunk)])
		n += k
		c.account(k)
		if err != nil {
			return n, err
		}
		p = p[k:]
	}
	return n, nil
}

func (c *throttled) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p[:min(len(p), throttleChunk)])
	c.account(n)
	return n, err
}

// oneConn hands out one connection, then blocks until closed.
type oneConn struct {
	c    chan net.Conn
	done chan struct{}
	once sync.Once
}

func (l *oneConn) Accept() (net.Conn, error) {
	select {
	case c := <-l.c:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *oneConn) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *oneConn) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "relaytest" }
