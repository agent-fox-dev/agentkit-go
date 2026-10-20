// Package attach implements sandbox.Sandbox once, for every provider. It is
// written against a gRPC client connection plus per-call outgoing metadata,
// so a provider holding a net.Conn to an agent (Dial) and a later gateway
// client (New over its own connection) share it.
package attach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/sandbox"
	"github.com/agent-fox-dev/agentkit-go/sandbox/internal/sandboxpb"
)

// Config is what a provider hands to Dial or New.
type Config struct {
	ID string
	// Spec is the spec CheckSpec returned.
	Spec sandbox.Spec
	// Destroy is the provider's destroy step. Nil means none.
	Destroy func(context.Context) error
	// Metadata is sent with every RPC.
	Metadata metadata.MD
	// Provider is the provider's name, for the RequireLandlock refusal.
	Provider string
}

// Dial opens a gRPC client over conn — insecure, because the channel is a
// private pipe, and with a dialer that hands out conn once and never
// redials — and attaches with New.
func Dial(ctx context.Context, conn net.Conn, cfg Config) (sandbox.Sandbox, error) {
	cc, err := grpc.NewClient("passthrough:///agentkit-sandbox",
		grpc.WithContextDialer(oneShot(conn)),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(sandbox.MaxMessageBytes), grpc.MaxCallSendMsgSize(sandbox.MaxMessageBytes)),
		grpc.WithIdleTimeout(0))
	if err != nil {
		_ = conn.Close()
		return nil, errors.Join(err, destroy(cfg))
	}
	return New(ctx, cc, cc, cfg)
}

// oneShot returns a dialer that returns conn on its first call and an error
// on every later one.
func oneShot(conn net.Conn) func(context.Context, string) (net.Conn, error) {
	var used atomic.Bool
	return func(context.Context, string) (net.Conn, error) {
		if used.Swap(true) {
			return nil, errors.New("sandbox: the agent connection is gone and is never redialled")
		}
		return conn, nil
	}
}

// New attaches to the agent behind cc: it calls Hello under ctx, refuses a
// different protocol major with sandbox.ErrProtocol, refuses a spec with
// RequireLandlock whose agent's Landlock report does not satisfy its network
// mode with a *sandbox.SpecError, and starts the TTL timer. closer closes the client. On any failure the client is closed and
// the destroy step runs. ctx bounds creation only.
func New(ctx context.Context, cc grpc.ClientConnInterface, closer io.Closer, cfg Config) (sandbox.Sandbox, error) {
	s := &sb{cc: cc, client: sandboxpb.NewAgentClient(cc), closer: closer, cfg: cfg}
	h, err := s.client.Hello(s.outgoing(ctx), &sandboxpb.HelloRequest{})
	if err == nil && h.ProtocolMajor != sandbox.ProtocolMajor {
		err = fmt.Errorf("%w: agent protocol %d.%d, host protocol %d.%d", sandbox.ErrProtocol,
			h.ProtocolMajor, h.ProtocolMinor, sandbox.ProtocolMajor, sandbox.ProtocolMinor)
	} else if err != nil {
		err = fmt.Errorf("sandbox: agent handshake: %w", err)
	}
	if err != nil {
		return nil, errors.Join(err, s.teardown())
	}
	s.agent = sandbox.AgentInfo{Version: h.Version, ProtocolMajor: h.ProtocolMajor, ProtocolMinor: h.ProtocolMinor,
		Workdir: h.Workdir, OutlineBackend: h.OutlineBackend, Git: h.Git, Landlock: landlockInfo(h)}
	if cfg.Spec.RequireLandlock && !s.agent.Landlock.Satisfies(cfg.Spec.Network.Mode) {
		l := s.agent.Landlock
		err := &sandbox.SpecError{Provider: cfg.Provider, Field: "RequireLandlock",
			Reason: fmt.Sprintf("agent Landlock does not satisfy network mode %s: ABI %d, kernel ABI %d, TCP %t, note: %s",
				cfg.Spec.Network.Mode, l.ABI, l.KernelABI, l.TCP, l.Note)}
		return nil, errors.Join(err, s.teardown())
	}
	s.spec = cfg.Spec
	s.spec.Workdir = h.Workdir
	if ttl := s.spec.TTL; ttl > 0 {
		s.timer.Store(time.AfterFunc(ttl, func() {
			s.fail(fmt.Sprintf("TTL %v expired", ttl))
			_ = s.teardown()
		}))
	}
	if c, ok := cc.(*grpc.ClientConn); ok {
		go s.watch(c)
	}
	return s, nil
}

