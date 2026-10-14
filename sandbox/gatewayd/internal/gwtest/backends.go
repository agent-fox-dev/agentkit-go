package gwtest

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/attach"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/fakeagent"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
	"github.com/agent-fox-dev/agentkit-go/sandbox/local"
)

// Counting is a backend over another provider (local.Unisolated() by
// default) that counts Create, Reap, sandbox Close and Sync calls and
// records each sandbox it returns. Its Set methods turn it into a stub:
// other capabilities, a Create error, Reap results, a sandbox Close error.
type Counting struct {
	inner sandbox.Provider

	mu        sync.Mutex
	caps      *sandbox.Capabilities
	createErr error
	reaps     []Reaped
	closeErr  error
	block     <-chan struct{}
	unrelay   bool
	ctxWait   bool
	after     func(context.Context)
	creates   int
	reapsN    int
	closes    int
	syncs     int
	sbs       []*Sandbox
}

// Reaped is one Reap result of a stub.
type Reaped struct {
	IDs []string
	Err error
}

// NewCounting is a Counting over local.Unisolated().
func NewCounting() *Counting { return &Counting{inner: local.Unisolated()} }

// SetCaps makes Capabilities return caps. Create still creates local
// sandboxes, with an unrestricted network whatever the spec asks for.
func (c *Counting) SetCaps(caps sandbox.Capabilities) { c.with(func() { c.caps = &caps }) }

// SetCreateErr makes Create return err.
func (c *Counting) SetCreateErr(err error) { c.with(func() { c.createErr = err }) }

// SetReaps queues Reap's results; once they run out Reap returns nothing.
func (c *Counting) SetReaps(r ...Reaped) { c.with(func() { c.reaps = r }) }

// SetCloseErr makes every sandbox's Close return err after closing it.
func (c *Counting) SetCloseErr(err error) { c.with(func() { c.closeErr = err }) }

// SetBlock makes Create wait until release is closed (or its context ends)
// before creating.
func (c *Counting) SetBlock(release <-chan struct{}) { c.with(func() { c.block = release }) }

// SetUnrelayable makes Create return each sandbox inside a wrapper with no
// Unwrap, so attach's accessors cannot reach its agent.
func (c *Counting) SetUnrelayable(b bool) { c.with(func() { c.unrelay = b }) }

// SetCtxWait makes Create wait for its context to end and then create
// anyway, as a backend that returns after its caller has gone.
func (c *Counting) SetCtxWait(b bool) { c.with(func() { c.ctxWait = b }) }

// SetAfterCreate makes Create call fn with its context after creating and
// before returning.
func (c *Counting) SetAfterCreate(fn func(context.Context)) { c.with(func() { c.after = fn }) }

func (c *Counting) with(fn func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn()
}

func (c *Counting) Capabilities() sandbox.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.caps != nil {
		return *c.caps
	}
	return c.inner.Capabilities()
}

func (c *Counting) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	c.mu.Lock()
	c.creates++
	err, stub, block, unrelay, ctxWait, after := c.createErr, c.caps != nil, c.block, c.unrelay, c.ctxWait, c.after
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	createCtx := ctx
	if ctxWait {
		<-ctx.Done()
		createCtx = context.Background()
	}
	if stub {
		spec.Network = sandbox.Network{Mode: sandbox.NetworkUnrestricted}
	}
	sb, err := c.inner.Create(createCtx, spec)
	if err != nil {
		return nil, err
	}
	w := &Sandbox{Sandbox: sb, c: c}
	c.with(func() { c.sbs = append(c.sbs, w) })
	if after != nil {
		after(ctx)
	}
	if unrelay {
		return opaque{w}, nil
	}
	return w, nil
}

// opaque hides a sandbox's Unwrap.
type opaque struct{ sandbox.Sandbox }

func (c *Counting) Reap(ctx context.Context) ([]string, error) {
	c.mu.Lock()
	c.reapsN++
	if c.caps != nil || len(c.reaps) > 0 {
		var r Reaped
		if len(c.reaps) > 0 {
			r, c.reaps = c.reaps[0], c.reaps[1:]
		}
		c.mu.Unlock()
		return r.IDs, r.Err
	}
	c.mu.Unlock()
	return c.inner.Reap(ctx)
}

// Creates, Reaps, Closes and Syncs are the call counts.
func (c *Counting) Creates() int { c.mu.Lock(); defer c.mu.Unlock(); return c.creates }
func (c *Counting) Reaps() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.reapsN }
func (c *Counting) Closes() int  { c.mu.Lock(); defer c.mu.Unlock(); return c.closes }
func (c *Counting) Syncs() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.syncs }

// Sandboxes is every sandbox Create returned, in order.
func (c *Counting) Sandboxes() []*Sandbox {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*Sandbox(nil), c.sbs...)
}

// Last is the last sandbox Create returned.
func (c *Counting) Last() *Sandbox {
	sbs := c.Sandboxes()
	return sbs[len(sbs)-1]
}

