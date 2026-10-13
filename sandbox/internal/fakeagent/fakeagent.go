// Package fakeagent is a scriptable Agent server for tests of the host side.
// It serves over one end of a net.Pipe, counts RPCs, and records inputs,
// tar chunks and incoming metadata. Only tests import it.
package fakeagent

import (
	"context"
	"io"
	"net"
	"os"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/tooldigest"
	"github.com/agent-fox-dev/agentkit-go/tools"
)

// Agent is a fake agent. Configure it with the Set methods, before or
// between calls.
type Agent struct {
	sandboxpb.UnimplementedAgentServer

	mu        sync.Mutex
	hello     *sandboxpb.HelloResponse
	tools     *sandboxpb.ToolsResponse
	toolsErr  error
	callResp  *sandboxpb.CallResponse
	callErr   error
	callOnce  bool
	block     bool
	exportTar []byte
	exportErr error

	counts    map[string]int
	md        map[string]metadata.MD
	lastInput []byte
	chunks    []*sandboxpb.TarChunk
	syncErr   error
	callCtx   error
	conns     int
	entered   chan struct{}
	closed    chan struct{}
	srv       *grpc.Server
}

// New returns a fake agent that answers Hello with protocol 1.0, Tools with
// the host's own tool names and digests, and Call with an empty OK result.
func New() *Agent {
	return &Agent{
		hello:    &sandboxpb.HelloResponse{ProtocolMajor: 1, ProtocolMinor: 0, Version: "v0.0.0-fake", Workdir: "/fake/ws", OutlineBackend: "none"},
		tools:    HostTools(),
		callResp: &sandboxpb.CallResponse{Ok: true},
		counts:   map[string]int{},
		md:       map[string]metadata.MD{},
		entered:  make(chan struct{}, 16),
		closed:   make(chan struct{}),
	}
}

// HostTools is the Tools answer a real agent of this build gives: the names
// and digests of tools.All.
func HostTools() *sandboxpb.ToolsResponse {
	dir, err := os.MkdirTemp("", "fakeagent-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	ws, err := tools.NewWorkspace(dir)
	if err != nil {
		panic(err)
	}
	all, err := tools.All(tools.Options{Workspace: ws, Env: []string{}, Ignore: tools.NoGlobalExcludes()})
	if err != nil {
		panic(err)
	}
	resp := &sandboxpb.ToolsResponse{}
	for _, t := range all {
		resp.Tools = append(resp.Tools, &sandboxpb.ToolInfo{Name: t.Name, Digest: tooldigest.Digest(t)})
	}
	return resp
}

// Serve starts serving and returns the host's end of the connection.
func (f *Agent) Serve() net.Conn {
	host, agent := net.Pipe()
	f.srv = grpc.NewServer(grpc.MaxRecvMsgSize(16<<20), grpc.MaxSendMsgSize(16<<20))
	sandboxpb.RegisterAgentServer(f.srv, f)
	lis := &listener{conns: make(chan net.Conn, 1), done: make(chan struct{}), f: f}
	lis.conns <- &watched{Conn: agent, f: f}
	go func() { _ = f.srv.Serve(lis) }()
	return host
}

// Stop stops the server and closes its end of the connection.
func (f *Agent) Stop() { f.srv.Stop() }

// SetHello sets the Hello answer.
func (f *Agent) SetHello(h *sandboxpb.HelloResponse) { f.with(func() { f.hello = h }) }

// SetTools sets the Tools answer, or the error it returns.
func (f *Agent) SetTools(resp *sandboxpb.ToolsResponse, err error) {
	f.with(func() { f.tools, f.toolsErr = resp, err })
}

// SetCall sets the Call answer, or the error it returns; with once, the
// error is returned for one call only.
func (f *Agent) SetCall(resp *sandboxpb.CallResponse, err error, once bool) {
	f.with(func() { f.callResp, f.callErr, f.callOnce = resp, err, once })
}

// SetBlockCall makes Call block until its context is done.
func (f *Agent) SetBlockCall(b bool) { f.with(func() { f.block = b }) }

// SetExport sets the bytes ExportTar streams, or the error it returns.
func (f *Agent) SetExport(blob []byte, err error) {
	f.with(func() { f.exportTar, f.exportErr = blob, err })
}

// Count is how many times the named RPC was received.
func (f *Agent) Count(rpc string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[rpc]
}

// Metadata is the incoming metadata of the last call of the named RPC.
func (f *Agent) Metadata(rpc string) metadata.MD {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.md[rpc]
}

// LastInput is the input bytes of the last Call.
func (f *Agent) LastInput() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastInput
}