// landlockInfo is the Landlock report of h as a LandlockInfo, field for
// field. An agent older than protocol 1.2, or one that sends no report, gets
// ABI 0 and a note naming its protocol version.
func landlockInfo(h *sandboxpb.HelloResponse) sandbox.LandlockInfo {
	r := h.GetLandlock()
	if r == nil || (h.ProtocolMajor == 1 && h.ProtocolMinor <= 1) {
		return sandbox.LandlockInfo{Note: fmt.Sprintf("agent protocol %d.%d does not report Landlock", h.ProtocolMajor, h.ProtocolMinor)}
	}
	nilIfEmpty := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return slices.Clone(s)
	}
	return sandbox.LandlockInfo{ABI: int(r.Abi), KernelABI: int(r.KernelAbi), TCP: r.Tcp, Note: r.Note,
		Writable: nilIfEmpty(r.Writable), ReadOnly: nilIfEmpty(r.ReadOnly)}
}

// sb is the one Sandbox implementation.
type sb struct {
	cc     grpc.ClientConnInterface
	client sandboxpb.AgentClient
	closer io.Closer
	cfg    Config
	spec   sandbox.Spec
	agent  sandbox.AgentInfo
	// timer is the TTL timer; atomic because its own callback reads it.
	timer atomic.Pointer[time.Timer]

	state
	down    sync.Once
	downErr error

	toolsMu  sync.Mutex
	tools    []core.Tool
	toolOpts sandbox.ToolOptions
}

// ClientConn is the gRPC connection of the sandbox this package attached
// that s is, or that s's Unwrap chain reaches, or nil. Tests open raw
// streams on it, and a gateway relays agent RPCs over it.
func ClientConn(s sandbox.Sandbox) grpc.ClientConnInterface {
	if x := unwrapAttached(s); x != nil {
		return x.cc
	}
	return nil
}

// Outgoing is ctx with the outgoing metadata of the sandbox that s is, or that
// s's Unwrap chain reaches: the Config.Metadata it sends with every RPC of its
// own, in place of any outgoing metadata ctx has. It is ctx itself when there
// is no such sandbox or it sends no metadata, which is every sandbox of a
// container or local provider. A gateway relays an Agent RPC to the agent of
// a sandbox over ClientConn(s), and sends this with it: for a sandbox that
// lives on another gateway it names the sandbox there.
func Outgoing(ctx context.Context, s sandbox.Sandbox) context.Context {
	if x := unwrapAttached(s); x != nil {
		return x.outgoing(ctx)
	}
	return ctx
}

// maxUnwrap bounds an Unwrap chain, so a cyclic one cannot loop.
const maxUnwrap = 32

// unwrapAttached is the sandbox this package attached that s is, or that
// following `Unwrap() sandbox.Sandbox` from s reaches, or nil when the chain
// ends, holds a nil, or is longer than maxUnwrap.
func unwrapAttached(s sandbox.Sandbox) *sb {
	for range maxUnwrap {
		switch x := s.(type) {
		case *sb:
			return x
		case interface{ Unwrap() sandbox.Sandbox }:
			s = x.Unwrap()
		default:
			return nil
		}
	}
	return nil
}

func (s *sb) ID() string               { return s.cfg.ID }
func (s *sb) Spec() sandbox.Spec       { return s.spec }
func (s *sb) Agent() sandbox.AgentInfo { return s.agent }
func (s *sb) outgoing(ctx context.Context) context.Context {
	if len(s.cfg.Metadata) == 0 {
		return ctx
	}
	return metadata.NewOutgoingContext(ctx, s.cfg.Metadata)
}

// Close marks the sandbox closed and tears it down once; it returns the
// first error of the teardown, and nil on every later call.
func (s *sb) Close(context.Context) error {
	if !s.close() {
		return nil
	}
	return s.teardown()
}

// teardown stops the timer, closes the client and runs the destroy step,
// once; every caller gets the first error.
func (s *sb) teardown() error {
	s.down.Do(func() {
		if t := s.timer.Load(); t != nil {
			t.Stop()
		}
		s.downErr = errors.Join(s.closer.Close(), destroy(s.cfg))
	})
	return s.downErr
}

// destroy runs cfg's destroy step, if any, without the caller's context:
// the step must run even when the caller has given up.
func destroy(cfg Config) error {
	if cfg.Destroy == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return cfg.Destroy(ctx)
}

// watch marks the sandbox unavailable when the connection to the agent
// ends. It is never redialled, so leaving Ready is the end.
func (s *sb) watch(cc *grpc.ClientConn) {
	ready := false
	for {
		st := cc.GetState()
		switch {
		case st == connectivity.Ready:
			ready = true
		case st == connectivity.Shutdown || st == connectivity.TransientFailure || ready:
			s.fail("agent connection ended")
			return
		}
		if !cc.WaitForStateChange(context.Background(), st) {
			return
		}
	}
}