// ByID is the sandbox with id, or nil.
func (c *Counting) ByID(id string) *Sandbox {
	for _, s := range c.Sandboxes() {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

// Sandbox is a backend sandbox of a Counting: the local sandbox, counted.
type Sandbox struct {
	sandbox.Sandbox
	c *Counting
}

// Unwrap is the local sandbox, for attach's accessors.
func (s *Sandbox) Unwrap() sandbox.Sandbox { return s.Sandbox }

// Dir is the local sandbox's directory: the parent of its workspace.
func (s *Sandbox) Dir() string { return filepath.Dir(s.Spec().Workdir) }

func (s *Sandbox) Close(ctx context.Context) error {
	err := s.Sandbox.Close(ctx)
	s.c.mu.Lock()
	s.c.closes++
	if s.c.closeErr != nil {
		err = s.c.closeErr
	}
	s.c.mu.Unlock()
	return err
}

func (s *Sandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	s.c.with(func() { s.c.syncs++ })
	return s.Sandbox.Sync(ctx, src)
}

// Recorder is a fake agent that also records each Call request whole, the
// arrival times of Tools and SyncTar, and answers Rsync by echoing frames.
// Its embedded fakeagent.Agent answers the rest and counts RPCs and their
// metadata.
type Recorder struct {
	*fakeagent.Agent

	mu         sync.Mutex
	calls      []*sandboxpb.CallRequest
	callResp   *sandboxpb.CallResponse
	block      bool
	entered    chan struct{}
	callCtxErr error
	toolsAt    []time.Time
	syncTarAt  []time.Time
}

// NewRecorder is a Recorder over a fresh fakeagent.Agent.
func NewRecorder() *Recorder {
	a := fakeagent.New()
	a.SetRsync(echoRsync)
	return &Recorder{Agent: a, entered: make(chan struct{}, 16)}
}

// echoRsync sends each frame back until the host half-closes.
func echoRsync(stream sandboxpb.Agent_RsyncServer) error {
	for {
		f, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := stream.Send(f); err != nil {
			return err
		}
	}
}

// SetCallResponse makes Call answer resp (unknown fields included).
func (r *Recorder) SetCallResponse(resp *sandboxpb.CallResponse) {
	r.with(func() { r.callResp = resp })
}

// SetBlock makes Call block until its context ends.
func (r *Recorder) SetBlock(b bool) { r.with(func() { r.block = b }) }

// Entered receives once each time a blocking Call starts.
func (r *Recorder) Entered() <-chan struct{} { return r.entered }

// CallCtxErr is the context error the last blocked Call saw.
func (r *Recorder) CallCtxErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.callCtxErr
}

// Calls is every Call request received.
func (r *Recorder) Calls() []*sandboxpb.CallRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*sandboxpb.CallRequest(nil), r.calls...)
}

// ToolsAt and SyncTarAt are the arrival times of Tools and SyncTar.
func (r *Recorder) ToolsAt() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.toolsAt...)
}

func (r *Recorder) SyncTarAt() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.syncTarAt...)
}

func (r *Recorder) with(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn()
}

func (r *Recorder) Tools(ctx context.Context, in *sandboxpb.ToolsRequest) (*sandboxpb.ToolsResponse, error) {
	r.with(func() { r.toolsAt = append(r.toolsAt, time.Now()) })
	return r.Agent.Tools(ctx, in)
}

func (r *Recorder) SyncTar(stream sandboxpb.Agent_SyncTarServer) error {
	r.with(func() { r.syncTarAt = append(r.syncTarAt, time.Now()) })
	return r.Agent.SyncTar(stream)
}

func (r *Recorder) Call(ctx context.Context, in *sandboxpb.CallRequest) (*sandboxpb.CallResponse, error) {
	r.mu.Lock()
	r.calls = append(r.calls, in) // gRPC decodes a fresh request per call
	resp, block := r.callResp, r.block
	r.mu.Unlock()
	if block {
		r.entered <- struct{}{}
		<-ctx.Done()
		r.with(func() { r.callCtxErr = ctx.Err() })
		return nil, ctx.Err()
	}
	if resp != nil {
		return resp, nil
	}
	return r.Agent.Call(ctx, in)
}

// serveAgent serves srv over one end of a net.Pipe and returns the other
// end and a stop function.
func serveAgent(srv sandboxpb.AgentServer) (net.Conn, func()) {
	host, agent := net.Pipe()
	s := grpc.NewServer(grpc.MaxRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxSendMsgSize(sandbox.MaxMessageBytes))
	sandboxpb.RegisterAgentServer(s, srv)
	l := &Listener{conns: make(chan net.Conn, 1), done: make(chan struct{})}
	l.conns <- agent
	go func() { _ = s.Serve(l) }()
	return host, s.Stop
}