// Chunks are the chunks of the last SyncTar stream.
func (f *Agent) Chunks() []*sandboxpb.TarChunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chunks
}

// SyncErr is the receive error the last failed SyncTar stream ended with,
// such as the host cancelling it.
func (f *Agent) SyncErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncErr
}

// CallCtxErr is the context error the last blocked Call saw.
func (f *Agent) CallCtxErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.callCtx
}

// Conns is how many connections the server accepted.
func (f *Agent) Conns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// Entered receives once each time a blocking Call starts.
func (f *Agent) Entered() <-chan struct{} { return f.entered }

// Closed is closed when the host closes the connection.
func (f *Agent) Closed() <-chan struct{} { return f.closed }

func (f *Agent) with(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

// record counts an RPC and keeps its metadata.
func (f *Agent) record(ctx context.Context, rpc string) {
	md, _ := metadata.FromIncomingContext(ctx)
	f.with(func() { f.counts[rpc]++; f.md[rpc] = md })
}

func (f *Agent) Hello(ctx context.Context, _ *sandboxpb.HelloRequest) (*sandboxpb.HelloResponse, error) {
	f.record(ctx, "Hello")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hello, nil
}

func (f *Agent) Tools(ctx context.Context, _ *sandboxpb.ToolsRequest) (*sandboxpb.ToolsResponse, error) {
	f.record(ctx, "Tools")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools, f.toolsErr
}

func (f *Agent) Call(ctx context.Context, req *sandboxpb.CallRequest) (*sandboxpb.CallResponse, error) {
	f.record(ctx, "Call")
	f.mu.Lock()
	f.lastInput = req.Input
	resp, err, block := f.callResp, f.callErr, f.block
	if f.callOnce {
		f.callErr = nil
	}
	f.mu.Unlock()
	if block {
		f.entered <- struct{}{}
		<-ctx.Done()
		f.with(func() { f.callCtx = ctx.Err() })
		return nil, ctx.Err()
	}
	return resp, err
}

func (f *Agent) SyncTar(stream sandboxpb.Agent_SyncTarServer) error {
	f.record(stream.Context(), "SyncTar")
	var chunks []*sandboxpb.TarChunk
	for {
		c, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			f.with(func() { f.chunks, f.syncErr = chunks, err })
			return err
		}
		chunks = append(chunks, c)
	}
	f.with(func() { f.chunks = chunks })
	return stream.SendAndClose(&sandboxpb.SyncResponse{})
}

func (f *Agent) ExportTar(_ *sandboxpb.ExportRequest, stream sandboxpb.Agent_ExportTarServer) error {
	f.record(stream.Context(), "ExportTar")
	f.mu.Lock()
	blob, err := f.exportTar, f.exportErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	for len(blob) > 0 {
		n := min(len(blob), 1<<20)
		if err := stream.Send(&sandboxpb.TarChunk{Body: &sandboxpb.TarChunk_Data{Data: blob[:n]}}); err != nil {
			return err
		}
		blob = blob[n:]
	}
	return nil
}

// listener hands out one connection, then blocks until closed.
type listener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	f     *Agent
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		l.f.with(func() { l.f.conns++ })
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *listener) Addr() net.Addr { return pipeAddr{} }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "fakeagent" }

// watched closes Agent.closed when a read fails: the host has gone.
type watched struct {
	net.Conn
	f    *Agent
	once sync.Once
}

func (w *watched) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	if err != nil {
		w.once.Do(func() { close(w.f.closed) })
	}
	return n, err
}