// Agents is a backend whose sandboxes are attached to Recorder agents
// served in-process, each with a temporary directory standing in for the
// sandbox's files. It offers NetworkNone and NetworkUnrestricted.
type Agents struct {
	mu   sync.Mutex
	sbs  []*AgentSandbox
	init func(*Recorder)
}

// NewAgents is an Agents backend; init, when set, configures each new
// sandbox's agent after the backend's own handshake.
func NewAgents(init func(*Recorder)) *Agents { return &Agents{init: init} }

func (b *Agents) Capabilities() sandbox.Capabilities {
	return sandbox.Capabilities{Provider: "agents", Isolation: sandbox.IsolationNone,
		Network: []sandbox.NetworkMode{sandbox.NetworkNone, sandbox.NetworkUnrestricted}}
}

func (b *Agents) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	eff, err := sandbox.CheckSpec(b.Capabilities(), spec)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "gwtest-agents-")
	if err != nil {
		return nil, err
	}
	rec := NewRecorder()
	conn, stop := serveAgent(rec)
	destroy := func(context.Context) error { stop(); return os.RemoveAll(dir) }
	sb, err := attach.Dial(ctx, conn, attach.Config{ID: newID(), Spec: eff, Destroy: destroy, Provider: "agents"})
	if err != nil {
		return nil, err
	}
	if b.init != nil {
		b.init(rec)
	}
	w := &AgentSandbox{Sandbox: sb, Rec: rec, Dir: dir}
	b.mu.Lock()
	b.sbs = append(b.sbs, w)
	b.mu.Unlock()
	return w, nil
}

func (b *Agents) Reap(context.Context) ([]string, error) { return nil, nil }

// Sandboxes is every sandbox Create returned.
func (b *Agents) Sandboxes() []*AgentSandbox {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*AgentSandbox(nil), b.sbs...)
}

// ByID is the sandbox with id, or nil.
func (b *Agents) ByID(id string) *AgentSandbox {
	for _, s := range b.Sandboxes() {
		if s.ID() == id {
			return s
		}
	}
	return nil
}

// AgentSandbox is a sandbox of Agents: attach's sandbox, its Recorder agent
// and its directory.
type AgentSandbox struct {
	sandbox.Sandbox
	Rec *Recorder
	Dir string
}

// Unwrap is attach's sandbox.
func (s *AgentSandbox) Unwrap() sandbox.Sandbox { return s.Sandbox }

// newID is 16 random hex characters.
func newID() string {
	b := make([]byte, 8)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// Slot is a backend that wraps another (local.Unisolated() by default) and
// simulates the provider-side clone of a GitRepo Sync: it waits a seeded
// random 0-20 ms, records when each clone started and ended, and returns
// the error set with SetCloneErr. Other sources go to the wrapped sandbox.
type Slot struct {
	inner sandbox.Provider

	mu       sync.Mutex
	cloneErr error
	r        *rand.Rand
	clones   map[string][]Clone
}

// Clone is one simulated clone.
type Clone struct {
	Repo       sandbox.GitRepo
	Start, End time.Time
}

// NewSlot is a Slot over inner; nil means local.Unisolated().
func NewSlot(inner sandbox.Provider, seed uint64) *Slot {
	if inner == nil {
		inner = local.Unisolated()
	}
	return &Slot{inner: inner, r: rand.New(rand.NewPCG(seed, 7)), clones: map[string][]Clone{}}
}

// SetCloneErr makes the simulated clone fail with err; nil succeeds.
func (b *Slot) SetCloneErr(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cloneErr = err
}

// Clones is the clones run for the sandbox id.
func (b *Slot) Clones(id string) []Clone {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]Clone(nil), b.clones[id]...)
}

func (b *Slot) Capabilities() sandbox.Capabilities         { return b.inner.Capabilities() }
func (b *Slot) Reap(ctx context.Context) ([]string, error) { return b.inner.Reap(ctx) }
func (b *Slot) Create(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	sb, err := b.inner.Create(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &SlotSandbox{Sandbox: sb, b: b}, nil
}

// SlotSandbox is a sandbox of Slot.
type SlotSandbox struct {
	sandbox.Sandbox
	b *Slot
}

// Unwrap is the wrapped sandbox.
func (s *SlotSandbox) Unwrap() sandbox.Sandbox { return s.Sandbox }

func (s *SlotSandbox) Sync(ctx context.Context, src sandbox.Source) (sandbox.SyncResult, error) {
	repo, ok := src.(sandbox.GitRepo)
	if !ok {
		return s.Sandbox.Sync(ctx, src)
	}
	b := s.b
	b.mu.Lock()
	delay, err := time.Duration(b.r.IntN(21))*time.Millisecond, b.cloneErr
	b.mu.Unlock()
	start := time.Now()
	time.Sleep(delay)
	b.mu.Lock()
	b.clones[s.ID()] = append(b.clones[s.ID()], Clone{Repo: repo, Start: start, End: time.Now()})
	b.mu.Unlock()
	return sandbox.SyncResult{}, err
}
